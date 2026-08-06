package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aceitcenter.local/platform/internal/core"
	"aceitcenter.local/platform/internal/security"
)

var fixedNow = time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)

type fakeRepository struct {
	setup         bool
	setupErr      error
	owner         core.Owner
	sessions      map[string]core.Owner
	organizations []core.Organization
	sites         []core.Site
	groups        []core.NodeGroup
	nodes         []core.Node
	nodeRemarkID  string
	nodeRemark    string
	nodeUpdateErr error
	enrollments   []core.Enrollment
	enrollResult  core.Node
	enrollErr     error
	enrollHash    string
	deviceHash    string
	heartbeatHash string
	heartbeat     core.Heartbeat
	agentLogHash  string
	agentLog      string
	updateLog     string
	history       []core.NetworkHistoryPoint
	historyErr    error
	historyNodeID string
	historySince  time.Time
	historyBucket time.Duration
	historyCalls  int
	summary       []core.NetworkSummaryItem
	summaryErr    error
	summarySince  time.Time
	summaryCalls  int
	pairings      []core.PairingRequest
	pairingCreate core.PairingRequest
	pairingResult core.PairingRequest
	pairingErr    error
	pairingHash   string
	pairingNode   core.Node
	pairingGroup  string
	pairingRemark string
	pairingID     string
}

func (f *fakeRepository) IsSetup(context.Context) (bool, error) {
	return f.setup, f.setupErr
}

