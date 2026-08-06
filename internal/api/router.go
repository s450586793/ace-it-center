package api

import (
	"encoding/base64"
	"errors"
	"log"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	repo                Repository
	commands            CommandRepository
	systemUpdater       SystemUpdater
	now                 func() time.Time
	secureCookies       bool
	pairingLimiter      PairingLimiter
	commandPollDuration time.Duration
	commandPollInterval time.Duration
}

func NewRouter(repo Repository, now func() time.Time) http.Handler {
	return NewRouterWithOptions(repo, RouterOptions{Now: now, SecureCookies: true})
}

type RouterOptions struct {
	Now                 func() time.Time
	SecureCookies       bool
	SystemUpdater       SystemUpdater
	PairingLimiter      PairingLimiter
	CommandPollDuration time.Duration
	CommandPollInterval time.Duration
}

type PairingLimiter interface {
	Allow(string, string, time.Time) bool
}

const (
	pairingRateLimit           = 10
	pairingRateWindowDuration  = time.Minute
	pairingLimiterMaxWindows   = 4096
	maxAgentLogBytes           = 64 << 10
	maxAgentLogRequestBytes    = 160 << 10
	maxCommandRequestBytes     = 64 << 10
	maxCompletionRequestBytes  = 300 << 10
	maxCommandErrorBytes       = 512
	defaultCommandPollDuration = 20 * time.Second
	defaultCommandPollInterval = time.Second
	commandLeaseDuration       = 35 * time.Minute
)

type pairingLimiter struct {
	mu      sync.Mutex
	windows map[string]pairingRateWindow
}

type pairingRateWindow struct {
	startedAt time.Time
	count     int
}

func newPairingLimiter() *pairingLimiter {
	return &pairingLimiter{windows: make(map[string]pairingRateWindow, pairingLimiterMaxWindows)}
}

func (l *pairingLimiter) Allow(remoteAddr, machineID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.evictExpired(now)
	remoteKey := "remote:" + remoteAddr
	machineKey := "machine:" + machineID
	remoteWindow, remoteExists := l.windows[remoteKey]
	machineWindow, machineExists := l.windows[machineKey]
	if remoteWindow.count >= pairingRateLimit || machineWindow.count >= pairingRateLimit {
		return false
	}
	newWindows := 0
	if !remoteExists {
		newWindows++
		remoteWindow = pairingRateWindow{startedAt: now}
	}
	if !machineExists {
		newWindows++
		machineWindow = pairingRateWindow{startedAt: now}
	}
	if len(l.windows)+newWindows > pairingLimiterMaxWindows {
		return false
	}
	remoteWindow.count++
	machineWindow.count++
	l.windows[remoteKey] = remoteWindow
	l.windows[machineKey] = machineWindow
	return true
}

func (l *pairingLimiter) evictExpired(now time.Time) {
	for key, window := range l.windows {
		if !now.Before(window.startedAt.Add(pairingRateWindowDuration)) {
			delete(l.windows, key)
		}
	}
}

