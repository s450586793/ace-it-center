package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aceitcenter.local/platform/internal/core"
)

type commandFakeRepository struct {
	*fakeRepository
	createdTask         core.CommandTask
	createdNodeIDs      []string
	createdDetail       core.CommandTaskDetail
	commands            []core.CommandTask
	commandDetail       core.CommandTaskDetail
	commandErr          error
	retriedTask         core.CommandTask
	retriedSourceID     string
	claim               core.CommandClaim
	claimFound          bool
	claimCredential     string
	claimLeaseHash      string
	claimCalls          int
	startedCredential   string
	startedExecution    string
	startedLeaseHash    string
	completedCredential string
	completedLeaseHash  string
	completion          core.CommandCompletion
}

func newAuthenticatedCommandRepository() *commandFakeRepository {
	return &commandFakeRepository{fakeRepository: authenticatedRepository()}
}

func (f *commandFakeRepository) CreateCommand(_ context.Context, task core.CommandTask, nodeIDs []string) (core.CommandTaskDetail, error) {
	f.createdTask = task
	f.createdNodeIDs = append([]string(nil), nodeIDs...)
	if f.commandErr != nil {
		return core.CommandTaskDetail{}, f.commandErr
	}
	if f.createdDetail.Task.ID != "" {
		return f.createdDetail, nil
	}
	task.TargetCount = len(nodeIDs)
	task.Counts.Queued = len(nodeIDs)
	return core.CommandTaskDetail{Task: task}, nil
}

func (f *commandFakeRepository) ListCommands(context.Context, int) ([]core.CommandTask, error) {
	return f.commands, f.commandErr
}

func (f *commandFakeRepository) GetCommand(context.Context, string) (core.CommandTaskDetail, error) {
	return f.commandDetail, f.commandErr
}

func (f *commandFakeRepository) RetryCommand(_ context.Context, task core.CommandTask, sourceID string) (core.CommandTaskDetail, error) {
	f.retriedTask = task
	f.retriedSourceID = sourceID
	return f.createdDetail, f.commandErr
}

func (f *commandFakeRepository) ClaimCommand(_ context.Context, credentialHash, leaseHash string, _ time.Time, _ time.Duration) (core.CommandClaim, bool, error) {
	f.claimCredential = credentialHash
	f.claimLeaseHash = leaseHash
	f.claimCalls++
	return f.claim, f.claimFound, f.commandErr
}

func (f *commandFakeRepository) StartCommand(_ context.Context, credentialHash, executionID, leaseHash string, _ time.Time) error {
	f.startedCredential = credentialHash
	f.startedExecution = executionID
	f.startedLeaseHash = leaseHash
	return f.commandErr
}

func (f *commandFakeRepository) CompleteCommand(_ context.Context, credentialHash, leaseHash string, completion core.CommandCompletion, _ time.Time) error {
	f.completedCredential = credentialHash
	f.completedLeaseHash = leaseHash
	f.completion = completion
	return f.commandErr
}

func TestOwnerCreatesCommandForSelectedWindowsNodes(t *testing.T) {
	t.Parallel()

	repo := newAuthenticatedCommandRepository()
	response := requestJSON(t, NewRouter(repo, func() time.Time { return fixedNow }), http.MethodPost, "/api/v1/commands", map[string]any{
		"node_ids":        []string{"node-1", "node-2"},
		"shell":           "powershell",
		"command":         "Get-ComputerInfo",
		"timeout_seconds": 300,
	}, &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})

	if response.Code != http.StatusCreated {
		t.Fatalf("create command status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.createdTask.ID == "" || repo.createdTask.CreatedBy != "owner-1" || repo.createdTask.Shell != core.CommandShellPowerShell || repo.createdTask.Command != "Get-ComputerInfo" {
		t.Fatalf("created task = %#v", repo.createdTask)
	}
	if strings.Join(repo.createdNodeIDs, ",") != "node-1,node-2" {
		t.Fatalf("created node IDs = %v", repo.createdNodeIDs)
	}
}

