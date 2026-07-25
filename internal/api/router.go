package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"aceitcenter.local/platform/internal/core"
	"aceitcenter.local/platform/internal/domain"
	"aceitcenter.local/platform/internal/security"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const sessionCookieName = "ace_session"

func init() {
	gin.SetMode(gin.ReleaseMode)
}

type server struct {
	repo          Repository
	now           func() time.Time
	secureCookies bool
}

func NewRouter(repo Repository, now func() time.Time) http.Handler {
	return NewRouterWithOptions(repo, RouterOptions{Now: now, SecureCookies: true})
}

type RouterOptions struct {
	Now           func() time.Time
	SecureCookies bool
}

func NewRouterWithOptions(repo Repository, options RouterOptions) http.Handler {
	if options.Now == nil {
		options.Now = time.Now
	}
	s := &server{repo: repo, now: options.Now, secureCookies: options.SecureCookies}
	router := gin.New()
	router.Use(gin.Recovery())

	api := router.Group("/api/v1")
	api.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	api.GET("/auth/status", s.authStatus)
	api.POST("/auth/setup", s.setup)
	api.POST("/auth/login", s.login)

	authenticated := api.Group("")
	authenticated.Use(s.requireOwner)
	authenticated.GET("/auth/me", s.me)
	authenticated.POST("/auth/logout", s.logout)
	authenticated.GET("/organizations", s.listOrganizations)
	authenticated.POST("/organizations", s.createOrganization)
	authenticated.GET("/sites", s.listSites)
	authenticated.POST("/sites", s.createSite)
	authenticated.GET("/groups", s.listGroups)
	authenticated.POST("/groups", s.createGroup)
	authenticated.GET("/nodes", s.listNodes)
	authenticated.POST("/enrollments", s.createEnrollment)

	api.POST("/agent/enroll", s.enrollAgent)
	api.POST("/agent/heartbeat", s.recordHeartbeat)
	return router
}

func (s *server) authStatus(c *gin.Context) {
	ready, err := s.repo.IsSetup(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"setup": ready})
}

func (s *server) setup(c *gin.Context) {
	ready, err := s.repo.IsSetup(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	if ready {
		writeError(c, core.ErrConflict)
		return
	}

	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	if request.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username is required"})
		return
	}
	passwordHash, err := security.HashPassword(request.Password)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := s.now().UTC()
	owner := core.Owner{
		ID:           uuid.NewString(),
		Username:     request.Username,
		PasswordHash: passwordHash,
		CreatedAt:    now,
	}
	if err := s.repo.CreateOwner(c.Request.Context(), owner); err != nil {
		writeError(c, err)
		return
	}
	if err := s.issueSession(c, owner, now); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"owner": owner})
}

func (s *server) issueSession(c *gin.Context, owner core.Owner, now time.Time) error {
	plain, hash, err := security.NewOpaqueToken()
	if err != nil {
		return err
	}
	if err := s.repo.CreateSession(c.Request.Context(), core.Session{
		ID:        uuid.NewString(),
		OwnerID:   owner.ID,
		TokenHash: hash,
		ExpiresAt: now.Add(12 * time.Hour),
		CreatedAt: now,
	}); err != nil {
		return err
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(sessionCookieName, plain, int((12 * time.Hour).Seconds()), "/", "", s.secureCookies, true)
	return nil
}

func (s *server) login(c *gin.Context) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	owner, err := s.repo.OwnerByUsername(c.Request.Context(), strings.TrimSpace(request.Username))
	if err != nil || !security.VerifyPassword(owner.PasswordHash, request.Password) {
		writeError(c, core.ErrUnauthorized)
		return
	}
	if err := s.issueSession(c, owner, s.now().UTC()); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"owner": owner})
}

func (s *server) me(c *gin.Context) {
	owner, _ := c.Get("owner")
	c.JSON(http.StatusOK, gin.H{"owner": owner})
}