func NewRouterWithOptions(repo Repository, options RouterOptions) http.Handler {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.PairingLimiter == nil {
		options.PairingLimiter = newPairingLimiter()
	}
	if options.CommandPollDuration <= 0 {
		options.CommandPollDuration = defaultCommandPollDuration
	}
	if options.CommandPollInterval <= 0 {
		options.CommandPollInterval = defaultCommandPollInterval
	}
	commandRepository, _ := repo.(CommandRepository)
	s := &server{
		repo: repo, commands: commandRepository, systemUpdater: options.SystemUpdater, now: options.Now, secureCookies: options.SecureCookies,
		pairingLimiter: options.PairingLimiter, commandPollDuration: options.CommandPollDuration,
		commandPollInterval: options.CommandPollInterval,
	}
	router := gin.New()
	router.Use(gin.Recovery())

	api := router.Group("/api/v1")
	api.GET("/health", s.health)
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
	authenticated.PATCH("/nodes/:id", s.updateNodeRemark)
	authenticated.GET("/nodes/:id/logs", s.getAgentLogs)
	authenticated.GET("/nodes/:id/network-history", s.listNetworkHistory)
	authenticated.GET("/network/summary", s.listNetworkSummary)
	authenticated.POST("/enrollments", s.createEnrollment)
	authenticated.GET("/pairings", s.listPairings)
	authenticated.POST("/pairings/:id/approve", s.approvePairing)
	authenticated.POST("/pairings/:id/reject", s.rejectPairing)
	authenticated.POST("/commands", s.createCommand)
	authenticated.GET("/commands", s.listCommands)
	authenticated.GET("/commands/:id", s.getCommand)
	authenticated.POST("/commands/:id/retry", s.retryCommand)
	authenticated.GET("/system/update", s.systemUpdateStatus)
	authenticated.POST("/system/update/check", s.checkSystemUpdate)
	authenticated.POST("/system/update", s.startSystemUpdate)

	api.POST("/agent/enroll", s.enrollAgent)
	api.POST("/agent/pairings", s.createPairing)
	api.GET("/agent/pairings/:id", s.pollPairing)
	api.POST("/agent/heartbeat", s.recordHeartbeat)
	api.POST("/agent/logs", s.recordAgentLogs)
	api.POST("/agent/commands/claim", s.claimCommand)
	api.POST("/agent/commands/:id/start", s.startCommand)
	api.POST("/agent/commands/:id/complete", s.completeCommand)
	return router
}

func (s *server) health(c *gin.Context) {
	if _, err := s.repo.IsSetup(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
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
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group name is required"})
		return
	}
	item := core.NodeGroup{ID: uuid.NewString(), Name: request.Name, CreatedAt: s.now().UTC()}
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

func (s *server) updateNodeRemark(c *gin.Context) {
	var request struct {
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.Remark = strings.TrimSpace(request.Remark)
	if utf8.RuneCountInString(request.Remark) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "remark must not exceed 500 characters"})
		return
	}
	node, err := s.repo.UpdateNodeRemark(c.Request.Context(), c.Param("id"), request.Remark)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"node": node})
}

func (s *server) getAgentLogs(c *gin.Context) {
	snapshot, err := s.repo.GetAgentLogs(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func (s *server) listNetworkHistory(c *gin.Context) {
	rangeValue := c.Query("range")
	since, bucket, ok := networkRange(s.now().UTC(), rangeValue)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid range"})
		return
	}
	points, err := s.repo.ListNetworkHistory(c.Request.Context(), c.Param("id"), since, bucket)
	if err != nil {
		writeError(c, err)
		return
	}
	if points == nil {
		points = make([]core.NetworkHistoryPoint, 0)
	}
	c.JSON(http.StatusOK, gin.H{
		"node_id": c.Param("id"),
		"range":   rangeValue,
		"unit":    "MB/s",
		"points":  points,
	})
}

func (s *server) listNetworkSummary(c *gin.Context) {
	rangeValue := c.Query("range")
	since, _, ok := networkRange(s.now().UTC(), rangeValue)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid range"})
		return
	}
	items, err := s.repo.ListNetworkSummary(c.Request.Context(), since)
	if err != nil {
		writeError(c, err)
		return
	}
	if items == nil {
		items = make([]core.NetworkSummaryItem, 0)
	}
	c.JSON(http.StatusOK, gin.H{
		"range": rangeValue,
		"unit":  "MB/s",
		"items": items,
	})
}

