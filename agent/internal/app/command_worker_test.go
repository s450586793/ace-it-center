package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	agentclient "aceitcenter.local/platform/agent/internal/agent"
	"aceitcenter.local/platform/internal/core"
)

type commandClaimResult struct {
	claim core.CommandClaim
	found bool
	err   error
}

type fakeCommandClient struct {
	claims           []commandClaimResult
	startErrors      []error
	completionErrors []error
	calls            *[]string
	cancel           context.CancelFunc
	claimCount       int
	completions      []core.CommandCompletion
}

func (f *fakeCommandClient) ClaimCommand(ctx context.Context, credential string) (core.CommandClaim, bool, error) {
	*f.calls = append(*f.calls, "claim")
	if credential != "device-secret" {
		return core.CommandClaim{}, false, errors.New("unexpected credential")
	}
	if f.claimCount >= len(f.claims) {
		<-ctx.Done()
		return core.CommandClaim{}, false, ctx.Err()
	}
	result := f.claims[f.claimCount]
	f.claimCount++
	return result.claim, result.found, result.err
}

func (f *fakeCommandClient) StartCommand(_ context.Context, credential, executionID, leaseToken string) error {
	*f.calls = append(*f.calls, "start")
	if credential != "device-secret" || executionID == "" || leaseToken == "" {
		return errors.New("invalid start request")
	}
	if len(f.startErrors) == 0 {
		return nil
	}
	err := f.startErrors[0]
	f.startErrors = f.startErrors[1:]
	if errors.Is(err, agentclient.ErrCommandLeaseRejected) && f.cancel != nil {
		f.cancel()
	}
	return err
}

func (f *fakeCommandClient) CompleteCommand(_ context.Context, credential string, completion core.CommandCompletion) error {
	*f.calls = append(*f.calls, "complete")
	if credential != "device-secret" {
		return errors.New("unexpected credential")
	}
	f.completions = append(f.completions, completion)
	if len(f.completionErrors) > 0 {
		err := f.completionErrors[0]
		f.completionErrors = f.completionErrors[1:]
		return err
	}
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

type fakeCommandExecutor struct {
	calls  *[]string
	result core.CommandCompletion
	count  int
}

func (f *fakeCommandExecutor) Execute(_ context.Context, claim core.CommandClaim) core.CommandCompletion {
	*f.calls = append(*f.calls, "execute")
	if claim.ExecutionID == "" {
		panic("executor received an empty execution")
	}
	f.count++
	return f.result
}

func TestCommandWorkerClaimsStartsExecutesAndCompletesInOrder(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make([]string, 0, 4)
	claim := validCommandClaim()
	client := &fakeCommandClient{
		claims: []commandClaimResult{{claim: claim, found: true}}, calls: &calls, cancel: cancel,
	}
	exitCode := 0
	executor := &fakeCommandExecutor{calls: &calls, result: core.CommandCompletion{
		Status: core.CommandSucceeded, ExitCode: &exitCode, Output: "office-pc", DurationMS: 25,
	}}
	worker := NewCommandWorker(CommandWorkerOptions{Client: client, Executor: executor})

	if err := worker.Run(ctx, validConfig()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	wantCalls := []string{"claim", "start", "execute", "complete"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls=%v want=%v", calls, wantCalls)
	}
	if len(client.completions) != 1 || client.completions[0].ExecutionID != claim.ExecutionID || client.completions[0].LeaseToken != claim.LeaseToken {
		t.Fatalf("completion = %#v", client.completions)
	}
}

func TestCommandWorkerDoesNotExecuteRejectedLease(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	calls := make([]string, 0, 2)
	client := &fakeCommandClient{
		claims:      []commandClaimResult{{claim: validCommandClaim(), found: true}},
		startErrors: []error{agentclient.ErrCommandLeaseRejected}, calls: &calls, cancel: cancel,
	}
	executor := &fakeCommandExecutor{calls: &calls}
	worker := NewCommandWorker(CommandWorkerOptions{Client: client, Executor: executor})

	if err := worker.Run(ctx, validConfig()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if executor.count != 0 || !reflect.DeepEqual(calls, []string{"claim", "start"}) {
		t.Fatalf("executor count=%d calls=%v", executor.count, calls)
	}
}

func TestCommandWorkerBacksOffAfterClaimErrorAndContinues(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make([]string, 0, 5)
	var waits []time.Duration
	client := &fakeCommandClient{
		claims: []commandClaimResult{
			{err: errors.New("offline")},
			{claim: validCommandClaim(), found: true},
		},
		calls: &calls, cancel: cancel,
	}
	executor := &fakeCommandExecutor{calls: &calls, result: core.CommandCompletion{Status: core.CommandSucceeded}}
	worker := NewCommandWorker(CommandWorkerOptions{
		Client: client, Executor: executor,
		Wait: func(_ context.Context, duration time.Duration) bool {
			waits = append(waits, duration)
			return true
		},
	})

	if err := worker.Run(ctx, validConfig()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(waits) != 1 || waits[0] != 5*time.Second {
		t.Fatalf("waits = %v", waits)
	}
	if executor.count != 1 {
		t.Fatalf("executor count=%d", executor.count)
	}
}

func TestCommandWorkerRetriesCompletionBeforeLeaseExpires(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make([]string, 0, 5)
	var waits []time.Duration
	client := &fakeCommandClient{
		claims:           []commandClaimResult{{claim: validCommandClaim(), found: true}},
		completionErrors: []error{errors.New("offline")}, calls: &calls, cancel: cancel,
	}
	executor := &fakeCommandExecutor{calls: &calls, result: core.CommandCompletion{Status: core.CommandSucceeded}}
	worker := NewCommandWorker(CommandWorkerOptions{
		Client: client, Executor: executor,
		Wait: func(_ context.Context, duration time.Duration) bool {
			waits = append(waits, duration)
			return true
		},
	})

	if err := worker.Run(ctx, validConfig()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !reflect.DeepEqual(waits, []time.Duration{time.Second}) {
		t.Fatalf("waits=%v", waits)
	}
	if len(client.completions) != 2 {
		t.Fatalf("completion attempts=%d", len(client.completions))
	}
}

func validCommandClaim() core.CommandClaim {
	return core.CommandClaim{
		ExecutionID: "execution-1", TaskID: "task-1", Shell: core.CommandShellPowerShell,
		Command: "hostname", TimeoutSeconds: 300, LeaseToken: "lease-secret",
		LeaseExpiresAt: time.Now().UTC().Add(35 * time.Minute),
	}
}
