package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentclient "aceitcenter.local/platform/agent/internal/agent"
)

func TestWorkerKeepsHeartbeatRunningWhenCommandLoopFails(t *testing.T) {
	t.Parallel()

	client := &fakeHeartbeatClient{}
	var states []State
	var commandErrors []string
	commandCalls := 0
	worker := NewWorker(Dependencies{
		Client:  client,
		Collect: fakeCollect,
		Version: "0.4.0",
		StatusSink: func(snapshot StatusSnapshot) {
			states = append(states, snapshot.State)
		},
		CommandLoop: func(context.Context, agentclient.Config) error {
			commandCalls++
			return errors.New("command channel unavailable credential=device-secret")
		},
		CommandErrorSink: func(message string) {
			commandErrors = append(commandErrors, message)
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	if err := worker.Run(ctx, validConfig(), time.Millisecond); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if commandCalls != 1 {
		t.Fatalf("command loop calls=%d, want 1", commandCalls)
	}
	if client.Calls() < 2 {
		t.Fatalf("heartbeat calls=%d, want at least 2", client.Calls())
	}
	for _, state := range states {
		if state == StateError {
			t.Fatalf("command loop failure changed heartbeat state: %v", states)
		}
	}
	if len(commandErrors) != 1 || strings.Contains(commandErrors[0], "device-secret") {
		t.Fatalf("command errors=%q", commandErrors)
	}
}