func networkRange(now time.Time, value string) (time.Time, time.Duration, bool) {
	switch value {
	case "24h":
		return now.Add(-24 * time.Hour), 5 * time.Minute, true
	case "7d":
		return now.Add(-168 * time.Hour), 30 * time.Minute, true
	case "30d":
		return now.Add(-720 * time.Hour), 2 * time.Hour, true
	case "90d":
		return now.Add(-2160 * time.Hour), 6 * time.Hour, true
	default:
		return time.Time{}, 0, false
	}
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

func (s *server) createPairing(c *gin.Context) {
	var request core.PairingCreateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.Hostname = strings.TrimSpace(request.Hostname)
	request.MachineID = strings.TrimSpace(request.MachineID)
	request.AgentVersion = strings.TrimSpace(request.AgentVersion)
	if request.Hostname == "" || request.MachineID == "" || request.AgentVersion == "" || request.PairingCredential == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hostname, machine_id, agent_version and pairing_credential are required"})
		return
	}
	if !isPairingCredential(request.PairingCredential) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pairing_credential must be a 32-byte base64url value"})
		return
	}
	if err := domain.ValidateNodeType(request.Type); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	now := s.now().UTC()
	if !s.pairingLimiter.Allow(pairingRemoteAddress(c.Request.RemoteAddr), request.MachineID, now) {
		writeError(c, core.ErrRateLimited)
		return
	}
	pairing, err := s.repo.CreatePairingRequest(c.Request.Context(), core.PairingRequest{
		ID:             uuid.NewString(),
		MachineID:      request.MachineID,
		Hostname:       request.Hostname,
		Type:           request.Type,
		AgentVersion:   request.AgentVersion,
		CredentialHash: hashToken(request.PairingCredential),
		State:          core.PairingPending,
	}, now)
	if err != nil {
		writeError(c, err)
		return
	}
	logPairing(pairing.ID, pairing.MachineID, pairing.State)
	c.JSON(http.StatusCreated, gin.H{
		"pairing_id":         pairing.ID,
		"state":              pairing.State,
		"expires_at":         pairing.ExpiresAt,
		"poll_after_seconds": 5,
	})
}

func (s *server) pollPairing(c *gin.Context) {
	credential, ok := bearerCredential(c)
	if !ok {
		writeError(c, core.ErrUnauthorized)
		return
	}
	pairing, err := s.repo.GetPairingRequest(c.Request.Context(), c.Param("id"), hashToken(credential), s.now().UTC())
	if err != nil {
		writeError(c, err)
		return
	}
	response := core.PairingPollResult{
		ID:        pairing.ID,
		State:     pairing.State,
		ExpiresAt: pairing.ExpiresAt,
	}
	if pairing.State == core.PairingApproved {
		response.Node = pairing.ExistingNode
	}
	logPairing(pairing.ID, pairing.MachineID, pairing.State)
	if pairing.State == core.PairingRejected || pairing.State == core.PairingExpired {
		c.JSON(http.StatusGone, response)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (s *server) listPairings(c *gin.Context) {
	items, err := s.repo.ListPendingPairingRequests(c.Request.Context(), s.now().UTC())
	if err != nil {
		writeError(c, err)
		return
	}
	if items == nil {
		items = make([]core.PairingRequest, 0)
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *server) approvePairing(c *gin.Context) {
	var request struct {
		GroupID string `json:"group_id"`
		Remark  string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.GroupID = strings.TrimSpace(request.GroupID)
	request.Remark = strings.TrimSpace(request.Remark)
	if request.GroupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group_id is required"})
		return
	}
	node, err := s.repo.ApprovePairingRequest(c.Request.Context(), c.Param("id"), request.GroupID, request.Remark, s.now().UTC())
	if err != nil {
		writeError(c, err)
		return
	}
	logPairing(c.Param("id"), "", core.PairingApproved)
	c.JSON(http.StatusOK, gin.H{
		"node": node,
		"pairing": gin.H{
			"id":       c.Param("id"),
			"state":    core.PairingApproved,
			"group_id": request.GroupID,
			"remark":   request.Remark,
		},
	})
}

func (s *server) rejectPairing(c *gin.Context) {
	if err := s.repo.RejectPairingRequest(c.Request.Context(), c.Param("id"), s.now().UTC()); err != nil {
		writeError(c, err)
		return
	}
	logPairing(c.Param("id"), "", core.PairingRejected)
	c.Status(http.StatusNoContent)
}

func bearerCredential(c *gin.Context) (string, bool) {
	const bearerPrefix = "Bearer "
	authorization := c.GetHeader("Authorization")
	if !strings.HasPrefix(authorization, bearerPrefix) || len(authorization) == len(bearerPrefix) {
		return "", false
	}
	return strings.TrimPrefix(authorization, bearerPrefix), true
}

func isPairingCredential(value string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == 32
}

func pairingRemoteAddress(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func logPairing(pairingID, machineID string, state core.PairingState) {
	machineHash := ""
	if machineID != "" {
		machineHash = hashToken(machineID)[:12]
	}
	log.Printf("pairing id=%s machine_hash=%s state=%s", pairingID, machineHash, state)
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
	if err := validateNetworkUsage(heartbeat); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid network usage"})
		return
	}
	heartbeat.NetworkUploadMBPerSecond = normalizeNetworkRate(heartbeat.NetworkUploadMBPerSecond)
	heartbeat.NetworkDownloadMBPerSecond = normalizeNetworkRate(heartbeat.NetworkDownloadMBPerSecond)
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

func validateNetworkUsage(heartbeat core.Heartbeat) error {
	if !heartbeat.NetworkUsageAvailable {
		if heartbeat.NetworkUsageDay != "" || heartbeat.NetworkTodayUploadBytes != 0 ||
			heartbeat.NetworkTodayDownloadBytes != 0 || heartbeat.NetworkMonthUploadBytes != 0 ||
			heartbeat.NetworkMonthDownloadBytes != 0 {
			return errors.New("network usage fields require availability")
		}
		return nil
	}
	if heartbeat.NetworkUsageDay == "" {
		return errors.New("network usage day is required")
	}
	parsed, err := time.Parse("2006-01-02", heartbeat.NetworkUsageDay)
	if err != nil || parsed.Format("2006-01-02") != heartbeat.NetworkUsageDay {
		return errors.New("network usage day is invalid")
	}
	return nil
}

func (s *server) recordAgentLogs(c *gin.Context) {
	credential, ok := bearerCredential(c)
	if !ok {
		writeError(c, core.ErrUnauthorized)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAgentLogRequestBytes)
	var logs core.AgentLogUpload
	if err := c.ShouldBindJSON(&logs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log upload"})
		return
	}
	if !validAgentLog(logs.AgentLog) || !validAgentLog(logs.UpdateLog) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "log entries must be valid UTF-8 and not exceed 64 KiB"})
		return
	}
	snapshot, err := s.repo.RecordAgentLogs(c.Request.Context(), hashToken(credential), logs, s.now().UTC())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"accepted": true, "captured_at": snapshot.CapturedAt})
}