func TestHealthReflectsDatabaseReachabilityWithoutSetupState(t *testing.T) {
	t.Parallel()

	for _, setup := range []bool{false, true} {
		t.Run(fmt.Sprintf("setup=%t", setup), func(t *testing.T) {
			t.Parallel()
			response := requestJSON(t, NewRouter(&fakeRepository{setup: setup}, func() time.Time { return fixedNow }), http.MethodGet, "/api/v1/health", nil, nil)
			if response.Code != http.StatusOK || response.Body.String() != `{"status":"ok"}` {
				t.Fatalf("health status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestHealthReportsUnavailableWhenDatabaseQueryFails(t *testing.T) {
	t.Parallel()

	response := requestJSON(t, NewRouter(&fakeRepository{setupErr: errors.New("postgres password database-secret")}, func() time.Time { return fixedNow }), http.MethodGet, "/api/v1/health", nil, nil)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != `{"status":"unavailable"}` {
		t.Fatalf("health status=%d body=%q", response.Code, response.Body.String())
	}
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

func (f *fakeRepository) UpdateNodeRemark(_ context.Context, id, remark string) (core.Node, error) {
	f.nodeRemarkID = id
	f.nodeRemark = remark
	if f.nodeUpdateErr != nil {
		return core.Node{}, f.nodeUpdateErr
	}
	for index := range f.nodes {
		if f.nodes[index].ID == id {
			f.nodes[index].Remark = remark
			return f.nodes[index], nil
		}
	}
	return core.Node{}, core.ErrNotFound
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

func (f *fakeRepository) RecordAgentLogs(_ context.Context, hash string, logs core.AgentLogUpload, now time.Time) (core.AgentLogSnapshot, error) {
	f.agentLogHash = hash
	f.agentLog = logs.AgentLog
	f.updateLog = logs.UpdateLog
	if len(f.nodes) == 0 {
		return core.AgentLogSnapshot{}, core.ErrUnauthorized
	}
	return core.AgentLogSnapshot{NodeID: f.nodes[0].ID, AgentLog: logs.AgentLog, UpdateLog: logs.UpdateLog, CapturedAt: now}, nil
}

func (f *fakeRepository) GetAgentLogs(_ context.Context, nodeID string) (core.AgentLogSnapshot, error) {
	return core.AgentLogSnapshot{NodeID: nodeID, AgentLog: f.agentLog, UpdateLog: f.updateLog, CapturedAt: fixedNow}, nil
}

func TestAgentLogUploadAuthenticatesDeviceAndStoresBothLogTails(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{nodes: []core.Node{{ID: "node-1", Name: "office-pc"}}}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	body, err := json.Marshal(map[string]string{
		"agent_log":  "agent log tail",
		"update_log": "update log tail",
	})
	if err != nil {
		t.Fatalf("marshal log upload: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/logs", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer device-secret")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if repo.agentLogHash != hashToken("device-secret") || repo.agentLog != "agent log tail" || repo.updateLog != "update log tail" {
		t.Fatalf("stored logs hash=%q agent=%q update=%q", repo.agentLogHash, repo.agentLog, repo.updateLog)
	}
}

func TestOwnerCanReadLatestAgentLogSnapshot(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	repo.nodes = []core.Node{{ID: "node-1", Name: "office-pc"}}
	repo.agentLog = "agent log tail"
	repo.updateLog = "update log tail"
	response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodGet, "/api/v1/nodes/node-1/logs", nil, &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})

	if response.Code != http.StatusOK {
		t.Fatalf("log read status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"agent_log":"agent log tail"`) || !strings.Contains(response.Body.String(), `"update_log":"update log tail"`) {
		t.Fatalf("log read body = %s", response.Body.String())
	}
}

func (f *fakeRepository) ListNetworkHistory(_ context.Context, nodeID string, since time.Time, bucket time.Duration) ([]core.NetworkHistoryPoint, error) {
	f.historyNodeID = nodeID
	f.historySince = since
	f.historyBucket = bucket
	f.historyCalls++
	return f.history, f.historyErr
}

func (f *fakeRepository) ListNetworkSummary(_ context.Context, since time.Time) ([]core.NetworkSummaryItem, error) {
	f.summarySince = since
	f.summaryCalls++
	return f.summary, f.summaryErr
}

func (f *fakeRepository) CreatePairingRequest(_ context.Context, request core.PairingRequest, _ time.Time) (core.PairingRequest, error) {
	f.pairingCreate = request
	if f.pairingErr != nil {
		return core.PairingRequest{}, f.pairingErr
	}
	if f.pairingResult.ID != "" {
		return f.pairingResult, nil
	}
	return request, nil
}

func (f *fakeRepository) GetPairingRequest(_ context.Context, id, credentialHash string, _ time.Time) (core.PairingRequest, error) {
	f.pairingID = id
	f.pairingHash = credentialHash
	if f.pairingErr != nil {
		return core.PairingRequest{}, f.pairingErr
	}
	return f.pairingResult, nil
}

func (f *fakeRepository) ListPendingPairingRequests(context.Context, time.Time) ([]core.PairingRequest, error) {
	if f.pairingErr != nil {
		return nil, f.pairingErr
	}
	return f.pairings, nil
}

func (f *fakeRepository) ApprovePairingRequest(_ context.Context, id, groupID, remark string, _ time.Time) (core.Node, error) {
	f.pairingID = id
	f.pairingGroup = groupID
	f.pairingRemark = remark
	if f.pairingErr != nil {
		return core.Node{}, f.pairingErr
	}
	return f.pairingNode, nil
}

func (f *fakeRepository) RejectPairingRequest(_ context.Context, id string, _ time.Time) error {
	f.pairingID = id
	return f.pairingErr
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

func TestListNodesExposesCurrentNetworkRates(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	repo.nodes = []core.Node{{
		ID:                         "node-1",
		Name:                       "office-pc",
		NetworkMetricsAvailable:    true,
		NetworkUploadMBPerSecond:   1.25,
		NetworkDownloadMBPerSecond: 8.75,
	}}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	response := requestJSON(
		t,
		router,
		http.MethodGet,
		"/api/v1/nodes",
		nil,
		&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"},
	)

	if response.Code != http.StatusOK {
		t.Fatalf("list nodes status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list nodes response: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("node items = %#v, want one item", payload.Items)
	}
	item := payload.Items[0]
	if item["network_metrics_available"] != true || item["network_upload_mb_s"] != 1.25 || item["network_download_mb_s"] != 8.75 {
		t.Fatalf("network JSON = %#v, want true/1.25/8.75", item)
	}
}

func TestNetworkHistoryAndSummaryRequireAuthentication(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/api/v1/nodes/node-1/network-history?range=24h",
		"/api/v1/network/summary?range=24h",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			response := requestJSON(t, NewRouter(&fakeRepository{}, func() time.Time { return fixedNow }), http.MethodGet, path, nil, nil)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusUnauthorized, response.Body.String())
			}
		})
	}
}

func TestNetworkHistoryAndSummaryRejectInvalidRange(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/api/v1/nodes/node-1/network-history?range=1h",
		"/api/v1/nodes/node-1/network-history",
		"/api/v1/network/summary?range=1h",
		"/api/v1/network/summary",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			repo := authenticatedRepository()
			response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodGet, path, nil,
				&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if repo.historyCalls != 0 || repo.summaryCalls != 0 {
				t.Fatalf("repository calls = history:%d summary:%d, want none", repo.historyCalls, repo.summaryCalls)
			}
		})
	}
}

func TestNetworkHistoryReturnsNotFoundForMissingNode(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	repo.historyErr = core.ErrNotFound
	response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodGet,
		"/api/v1/nodes/missing/network-history?range=24h", nil,
		&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusNotFound, response.Body.String())
	}
}