func TestOwnerCommandAPIRejectsInvalidRequestAndRequiresSession(t *testing.T) {
	t.Parallel()

	repo := newAuthenticatedCommandRepository()
	router := NewRouter(repo, func() time.Time { return fixedNow })
	invalid := requestJSON(t, router, http.MethodPost, "/api/v1/commands", map[string]any{
		"node_ids": []string{"node-1"}, "shell": "shell", "command": "hostname", "timeout_seconds": 300,
	}, &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid command status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	unauthorized := requestJSON(t, router, http.MethodGet, "/api/v1/commands", nil, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized command status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
}

func TestOwnerListsReadsAndRetriesCommands(t *testing.T) {
	t.Parallel()

	repo := newAuthenticatedCommandRepository()
	repo.commands = []core.CommandTask{{ID: "task-1", Shell: core.CommandShellCMD, TargetCount: 1}}
	repo.commandDetail = core.CommandTaskDetail{
		Task:       core.CommandTask{ID: "task-1", Shell: core.CommandShellCMD},
		Executions: []core.CommandExecution{{ID: "execution-1", NodeID: "node-1", Status: core.CommandFailed}},
	}
	repo.createdDetail = core.CommandTaskDetail{Task: core.CommandTask{ID: "task-2", RetriedFromID: stringPointer("task-1")}}
	router := NewRouter(repo, func() time.Time { return fixedNow })
	cookie := &http.Cookie{Name: sessionCookieName, Value: "authenticated-session"}

	listed := requestJSON(t, router, http.MethodGet, "/api/v1/commands", nil, cookie)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":"task-1"`) {
		t.Fatalf("list commands status=%d body=%s", listed.Code, listed.Body.String())
	}
	detail := requestJSON(t, router, http.MethodGet, "/api/v1/commands/task-1", nil, cookie)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"execution-1"`) {
		t.Fatalf("command detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	retried := requestJSON(t, router, http.MethodPost, "/api/v1/commands/task-1/retry", map[string]any{}, cookie)
	if retried.Code != http.StatusCreated || repo.retriedSourceID != "task-1" || repo.retriedTask.ID == "" || repo.retriedTask.CreatedBy != "owner-1" {
		t.Fatalf("retry status=%d task=%#v source=%q body=%s", retried.Code, repo.retriedTask, repo.retriedSourceID, retried.Body.String())
	}
}

func TestAgentCommandClaimReturnsNoContentAfterBoundedPoll(t *testing.T) {
	t.Parallel()

	repo := &commandFakeRepository{fakeRepository: &fakeRepository{}}
	router := NewRouterWithOptions(repo, RouterOptions{
		Now: fixedClock, CommandPollDuration: 3 * time.Millisecond, CommandPollInterval: time.Millisecond,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/commands/claim", nil)
	request.Header.Set("Authorization", "Bearer device-secret")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("claim no work status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.claimCalls < 1 || repo.claimCredential != hashToken("device-secret") {
		t.Fatalf("claim calls=%d credential=%q", repo.claimCalls, repo.claimCredential)
	}
}

func TestAgentCommandClaimReturnsPlainLeaseOnlyToAgent(t *testing.T) {
	t.Parallel()

	repo := &commandFakeRepository{
		fakeRepository: &fakeRepository{}, claimFound: true,
		claim: core.CommandClaim{ExecutionID: "execution-1", TaskID: "task-1", Shell: core.CommandShellCMD, Command: "hostname", TimeoutSeconds: 300, LeaseExpiresAt: fixedNow.Add(35 * time.Minute)},
	}
	router := NewRouterWithOptions(repo, RouterOptions{Now: fixedClock})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/commands/claim", nil)
	request.Header.Set("Authorization", "Bearer device-secret")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", response.Code, response.Body.String())
	}
	var claim core.CommandClaim
	if err := json.Unmarshal(response.Body.Bytes(), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if claim.LeaseToken == "" || repo.claimLeaseHash != hashToken(claim.LeaseToken) || repo.claimLeaseHash == claim.LeaseToken {
		t.Fatalf("plain lease=%q stored hash=%q", claim.LeaseToken, repo.claimLeaseHash)
	}
}

func TestAgentStartsAndCompletesCommandWithLease(t *testing.T) {
	t.Parallel()

	repo := &commandFakeRepository{fakeRepository: &fakeRepository{}}
	router := NewRouterWithOptions(repo, RouterOptions{Now: fixedClock})
	start := requestAgentJSON(t, router, "/api/v1/agent/commands/execution-1/start", map[string]any{"lease_token": "lease-secret"})
	if start.Code != http.StatusOK || repo.startedCredential != hashToken("device-secret") || repo.startedExecution != "execution-1" || repo.startedLeaseHash != hashToken("lease-secret") {
		t.Fatalf("start status=%d credential=%q execution=%q lease=%q body=%s", start.Code, repo.startedCredential, repo.startedExecution, repo.startedLeaseHash, start.Body.String())
	}
	exitCode := 0
	complete := requestAgentJSON(t, router, "/api/v1/agent/commands/execution-1/complete", map[string]any{
		"lease_token": "lease-secret", "status": "succeeded", "exit_code": exitCode,
		"output": "office-pc", "output_truncated": false, "error_message": "", "duration_ms": 25,
	})
	if complete.Code != http.StatusOK || repo.completedCredential != hashToken("device-secret") || repo.completedLeaseHash != hashToken("lease-secret") {
		t.Fatalf("complete status=%d credential=%q lease=%q body=%s", complete.Code, repo.completedCredential, repo.completedLeaseHash, complete.Body.String())
	}
	if repo.completion.ExecutionID != "execution-1" || repo.completion.Status != core.CommandSucceeded || repo.completion.ExitCode == nil || *repo.completion.ExitCode != 0 || repo.completion.Output != "office-pc" {
		t.Fatalf("completion = %#v", repo.completion)
	}
}

func requestAgentJSON(t *testing.T, router http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal agent request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer device-secret")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func fixedClock() time.Time { return fixedNow }

func stringPointer(value string) *string { return &value }