func validAgentLog(value string) bool {
	return utf8.ValidString(value) && len(value) <= maxAgentLogBytes
}

func normalizeNetworkRate(rate float64) float64 {
	if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0
	}
	return rate
}

func (s *server) createCommand(c *gin.Context) {
	if s.commands == nil {
		writeError(c, errors.New("command repository unavailable"))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCommandRequestBytes)
	var request struct {
		NodeIDs        []string          `json:"node_ids"`
		Shell          core.CommandShell `json:"shell"`
		Command        string            `json:"command"`
		TimeoutSeconds int               `json:"timeout_seconds"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid command request"})
		return
	}
	if err := validateCommandNodeIDs(request.NodeIDs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := core.ValidateCommand(request.Shell, request.Command, request.TimeoutSeconds); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ownerValue, exists := c.Get("owner")
	owner, ok := ownerValue.(core.Owner)
	if !exists || !ok {
		writeError(c, core.ErrUnauthorized)
		return
	}
	detail, err := s.commands.CreateCommand(c.Request.Context(), core.CommandTask{
		ID:             uuid.NewString(),
		Shell:          request.Shell,
		Command:        request.Command,
		TimeoutSeconds: request.TimeoutSeconds,
		CreatedBy:      owner.ID,
		CreatedAt:      s.now().UTC(),
	}, request.NodeIDs)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, detail)
}

func (s *server) listCommands(c *gin.Context) {
	if s.commands == nil {
		writeError(c, errors.New("command repository unavailable"))
		return
	}
	items, err := s.commands.ListCommands(c.Request.Context(), 100)
	if err != nil {
		writeError(c, err)
		return
	}
	if items == nil {
		items = make([]core.CommandTask, 0)
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *server) getCommand(c *gin.Context) {
	if s.commands == nil {
		writeError(c, errors.New("command repository unavailable"))
		return
	}
	detail, err := s.commands.GetCommand(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *server) retryCommand(c *gin.Context) {
	if s.commands == nil {
		writeError(c, errors.New("command repository unavailable"))
		return
	}
	ownerValue, exists := c.Get("owner")
	owner, ok := ownerValue.(core.Owner)
	if !exists || !ok {
		writeError(c, core.ErrUnauthorized)
		return
	}
	detail, err := s.commands.RetryCommand(c.Request.Context(), core.CommandTask{
		ID:        uuid.NewString(),
		CreatedBy: owner.ID,
		CreatedAt: s.now().UTC(),
	}, c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, detail)
}

func (s *server) claimCommand(c *gin.Context) {
	if s.commands == nil {
		writeError(c, errors.New("command repository unavailable"))
		return
	}
	credential, ok := bearerCredential(c)
	if !ok {
		writeError(c, core.ErrUnauthorized)
		return
	}

	timer := time.NewTimer(s.commandPollDuration)
	defer timer.Stop()
	ticker := time.NewTicker(s.commandPollInterval)
	defer ticker.Stop()
	for {
		plainLease, leaseHash, err := security.NewOpaqueToken()
		if err != nil {
			writeError(c, err)
			return
		}
		claim, found, err := s.commands.ClaimCommand(
			c.Request.Context(), hashToken(credential), leaseHash, s.now().UTC(), commandLeaseDuration,
		)
		if err != nil {
			writeError(c, err)
			return
		}
		if found {
			claim.LeaseToken = plainLease
			c.JSON(http.StatusOK, claim)
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-timer.C:
			c.Status(http.StatusNoContent)
			return
		case <-ticker.C:
		}
	}
}

func (s *server) startCommand(c *gin.Context) {
	if s.commands == nil {
		writeError(c, errors.New("command repository unavailable"))
		return
	}
	credential, ok := bearerCredential(c)
	if !ok {
		writeError(c, core.ErrUnauthorized)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	var request struct {
		LeaseToken string `json:"lease_token"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.LeaseToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lease_token is required"})
		return
	}
	if err := s.commands.StartCommand(
		c.Request.Context(), hashToken(credential), c.Param("id"), hashToken(request.LeaseToken), s.now().UTC(),
	); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"accepted": true})
}