func TestNetworkHistoryReturnsNonNullEmptyPoints(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodGet,
		"/api/v1/nodes/node-1/network-history?range=24h", nil,
		&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var payload struct {
		NodeID string                     `json:"node_id"`
		Range  string                     `json:"range"`
		Unit   string                     `json:"unit"`
		Points []core.NetworkHistoryPoint `json:"points"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if payload.NodeID != "node-1" || payload.Range != "24h" || payload.Unit != "MB/s" {
		t.Fatalf("history metadata = %#v, want node-1/24h/MB/s", payload)
	}
	if payload.Points == nil || len(payload.Points) != 0 {
		t.Fatalf("history points = %#v, want non-null empty array", payload.Points)
	}
}

func TestNetworkHistoryMapsAllRanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rangeValue string
		since      time.Time
		bucket     time.Duration
	}{
		{rangeValue: "24h", since: fixedNow.Add(-24 * time.Hour), bucket: 5 * time.Minute},
		{rangeValue: "7d", since: fixedNow.Add(-168 * time.Hour), bucket: 30 * time.Minute},
		{rangeValue: "30d", since: fixedNow.Add(-720 * time.Hour), bucket: 2 * time.Hour},
		{rangeValue: "90d", since: fixedNow.Add(-2160 * time.Hour), bucket: 6 * time.Hour},
	}
	for _, test := range tests {
		t.Run(test.rangeValue, func(t *testing.T) {
			t.Parallel()
			repo := authenticatedRepository()
			response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodGet,
				"/api/v1/nodes/node-1/network-history?range="+test.rangeValue, nil,
				&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
			}
			if repo.historyNodeID != "node-1" || !repo.historySince.Equal(test.since) || repo.historyBucket != test.bucket {
				t.Fatalf("history args = node:%q since:%v bucket:%v, want node-1/%v/%v",
					repo.historyNodeID, repo.historySince, repo.historyBucket, test.since, test.bucket)
			}
		})
	}
}

func TestNetworkHistoryReturnsExpectedPointFields(t *testing.T) {
	t.Parallel()

	point := core.NetworkHistoryPoint{
		CapturedAt:                 fixedNow.Add(-5 * time.Minute),
		UploadAverageMBPerSecond:   1.25,
		DownloadAverageMBPerSecond: 8.75,
		UploadPeakMBPerSecond:      2.5,
		DownloadPeakMBPerSecond:    12.5,
	}
	repo := authenticatedRepository()
	repo.history = []core.NetworkHistoryPoint{point}
	response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodGet,
		"/api/v1/nodes/node-1/network-history?range=24h", nil,
		&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var payload struct {
		Points []core.NetworkHistoryPoint `json:"points"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if len(payload.Points) != 1 || payload.Points[0] != point {
		t.Fatalf("history points = %#v, want %#v", payload.Points, []core.NetworkHistoryPoint{point})
	}
}

func TestNetworkSummaryUsesOneRepositoryCall(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	repo.summary = []core.NetworkSummaryItem{{
		NodeID:                     "node-1",
		UploadAverageMBPerSecond:   1.25,
		DownloadAverageMBPerSecond: 8.75,
		UploadPeakMBPerSecond:      2.5,
		DownloadPeakMBPerSecond:    12.5,
	}}
	response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodGet,
		"/api/v1/network/summary?range=24h", nil,
		&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if repo.summaryCalls != 1 || !repo.summarySince.Equal(fixedNow.Add(-24*time.Hour)) {
		t.Fatalf("summary calls = %d since=%v, want one call since %v", repo.summaryCalls, repo.summarySince, fixedNow.Add(-24*time.Hour))
	}
	var payload struct {
		Range string                    `json:"range"`
		Unit  string                    `json:"unit"`
		Items []core.NetworkSummaryItem `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode summary response: %v", err)
	}
	if payload.Range != "24h" || payload.Unit != "MB/s" || len(payload.Items) != 1 || payload.Items[0] != repo.summary[0] {
		t.Fatalf("summary payload = %#v, want range/unit/item", payload)
	}
}

func TestNetworkSummaryReturnsNonNullEmptyItems(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodGet,
		"/api/v1/network/summary?range=7d", nil,
		&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var payload struct {
		Items []core.NetworkSummaryItem `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode summary response: %v", err)
	}
	if payload.Items == nil || len(payload.Items) != 0 {
		t.Fatalf("summary items = %#v, want non-null empty array", payload.Items)
	}
}

func TestNetworkHistoryAndSummaryHideRepositoryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path      string
		configure func(*fakeRepository)
	}{
		{
			path: "/api/v1/nodes/node-1/network-history?range=24h",
			configure: func(repo *fakeRepository) {
				repo.historyErr = errors.New("database details")
			},
		},
		{
			path: "/api/v1/network/summary?range=24h",
			configure: func(repo *fakeRepository) {
				repo.summaryErr = errors.New("database details")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			t.Parallel()
			repo := authenticatedRepository()
			test.configure(repo)
			response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodGet, test.path, nil,
				&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusInternalServerError, response.Body.String())
			}
			var payload struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if payload.Error != "internal server error" {
				t.Fatalf("error = %q, want generic internal error", payload.Error)
			}
		})
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