func (s *server) logout(c *gin.Context) {
	plain, _ := c.Cookie(sessionCookieName)
	if err := s.repo.DeleteSession(c.Request.Context(), hashToken(plain)); err != nil {
		writeError(c, err)
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(sessionCookieName, "", -1, "/", "", s.secureCookies, true)
	c.Status(http.StatusNoContent)
}

func (s *server) requireOwner(c *gin.Context) {
	plain, err := c.Cookie(sessionCookieName)
	if err != nil || plain == "" {
		writeError(c, core.ErrUnauthorized)
		c.Abort()
		return
	}
	owner, err := s.repo.OwnerBySessionHash(c.Request.Context(), hashToken(plain), s.now().UTC())
	if err != nil {
		writeError(c, core.ErrUnauthorized)
		c.Abort()
		return
	}
	c.Set("owner", owner)
	c.Next()
}

func (s *server) listOrganizations(c *gin.Context) {
	organizations, err := s.repo.ListOrganizations(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": organizations})
}

func (s *server) createOrganization(c *gin.Context) {
	var request struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "organization name is required"})
		return
	}
	organization := core.Organization{ID: uuid.NewString(), Name: request.Name, CreatedAt: s.now().UTC()}
	if err := s.repo.CreateOrganization(c.Request.Context(), organization); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, organization)
}

func (s *server) listSites(c *gin.Context) {
	items, err := s.repo.ListSites(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *server) createSite(c *gin.Context) {
	var request struct {
		OrganizationID string `json:"organization_id"`
		Name           string `json:"name"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.OrganizationID == "" || request.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "organization_id and name are required"})
		return
	}
	item := core.Site{
		ID:             uuid.NewString(),
		OrganizationID: request.OrganizationID,
		Name:           request.Name,
		CreatedAt:      s.now().UTC(),
	}
	if err := s.repo.CreateSite(c.Request.Context(), item); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (s *server) listGroups(c *gin.Context) {
	items, err := s.repo.ListGroups(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *server) createGroup(c *gin.Context) {
	var request struct {
		SiteID string `json:"site_id"`
		Name   string `json:"name"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.SiteID == "" || request.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "site_id and name are required"})
		return
	}
	item := core.NodeGroup{ID: uuid.NewString(), SiteID: request.SiteID, Name: request.Name, CreatedAt: s.now().UTC()}
	if err := s.repo.CreateGroup(c.Request.Context(), item); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (s *server) listNodes(c *gin.Context) {
	items, err := s.repo.ListNodes(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "server_time": s.now().UTC()})
}

func (s *server) createEnrollment(c *gin.Context) {
	var request struct {
		GroupID        string `json:"group_id"`
		ExpiresMinutes int    `json:"expires_minutes"`
		MaxUses        int    `json:"max_uses"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if request.GroupID == "" || request.ExpiresMinutes < 1 || request.ExpiresMinutes > 10080 || request.MaxUses < 1 || request.MaxUses > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid enrollment limits"})
		return
	}
	plain, hash, err := security.NewOpaqueToken()
	if err != nil {
		writeError(c, err)
		return
	}
	now := s.now().UTC()
	item := core.Enrollment{
		ID:        uuid.NewString(),
		GroupID:   request.GroupID,
		TokenHash: hash,
		ExpiresAt: now.Add(time.Duration(request.ExpiresMinutes) * time.Minute),
		MaxUses:   request.MaxUses,
		CreatedAt: now,
	}
	if err := s.repo.CreateEnrollment(c.Request.Context(), item); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"enrollment": item, "token": plain})
}

func (s *server) enrollAgent(c *gin.Context) {
	var request core.EnrollRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.Hostname = strings.TrimSpace(request.Hostname)
	if request.Token == "" || request.Hostname == "" || request.Version == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "token, hostname and version are required"})
		return
	}
	if err := domain.ValidateNodeType(request.Type); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	plainCredential, credentialHash, err := security.NewOpaqueToken()
	if err != nil {
		writeError(c, err)
		return
	}
	node, err := s.repo.EnrollNode(
		c.Request.Context(),
		hashToken(request.Token),
		credentialHash,
		request,
		s.now().UTC(),
	)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"node": node, "credential": plainCredential})
}

func (s *server) recordHeartbeat(c *gin.Context) {
	authorization := c.GetHeader("Authorization")
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authorization, bearerPrefix) || len(authorization) == len(bearerPrefix) {
		writeError(c, core.ErrUnauthorized)
		return
	}
	var heartbeat core.Heartbeat
	if err := c.ShouldBindJSON(&heartbeat); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	node, err := s.repo.RecordHeartbeat(
		c.Request.Context(),
		hashToken(strings.TrimPrefix(authorization, bearerPrefix)),
		heartbeat,
		s.now().UTC(),
	)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"node": node})
}

func hashToken(token string) string {
	return security.HashToken(token)
}

func writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
	case errors.Is(err, core.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "resource already exists"})
	case errors.Is(err, core.ErrUnauthorized), errors.Is(err, core.ErrEnrollmentExpired):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}
