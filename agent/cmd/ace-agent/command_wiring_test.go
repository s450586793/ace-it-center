package main

import (
	"context"
	"io"
	"testing"
	"time"

	agentclient "aceitcenter.local/platform/agent/internal/agent"
	agentcommand "aceitcenter.local/platform/agent/internal/command"
	"aceitcenter.local/platform/internal/core"
)

type wiringCommandClient struct {
	cancel     context.CancelFunc
	claimCalls int
	started    bool
	completion core.CommandCompletion
}

func (f *wiringCommandClient) ClaimCommand(ctx context.Context, _ string) (core.CommandClaim, bool, error) {
	if f.claimCalls > 0 {
		<-ctx.Done()
		return core.CommandClaim{}, false, ctx.Err()
	}
	f.claimCalls++
	return core.CommandClaim{
		ExecutionID: "execution-1", TaskID: "task-1", Shell: core.CommandShellCMD,
		Command: "hostname", TimeoutSeconds: 300, LeaseToken: "lease-secret",
		LeaseExpiresAt: time.Now().UTC().Add(35 * time.Minute),
	}, true, nil
}

func (f *wiringCommandClient) StartCommand(context.Context, string, string, string) error {
	f.started = true
	return nil
}

func (f *wiringCommandClient) CompleteCommand(_ context.Context, _ string, completion core.CommandCompletion) error {
	f.completion = completion
	f.cancel()
	return nil
}

type wiringRunner struct{}

func (wiringRunner) Run(_ context.Context, invocation agentcommand.Invocation, output io.Writer) (int, error) {
	if invocation.Program != "cmd.exe" {
		return -1, nil
	}
	_, _ = io.WriteString(output, "office-pc\n")
	return 0, nil
}

func TestNewServiceCommandLoopRunsClaimThroughPlatformExecutor(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	client := &wiringCommandClient{cancel: cancel}
	loop := newServiceCommandLoop(client, wiringRunner{}, true, nil)
	if loop == nil {
		t.Fatal("supported platform did not create a command loop")
	}
	if err := loop(ctx, agentclient.Config{Credential: "device-secret"}); err != nil {
		t.Fatalf("command loop returned error: %v", err)
	}
	if !client.started || client.completion.Status != core.CommandSucceeded || client.completion.Output != "office-pc\n" {
		t.Fatalf("started=%v completion=%#v", client.started, client.completion)
	}
}

func TestNewServiceCommandLoopIsDisabledOnUnsupportedPlatform(t *testing.T) {
	t.Parallel()

	if loop := newServiceCommandLoop(&wiringCommandClient{}, nil, false, nil); loop != nil {
		t.Fatal("unsupported platform created a command loop")
	}
}
