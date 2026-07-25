package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aceitcenter.local/platform/internal/core"
	"aceitcenter.local/platform/internal/security"
)

var fixedNow = time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)

type fakeRepository struct {
	setup         bool
	owner         core.Owner
	sessions      map[string]core.Owner
	organizations []core.Organization
	sites         []core.Site
	groups        []core.NodeGroup
	nodes         []core.Node
	enrollments   []core.Enrollment
	enrollResult  core.Node
	enrollErr     error
	enrollHash    string
	deviceHash    string
	heartbeatHash string
	heartbeat     core.Heartbeat
}

func (f *fakeRepository) IsSetup(context.Context) (bool, error) {
	return f.setup, nil
}

func (f *fakeRepository) CreateOwner(_ context.Context, owner core.Owner) error {
	if f.setup {
		return core.ErrConflict
	}
	f.setup = true
	f.owner = owner
	return nil
}

func (f *fakeRepository) OwnerByUsername(_ context.Context, username string) (core.Owner, error) {
	if !f.setup || username != f.owner.Username {
		return core.Owner{}, core.ErrNotFound
	}
	return f.owner, nil
}

func (f *fakeRepository) CreateSession(_ context.Context, session core.Session) error {
	if f.sessions == nil {
		f.sessions = make(map[string]core.Owner)
	}
	f.sessions[session.TokenHash] = f.owner
	return nil
}

func (f *fakeRepository) OwnerBySessionHash(_ context.Context, hash string, now time.Time) (core.Owner, error) {
	owner, ok := f.sessions[hash]
	if !ok {
		return core.Owner{}, core.ErrUnauthorized
	}
	return owner, nil
}

func (f *fakeRepository) DeleteSession(_ context.Context, hash string) error {
	delete(f.sessions, hash)
	return nil
}

func (f *fakeRepository) ListOrganizations(context.Context) ([]core.Organization, error) {
	return f.organizations, nil
}

func (f *fakeRepository) CreateOrganization(_ context.Context, organization core.Organization) error {
	f.organizations = append(f.organizations, organization)
	return nil
}

func (f *fakeRepository) ListSites(context.Context) ([]core.Site, error) {
	return f.sites, nil
}

func (f *fakeRepository) CreateSite(_ context.Context, site core.Site) error {
	f.sites = append(f.sites, site)
	return nil
}

func (f *fakeRepository) ListGroups(context.Context) ([]core.NodeGroup, error) {
	return f.groups, nil
}

func (f *fakeRepository) CreateGroup(_ context.Context, group core.NodeGroup) error {
	f.groups = append(f.groups, group)
	return nil
}

func (f *fakeRepository) ListNodes(context.Context) ([]core.Node, error) {
	return f.nodes, nil
}

func (f *fakeRepository) CreateEnrollment(_ context.Context, enrollment core.Enrollment) error {
	f.enrollments = append(f.enrollments, enrollment)
	return nil
}

func (f *fakeRepository) EnrollNode(_ context.Context, enrollmentHash, deviceHash string, request core.EnrollRequest, now time.Time) (core.Node, error) {
	f.enrollHash = enrollmentHash
	f.deviceHash = deviceHash
	return f.enrollResult, f.enrollErr
}

func (f *fakeRepository) RecordHeartbeat(_ context.Context, hash string, heartbeat core.Heartbeat, _ time.Time) (core.Node, error) {
	f.heartbeatHash = hash
	f.heartbeat = heartbeat
	if len(f.nodes) == 0 {
		return core.Node{}, core.ErrUnauthorized
	}
	return f.nodes[0], nil
}

func requestJSON(t *testing.T, handler http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestSetupCreatesOwnerAndAuthenticatedSession(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	response := requestJSON(t, router, http.MethodPost, "/api/v1/auth/setup", map[string]string{
		"username": "jarvis",
		"password": "correct-horse-battery-staple",
	}, nil)

	if response.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, want %d; body=%s", response.Code, http.StatusCreated, response.Body.String())
	}
	if repo.owner.Username != "jarvis" || repo.owner.PasswordHash == "" {
		t.Fatalf("owner = %#v, want stored username and password hash", repo.owner)
	}
	if len(response.Result().Cookies()) != 1 || response.Result().Cookies()[0].Name != sessionCookieName {
		t.Fatalf("setup cookies = %#v, want one %q cookie", response.Result().Cookies(), sessionCookieName)
	}
}