func (s *server) completeCommand(c *gin.Context) {
	if s.commands == nil {
		writeError(c, errors.New("command repository unavailable"))
		return
	}
	credential, ok := bearerCredential(c)
	if !ok {
		writeError(c, core.ErrUnauthorized)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCompletionRequestBytes)
	var completion core.CommandCompletion
	if err := c.ShouldBindJSON(&completion); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid command completion"})
		return
	}
	completion.ExecutionID = c.Param("id")
	if !validCommandCompletionPayload(completion) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid command completion"})
		return
	}
	leaseHash := hashToken(completion.LeaseToken)
	completion.LeaseToken = ""
	if err := s.commands.CompleteCommand(
		c.Request.Context(), hashToken(credential), leaseHash, completion, s.now().UTC(),
	); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"accepted": true})
}

func validateCommandNodeIDs(nodeIDs []string) error {
	if len(nodeIDs) == 0 {
		return errors.New("node_ids is required")
	}
	seen := make(map[string]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		if strings.TrimSpace(nodeID) == "" {
			return errors.New("node_ids contains an empty value")
		}
		if _, exists := seen[nodeID]; exists {
			return errors.New("node_ids contains a duplicate value")
		}
		seen[nodeID] = struct{}{}
	}
	return nil
}

func validCommandCompletionPayload(completion core.CommandCompletion) bool {
	return completion.ExecutionID != "" && completion.LeaseToken != "" && completion.Status.Terminal() &&
		completion.DurationMS >= 0 && utf8.ValidString(completion.Output) &&
		len(completion.Output) <= core.MaxCommandOutputBytes && utf8.ValidString(completion.ErrorMessage) &&
		len(completion.ErrorMessage) <= maxCommandErrorBytes
}

func hashToken(token string) string {
	return security.HashToken(token)
}

func writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
	case errors.Is(err, core.ErrPairingExpired):
		c.JSON(http.StatusGone, gin.H{"error": "pairing expired"})
	case errors.Is(err, core.ErrRateLimited):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "pairing request rate limit exceeded"})
	case errors.Is(err, core.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "resource already exists"})
	case errors.Is(err, core.ErrUnauthorized), errors.Is(err, core.ErrEnrollmentExpired):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}