func TestCreateGroupTrimsNameWithoutSite(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	router := NewRouter(repo, func() time.Time { return fixedNow })
	cookie := &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"}

	groupResponse := requestJSON(t, router, http.MethodPost, "/api/v1/groups", map[string]string{
		"name": "  财务电脑  ",
	}, cookie)
	if groupResponse.Code != http.StatusCreated || len(repo.groups) != 1 {
		t.Fatalf("group status=%d groups=%#v", groupResponse.Code, repo.groups)
	}
	if repo.groups[0].SiteID != "" || repo.groups[0].Name != "财务电脑" {
		t.Fatalf("group = %#v, want a flat 财务电脑 group", repo.groups[0])
	}
}

func TestUpdateNodeRemarkRequiresOwnerAndReturnsTrimmedRemark(t *testing.T) {
	t.Parallel()

	unauthorized := requestJSON(
		t,
		NewRouter(&fakeRepository{}, func() time.Time { return fixedNow }),
		http.MethodPatch,
		"/api/v1/nodes/node-1",
		map[string]string{"remark": "财务电脑"},
		nil,
	)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	repo := authenticatedRepository()
	repo.nodes = []core.Node{{ID: "node-1", Name: "finance-pc"}}
	response := requestJSON(
		t,
		NewRouter(repo, func() time.Time { return fixedNow }),
		http.MethodPatch,
		"/api/v1/nodes/node-1",
		map[string]string{"remark": "  财务电脑  "},
		&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"},
	)
	if response.Code != http.StatusOK || repo.nodeRemarkID != "node-1" || repo.nodeRemark != "财务电脑" {
		t.Fatalf("status=%d id=%q remark=%q body=%s", response.Code, repo.nodeRemarkID, repo.nodeRemark, response.Body.String())
	}
	var payload struct {
		Node core.Node `json:"node"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if payload.Node.Remark != "财务电脑" {
		t.Fatalf("response remark = %q, want trimmed value", payload.Node.Remark)
	}
}

func TestUpdateNodeRemarkRejectsMoreThanFiveHundredCharacters(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	response := requestJSON(
		t,
		NewRouter(repo, func() time.Time { return fixedNow }),
		http.MethodPatch,
		"/api/v1/nodes/node-1",
		map[string]string{"remark": strings.Repeat("备", 501)},
		&http.Cookie{Name: sessionCookieName, Value: "authenticated-session"},
	)
	if response.Code != http.StatusBadRequest || repo.nodeRemarkID != "" {
		t.Fatalf("status=%d updated=%q body=%s", response.Code, repo.nodeRemarkID, response.Body.String())
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

func TestHeartbeatNormalizesNegativeNetworkRates(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{nodes: []core.Node{{ID: "node-1", Name: "office-pc"}}}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	body, err := json.Marshal(core.Heartbeat{
		NetworkMetricsAvailable:    true,
		NetworkUploadMBPerSecond:   -1,
		NetworkDownloadMBPerSecond: -2,
	})
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer device-secret")
	authorizedResponse := httptest.NewRecorder()
	router.ServeHTTP(authorizedResponse, req)

	if authorizedResponse.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, want %d; body=%s", authorizedResponse.Code, http.StatusOK, authorizedResponse.Body.String())
	}
	if repo.heartbeat.NetworkUploadMBPerSecond != 0 || repo.heartbeat.NetworkDownloadMBPerSecond != 0 {
		t.Fatalf("recorded network rates = %v/%v, want 0/0", repo.heartbeat.NetworkUploadMBPerSecond, repo.heartbeat.NetworkDownloadMBPerSecond)
	}
}

func TestAgentHeartbeatAcceptsLegacyNetworkUsagePayload(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{nodes: []core.Node{{ID: "node-1", Name: "office-pc"}}}
	response := postHeartbeatJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), `{"hostname":"office-pc"}`)

	if response.Code != http.StatusOK {
		t.Fatalf("legacy heartbeat status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if repo.heartbeat.NetworkUsageAvailable || repo.heartbeat.NetworkUsageDay != "" {
		t.Fatalf("legacy heartbeat = %#v", repo.heartbeat)
	}
}

func TestAgentHeartbeatNetworkUsageValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "valid", body: `{"network_usage_available":true,"network_usage_day":"2026-08-03","network_today_download_bytes":10}`, want: http.StatusOK},
		{name: "missing day", body: `{"network_usage_available":true}`, want: http.StatusBadRequest},
		{name: "invalid day", body: `{"network_usage_available":true,"network_usage_day":"2026-02-30"}`, want: http.StatusBadRequest},
		{name: "disabled with date", body: `{"network_usage_available":false,"network_usage_day":"2026-08-03"}`, want: http.StatusBadRequest},
		{name: "disabled with counters", body: `{"network_usage_available":false,"network_today_download_bytes":10}`, want: http.StatusBadRequest},
		{name: "negative counter", body: `{"network_usage_available":true,"network_usage_day":"2026-08-03","network_today_download_bytes":-1}`, want: http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeRepository{nodes: []core.Node{{ID: "node-1", Name: "office-pc"}}}
			response := postHeartbeatJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), test.body)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
			if test.want != http.StatusOK && repo.heartbeatHash != "" {
				t.Fatalf("invalid heartbeat reached repository: %#v", repo.heartbeat)
			}
		})
	}
}

func postHeartbeatJSON(t *testing.T, router http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer device-secret")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestNormalizeNetworkRateRejectsNonFiniteValues(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]float64{
		"NaN":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeNetworkRate(input); got != 0 {
				t.Fatalf("normalizeNetworkRate(%v) = %v, want 0", input, got)
			}
		})
	}
}

const testPairingCredential = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestAgentPairingCreatesPendingRequestWithoutReturningCredential(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{pairingResult: core.PairingRequest{
		ID: "pairing-1", State: core.PairingPending, ExpiresAt: fixedNow.Add(10 * time.Minute),
	}}
	response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodPost, "/api/v1/agent/pairings", map[string]string{
		"hostname":           "  finance-pc  ",
		"type":               "windows",
		"agent_version":      "  0.3.2  ",
		"machine_id":         "  machine-1  ",
		"pairing_credential": testPairingCredential,
	}, nil)

	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), testPairingCredential) {
		t.Fatal("pairing credential leaked")
	}
	if repo.pairingCreate.CredentialHash != security.HashToken(testPairingCredential) {
		t.Fatal("credential was not hashed")
	}
	if repo.pairingCreate.Hostname != "finance-pc" || repo.pairingCreate.MachineID != "machine-1" || repo.pairingCreate.AgentVersion != "0.3.2" {
		t.Fatalf("pairing create=%#v, want trimmed agent fields", repo.pairingCreate)
	}
}

func TestAgentPairingRejectsInvalidCredential(t *testing.T) {
	t.Parallel()

	response := requestJSON(t, NewRouter(&fakeRepository{}, func() time.Time { return fixedNow }), http.MethodPost, "/api/v1/agent/pairings", map[string]string{
		"hostname": "finance-pc", "type": "windows", "agent_version": "0.3.2", "machine_id": "machine-1", "pairing_credential": "not-a-32-byte-base64url-credential",
	}, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAgentPairingRetriesReturnExistingPairingID(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{pairingResult: core.PairingRequest{
		ID: "pairing-1", State: core.PairingPending, ExpiresAt: fixedNow.Add(10 * time.Minute),
	}}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	for attempt := 0; attempt < 2; attempt++ {
		response := requestJSON(t, router, http.MethodPost, "/api/v1/agent/pairings", map[string]string{
			"hostname": "finance-pc", "type": "windows", "agent_version": "0.3.2", "machine_id": "machine-1", "pairing_credential": testPairingCredential,
		}, nil)
		var payload core.PairingPollResult
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode pairing response: %v", err)
		}
		if response.Code != http.StatusCreated || payload.ID != "pairing-1" {
			t.Fatalf("attempt=%d status=%d payload=%#v", attempt+1, response.Code, payload)
		}
	}
}

func TestAgentPairingPollRequiresBearerCredential(t *testing.T) {
	t.Parallel()

	router := NewRouter(&fakeRepository{}, func() time.Time { return fixedNow })
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/pairings/pairing-1", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAgentPairingPollReturnsApprovedNodeWithoutCredential(t *testing.T) {
	t.Parallel()

	repo := &fakeRepository{pairingResult: core.PairingRequest{
		ID: "pairing-1", State: core.PairingApproved, ExpiresAt: fixedNow.Add(10 * time.Minute),
		ExistingNode: &core.Node{ID: "node-1", Name: "finance-pc"},
	}}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/pairings/pairing-1", nil)
	request.Header.Set("Authorization", "Bearer "+testPairingCredential)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.pairingHash != security.HashToken(testPairingCredential) {
		t.Fatal("polling credential was not hashed")
	}
	if strings.Contains(response.Body.String(), testPairingCredential) {
		t.Fatal("pairing credential leaked")
	}
	var payload core.PairingPollResult
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode poll response: %v", err)
	}
	if payload.Node == nil || payload.Node.ID != "node-1" {
		t.Fatalf("poll payload=%#v, want approved node", payload)
	}
}

func TestAgentPairingPollRejectedAndExpiredHideNode(t *testing.T) {
	t.Parallel()

	for _, state := range []core.PairingState{core.PairingRejected, core.PairingExpired} {
		t.Run(string(state), func(t *testing.T) {
			repo := &fakeRepository{pairingResult: core.PairingRequest{
				ID: "pairing-1", State: state, ExpiresAt: fixedNow.Add(10 * time.Minute),
				ExistingNode: &core.Node{ID: "node-1"},
			}}
			router := NewRouter(repo, func() time.Time { return fixedNow })
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/pairings/pairing-1", nil)
			request.Header.Set("Authorization", "Bearer "+testPairingCredential)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusGone {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var payload core.PairingPollResult
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode rejected poll response: %v", err)
			}
			if payload.Node != nil || strings.Contains(response.Body.String(), testPairingCredential) {
				t.Fatalf("poll payload=%#v body=%s, must not leak node or credential", payload, response.Body.String())
			}
		})
	}
}

func TestPairingOwnerAPIsRequireSession(t *testing.T) {
	t.Parallel()

	router := NewRouter(&fakeRepository{}, func() time.Time { return fixedNow })
	for _, test := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/v1/pairings", nil},
		{http.MethodPost, "/api/v1/pairings/pairing-1/approve", map[string]string{"group_id": "group-1"}},
		{http.MethodPost, "/api/v1/pairings/pairing-1/reject", nil},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := requestJSON(t, router, test.method, test.path, test.body, nil)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPairingApprovalRequiresOwnerAndGroup(t *testing.T) {
	t.Parallel()

	response := requestJSON(t, NewRouter(&fakeRepository{}, func() time.Time { return fixedNow }), http.MethodPost, "/api/v1/pairings/pairing-1/approve", map[string]string{"group_id": ""}, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}

	repo := authenticatedRepository()
	response = requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodPost, "/api/v1/pairings/pairing-1/approve", map[string]string{"group_id": "  "}, &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("authenticated empty group status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPairingApprovalMapsExpiredRequestToGone(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	repo.pairingErr = core.ErrPairingExpired
	response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodPost, "/api/v1/pairings/pairing-1/approve", map[string]string{"group_id": "group-1"}, &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
	if response.Code != http.StatusGone {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPairingOwnerAPIsListApproveAndReject(t *testing.T) {
	t.Parallel()

	repo := authenticatedRepository()
	repo.pairingNode = core.Node{ID: "node-1", GroupID: "group-1", Name: "finance-pc"}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	cookie := &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"}

	list := requestJSON(t, router, http.MethodGet, "/api/v1/pairings", nil, cookie)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "\"items\":[]") {
		t.Fatalf("list status=%d body=%s, want non-null empty items", list.Code, list.Body.String())
	}
	approve := requestJSON(t, router, http.MethodPost, "/api/v1/pairings/pairing-1/approve", map[string]string{
		"group_id": "group-1",
		"remark":   " 15 楼财务电脑 ",
	}, cookie)
	if approve.Code != http.StatusOK || repo.pairingID != "pairing-1" || repo.pairingGroup != "group-1" || repo.pairingRemark != "15 楼财务电脑" {
		t.Fatalf("approve status=%d id=%q group=%q remark=%q body=%s", approve.Code, repo.pairingID, repo.pairingGroup, repo.pairingRemark, approve.Body.String())
	}
	var payload struct {
		Pairing struct {
			Remark string `json:"remark"`
		} `json:"pairing"`
	}
	if err := json.Unmarshal(approve.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode approval response: %v", err)
	}
	if payload.Pairing.Remark != "15 楼财务电脑" {
		t.Fatalf("approval remark = %q, want trimmed value", payload.Pairing.Remark)
	}
	reject := requestJSON(t, router, http.MethodPost, "/api/v1/pairings/pairing-1/reject", nil, cookie)
	if reject.Code != http.StatusNoContent || repo.pairingID != "pairing-1" {
		t.Fatalf("reject status=%d id=%q body=%s", reject.Code, repo.pairingID, reject.Body.String())
	}
}

func TestAgentPairingRateLimitsSourceAndMachineAtEleventhRequest(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		remoteAddr func(int) string
		machineID  func(int) string
	}{
		{
			name:       "source",
			remoteAddr: func(int) string { return "198.51.100.10:5000" },
			machineID:  func(index int) string { return "machine-" + string(rune('a'+index)) },
		},
		{
			name:       "machine",
			remoteAddr: func(index int) string { return fmt.Sprintf("198.51.100.%d:5000", index) },
			machineID:  func(int) string { return "machine-1" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouter(&fakeRepository{pairingResult: core.PairingRequest{ID: "pairing-1", State: core.PairingPending, ExpiresAt: fixedNow.Add(10 * time.Minute)}}, func() time.Time { return fixedNow })
			for requestNumber := 1; requestNumber <= 11; requestNumber++ {
				response := postPairingRequest(t, router, test.remoteAddr(requestNumber), test.machineID(requestNumber))
				if requestNumber <= 10 && response.Code != http.StatusCreated {
					t.Fatalf("request=%d status=%d body=%s", requestNumber, response.Code, response.Body.String())
				}
				if requestNumber == 11 {
					var payload struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
						t.Fatalf("decode rate-limit response: %v", err)
					}
					if response.Code != http.StatusTooManyRequests || payload.Error != "pairing request rate limit exceeded" || strings.Contains(response.Body.String(), testPairingCredential) {
						t.Fatalf("request=%d status=%d body=%s", requestNumber, response.Code, response.Body.String())
					}
				}
			}
		})
	}
}

func TestPairingLimiterRejectsNewKeysAtCapacity(t *testing.T) {
	t.Parallel()

	const expectedMaximumWindows = 4096
	limiter := newPairingLimiter()
	fillPairingLimiter(t, limiter, fixedNow, expectedMaximumWindows/2)
	if got := len(limiter.windows); got != expectedMaximumWindows {
		t.Fatalf("windows=%d, want %d", got, expectedMaximumWindows)
	}
	if limiter.Allow("new-source", "new-machine", fixedNow) {
		t.Fatal("new keys were allowed after the limiter reached capacity")
	}
	if got := len(limiter.windows); got != expectedMaximumWindows {
		t.Fatalf("windows=%d after rejection, want %d", got, expectedMaximumWindows)
	}
}

func TestPairingLimiterEvictsExpiredWindowsBeforeAdmittingNewKeys(t *testing.T) {
	t.Parallel()

	const expectedMaximumWindows = 4096
	limiter := newPairingLimiter()
	fillPairingLimiter(t, limiter, fixedNow, expectedMaximumWindows/2)
	if !limiter.Allow("new-source", "new-machine", fixedNow.Add(time.Minute)) {
		t.Fatal("new keys were rejected after all prior windows expired")
	}
	if got := len(limiter.windows); got != 2 {
		t.Fatalf("windows=%d after expiry, want 2 active keys", got)
	}
}

func TestPairingLimiterRestoresQuotaAfterWindowExpires(t *testing.T) {
	t.Parallel()

	limiter := newPairingLimiter()
	for requestNumber := 1; requestNumber <= 10; requestNumber++ {
		if !limiter.Allow("198.51.100.10", "machine-1", fixedNow) {
			t.Fatalf("request=%d was rejected before the quota", requestNumber)
		}
	}
	if limiter.Allow("198.51.100.10", "machine-1", fixedNow) {
		t.Fatal("eleventh request was allowed before window expiry")
	}
	if !limiter.Allow("198.51.100.10", "machine-1", fixedNow.Add(time.Minute)) {
		t.Fatal("quota did not recover after window expiry")
	}
}

func TestAgentPairingRateLimitWindowExpiryRestoresQuota(t *testing.T) {
	t.Parallel()

	now := fixedNow
	router := NewRouter(&fakeRepository{pairingResult: core.PairingRequest{ID: "pairing-1", State: core.PairingPending, ExpiresAt: fixedNow.Add(10 * time.Minute)}}, func() time.Time { return now })
	for requestNumber := 1; requestNumber <= 10; requestNumber++ {
		response := postPairingRequest(t, router, "198.51.100.10:5000", "machine-1")
		if response.Code != http.StatusCreated {
			t.Fatalf("request=%d status=%d body=%s", requestNumber, response.Code, response.Body.String())
		}
	}
	if response := postPairingRequest(t, router, "198.51.100.10:5000", "machine-1"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("eleventh status=%d body=%s", response.Code, response.Body.String())
	}
	now = now.Add(time.Minute)
	if response := postPairingRequest(t, router, "198.51.100.10:5000", "machine-1"); response.Code != http.StatusCreated {
		t.Fatalf("post-expiry status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAgentPairingRateLimitIgnoresForwardedFor(t *testing.T) {
	t.Parallel()

	router := NewRouter(&fakeRepository{pairingResult: core.PairingRequest{ID: "pairing-1", State: core.PairingPending, ExpiresAt: fixedNow.Add(10 * time.Minute)}}, func() time.Time { return fixedNow })
	for requestNumber := 1; requestNumber <= 11; requestNumber++ {
		response := postPairingRequestWithForwardedFor(t, router, "198.51.100.10:5000", fmt.Sprintf("machine-%d", requestNumber), fmt.Sprintf("203.0.113.%d", requestNumber))
		if requestNumber <= 10 && response.Code != http.StatusCreated {
			t.Fatalf("request=%d status=%d body=%s", requestNumber, response.Code, response.Body.String())
		}
		if requestNumber == 11 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("eleventh status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func fillPairingLimiter(t *testing.T, limiter *pairingLimiter, now time.Time, pairs int) {
	t.Helper()
	for index := 0; index < pairs; index++ {
		if !limiter.Allow(fmt.Sprintf("source-%d", index), fmt.Sprintf("machine-%d", index), now) {
			t.Fatalf("pair=%d was rejected before the expected capacity", index)
		}
	}
}

type denyPairingLimiter struct{}

func (denyPairingLimiter) Allow(string, string, time.Time) bool {
	return false
}

func TestRouterOptionsUsesInjectedPairingLimiter(t *testing.T) {
	t.Parallel()

	router := NewRouterWithOptions(&fakeRepository{}, RouterOptions{
		Now:            func() time.Time { return fixedNow },
		SecureCookies:  true,
		PairingLimiter: denyPairingLimiter{},
	})
	response := postPairingRequest(t, router, "198.51.100.10:5000", "machine-1")
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "pairing request rate limit exceeded") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func postPairingRequest(t *testing.T, handler http.Handler, remoteAddr, machineID string) *httptest.ResponseRecorder {
	return postPairingRequestWithForwardedFor(t, handler, remoteAddr, machineID, "")
}

func postPairingRequestWithForwardedFor(t *testing.T, handler http.Handler, remoteAddr, machineID, forwardedFor string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"hostname": "finance-pc", "type": "windows", "agent_version": "0.3.2", "machine_id": machineID, "pairing_credential": testPairingCredential,
	})
	if err != nil {
		t.Fatalf("marshal pairing request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/pairings", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if forwardedFor != "" {
		request.Header.Set("X-Forwarded-For", forwardedFor)
	}
	request.RemoteAddr = remoteAddr
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func authenticatedRepository() *fakeRepository {
	plainSession := "authenticated-session"
	return &fakeRepository{
		sessions: map[string]core.Owner{
			hashToken(plainSession): {ID: "owner-1", Username: "jarvis"},
		},
	}
}