func TestRouterOptionsAllowExplicitLocalHTTPCookie(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{}
	router := NewRouterWithOptions(repo, RouterOptions{
		Now:           func() time.Time { return fixedNow },
		SecureCookies: false,
	})
	response := requestJSON(t, router, http.MethodPost, "/api/v1/auth/setup", map[string]string{
		"username": "jarvis",
		"password": "correct-horse-battery-staple",
	}, nil)

	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Secure {
		t.Fatalf("setup cookies = %#v, want explicit non-secure local cookie", cookies)
	}
}

func TestOrganizationsRequireAuthentication(t *testing.T) {
	t.Parallel()

	router := NewRouter(&fakeRepository{}, func() time.Time { return fixedNow })
	response := requestJSON(t, router, http.MethodGet, "/api/v1/organizations", nil, nil)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("organizations status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestCreateOrganizationTrimsName(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{sessions: map[string]core.Owner{}}
	plainSession := "authenticated-session"
	repo.sessions[hashToken(plainSession)] = core.Owner{ID: "owner-1", Username: "jarvis"}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	response := requestJSON(t, router, http.MethodPost, "/api/v1/organizations", map[string]string{
		"name": "  万禾公司  ",
	}, &http.Cookie{Name: sessionCookieName, Value: plainSession})

	if response.Code != http.StatusCreated {
		t.Fatalf("create organization status = %d, want %d; body=%s", response.Code, http.StatusCreated, response.Body.String())
	}
	if len(repo.organizations) != 1 || repo.organizations[0].Name != "万禾公司" {
		t.Fatalf("organizations = %#v, want trimmed name", repo.organizations)
	}
}

func TestEnrollReturnsCredentialOnlyWhenEnrollmentIsAccepted(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{enrollResult: core.Node{ID: "node-1", Name: "office-pc", Type: "windows"}}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	response := requestJSON(t, router, http.MethodPost, "/api/v1/agent/enroll", core.EnrollRequest{
		Token:    "one-time-enrollment",
		Hostname: "office-pc",
		Type:     "windows",
		Version:  "0.1.0",
	}, nil)

	if response.Code != http.StatusCreated {
		t.Fatalf("enroll status = %d, want %d; body=%s", response.Code, http.StatusCreated, response.Body.String())
	}
	var payload struct {
		Node       core.Node `json:"node"`
		Credential string    `json:"credential"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode enroll response: %v", err)
	}
	if payload.Credential == "" || payload.Node.ID != "node-1" {
		t.Fatalf("enroll payload = %#v, want node and one-time credential", payload)
	}
	if repo.deviceHash == "" || repo.deviceHash == payload.Credential {
		t.Fatal("repository did not receive a non-plaintext device credential hash")
	}
}

func TestEnrollRejectsExpiredEnrollment(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{enrollErr: core.ErrEnrollmentExpired}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	response := requestJSON(t, router, http.MethodPost, "/api/v1/agent/enroll", core.EnrollRequest{
		Token:    "expired-enrollment",
		Hostname: "office-pc",
		Type:     "windows",
		Version:  "0.1.0",
	}, nil)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired enrollment status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if !errors.Is(repo.enrollErr, core.ErrEnrollmentExpired) {
		t.Fatal("test repository is not configured with enrollment expiration")
	}
}

func TestLoginRejectsWrongPasswordAndAcceptsCorrectPassword(t *testing.T) {
	t.Parallel()

	passwordHash, err := security.HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("hash fixture password: %v", err)
	}
	repo := &fakeRepository{
		setup: true,
		owner: core.Owner{ID: "owner-1", Username: "jarvis", PasswordHash: passwordHash},
	}
	router := NewRouter(repo, func() time.Time { return fixedNow })

	wrong := requestJSON(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "jarvis",
		"password": "wrong-password-value",
	}, nil)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want %d", wrong.Code, http.StatusUnauthorized)
	}

	correct := requestJSON(t, router, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "jarvis",
		"password": "correct-horse-battery-staple",
	}, nil)
	if correct.Code != http.StatusOK || len(correct.Result().Cookies()) != 1 {
		t.Fatalf("correct login status = %d cookies=%d, want 200 and session cookie", correct.Code, len(correct.Result().Cookies()))
	}
}

func TestCreateSiteAndGroupPreservesHierarchy(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	router := NewRouter(repo, func() time.Time { return fixedNow })
	cookie := &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"}

	siteResponse := requestJSON(t, router, http.MethodPost, "/api/v1/sites", map[string]string{
		"organization_id": "org-1",
		"name":            "  办公室  ",
	}, cookie)
	if siteResponse.Code != http.StatusCreated || len(repo.sites) != 1 {
		t.Fatalf("site status=%d sites=%#v", siteResponse.Code, repo.sites)
	}
	if repo.sites[0].OrganizationID != "org-1" || repo.sites[0].Name != "办公室" {
		t.Fatalf("site = %#v, want org-1/办公室", repo.sites[0])
	}

	groupResponse := requestJSON(t, router, http.MethodPost, "/api/v1/groups", map[string]string{
		"site_id": "site-1",
		"name":    "  财务电脑  ",
	}, cookie)
	if groupResponse.Code != http.StatusCreated || len(repo.groups) != 1 {
		t.Fatalf("group status=%d groups=%#v", groupResponse.Code, repo.groups)
	}
	if repo.groups[0].SiteID != "site-1" || repo.groups[0].Name != "财务电脑" {
		t.Fatalf("group = %#v, want site-1/财务电脑", repo.groups[0])
	}
}

func TestCreateEnrollmentReturnsPlainTokenAndStoresOnlyHash(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	router := NewRouter(repo, func() time.Time { return fixedNow })
	response := requestJSON(t, router, http.MethodPost, "/api/v1/enrollments", map[string]any{
		"group_id":        "group-1",
		"expires_minutes": 60,
		"max_uses":        3,
	}, &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})

	if response.Code != http.StatusCreated || len(repo.enrollments) != 1 {
		t.Fatalf("enrollment status=%d enrollments=%#v body=%s", response.Code, repo.enrollments, response.Body.String())
	}
	var payload struct {
		Token      string          `json:"token"`
		Enrollment core.Enrollment `json:"enrollment"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode enrollment response: %v", err)
	}
	if payload.Token == "" || repo.enrollments[0].TokenHash == payload.Token {
		t.Fatal("enrollment did not return plaintext once and store only its hash")
	}
	if !repo.enrollments[0].ExpiresAt.Equal(fixedNow.Add(time.Hour)) || repo.enrollments[0].MaxUses != 3 {
		t.Fatalf("enrollment = %#v, want one-hour expiry and three uses", repo.enrollments[0])
	}
}

func TestHeartbeatAuthenticatesDeviceCredentialAndRecordsSnapshot(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{nodes: []core.Node{{ID: "node-1", Name: "office-pc"}}}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	body := core.Heartbeat{Hostname: "office-pc", AgentVersion: "0.1.0", CPUPercent: 23.5, MemoryPercent: 51.2}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer device-secret")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if repo.heartbeatHash != hashToken("device-secret") || repo.heartbeat.CPUPercent != 23.5 {
		t.Fatalf("recorded heartbeat hash=%q heartbeat=%#v", repo.heartbeatHash, repo.heartbeat)
	}
}

func authenticatedRepository() *fakeRepository {
	plainSession := "authenticated-session"
	return &fakeRepository{
		sessions: map[string]core.Owner{
			hashToken(plainSession): {ID: "owner-1", Username: "jarvis"},
		},
	}
}
