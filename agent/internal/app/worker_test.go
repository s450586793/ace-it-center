package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentclient "aceitcenter.local/platform/agent/internal/agent"
	"aceitcenter.local/platform/internal/core"
)

func TestWorkerKeepsRunningAfterHeartbeatFailure(t *testing.T) {
	client := &fakeHeartbeatClient{errors: []error{errors.New("offline token=device-secret"), nil}}
	var snapshots []StatusSnapshot
	worker := NewWorker(Dependencies{
		Client:  client,
		Collect: fakeCollect,
		Version: "0.2.0",
		StatusSink: func(snapshot StatusSnapshot) {
			snapshots = append(snapshots, snapshot)
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := worker.Run(ctx, validConfig(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if calls := client.Calls(); calls < 2 {
		t.Fatalf("heartbeat calls=%d, want at least 2", calls)
	}
	if len(snapshots) < 3 {
		t.Fatalf("snapshots=%d, want startup, failure, and success", len(snapshots))
	}
	if snapshots[1].State != StateError || snapshots[1].Error == "" {
		t.Fatalf("failure snapshot = %#v", snapshots[1])
	}
	if snapshots[1].Error == "offline token=device-secret" {
		t.Fatalf("error was not sanitized: %q", snapshots[1].Error)
	}
	if snapshots[len(snapshots)-2].State != StateOnline || snapshots[len(snapshots)-2].LastHeartbeat.IsZero() {
		t.Fatalf("success snapshot = %#v", snapshots[len(snapshots)-2])
	}
}

func TestWorkerSendsAnImmediateHeartbeatAndPublishesConfig(t *testing.T) {
	client := &fakeHeartbeatClient{}
	status := make(chan StatusSnapshot, 2)
	worker := NewWorker(Dependencies{
		Client:  client,
		Collect: fakeCollect,
		Version: "0.2.0",
		StatusSink: func(snapshot StatusSnapshot) {
			status <- snapshot
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx, validConfig(), time.Hour) }()

	select {
	case snapshot := <-status:
		if snapshot.State != StateStarting || snapshot.NodeID != "node-1" || snapshot.ServerURL != "https://it.example.test" || snapshot.Version != "0.2.0" {
			t.Fatalf("starting snapshot = %#v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not publish starting status")
	}
	select {
	case snapshot := <-status:
		if snapshot.State != StateOnline || snapshot.LastHeartbeat.IsZero() {
			t.Fatalf("online snapshot = %#v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not send an immediate heartbeat")
	}
	if calls := client.Calls(); calls != 1 {
		t.Fatalf("heartbeat calls=%d, want 1", calls)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error after cancellation: %v", err)
	}
}

func TestWorkerUsesOneCollectorForConsecutiveHeartbeats(t *testing.T) {
	client := &recordingHeartbeatClient{}
	collectCalls := 0
	worker := NewWorker(Dependencies{
		Client: client,
		Collect: func(string) (core.EnrollRequest, core.Heartbeat, error) {
			collectCalls++
			return core.EnrollRequest{}, core.Heartbeat{NetworkMetricsAvailable: true, NetworkUploadMBPerSecond: float64(collectCalls)}, nil
		},
		Version: "0.2.0",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := worker.Run(ctx, validConfig(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(client.heartbeats) < 2 {
		t.Fatalf("heartbeats=%d, want at least 2", len(client.heartbeats))
	}
	if client.heartbeats[0].NetworkUploadMBPerSecond != 1 || client.heartbeats[1].NetworkUploadMBPerSecond != 2 {
		t.Fatalf("consecutive network rates=%v/%v", client.heartbeats[0].NetworkUploadMBPerSecond, client.heartbeats[1].NetworkUploadMBPerSecond)
	}
}

func TestWorkerUploadsLogsAfterFirstSuccessfulHeartbeatOnlyOncePerInterval(t *testing.T) {
	client := &fakeHeartbeatClient{}
	uploadCalls := 0
	worker := NewWorker(Dependencies{
		Client:  client,
		Collect: fakeCollect,
		Version: "0.3.7",
		LogUploader: func(context.Context, agentclient.Config) error {
			uploadCalls++
			return nil
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	if err := worker.Run(ctx, validConfig(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if uploadCalls != 1 {
		t.Fatalf("log upload calls=%d, want first successful heartbeat only", uploadCalls)
	}
}

func TestWorkerKeepsOnlineStateWhenLogUploadFails(t *testing.T) {
	client := &fakeHeartbeatClient{}
	var states []State
	var uploadErrors []string
	worker := NewWorker(Dependencies{
		Client:  client,
		Collect: fakeCollect,
		Version: "0.3.7",
		StatusSink: func(snapshot StatusSnapshot) {
			states = append(states, snapshot.State)
		},
		LogUploader: func(context.Context, agentclient.Config) error {
			return errors.New("upload failed credential=device-secret")
		},
		LogErrorSink: func(message string) {
			uploadErrors = append(uploadErrors, message)
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	if err := worker.Run(ctx, validConfig(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state == StateError {
			t.Fatalf("log upload failure changed worker state: %v", states)
		}
	}
	if len(uploadErrors) != 1 || strings.Contains(uploadErrors[0], "device-secret") {
		t.Fatalf("sanitized log upload errors=%q", uploadErrors)
	}
}

func TestWorkerRejectsInvalidDependencies(t *testing.T) {
	worker := NewWorker(Dependencies{})
	err := worker.Run(context.Background(), validConfig(), time.Second)
	var configurationError *ConfigurationError
	if !errors.As(err, &configurationError) {
		t.Fatalf("Run error = %T %v, want *ConfigurationError", err, err)
	}
}

func validConfig() agentclient.Config {
	return agentclient.Config{ServerURL: "https://it.example.test", NodeID: "node-1", Credential: "device-secret"}
}

func fakeCollect(string) (core.EnrollRequest, core.Heartbeat, error) {
	return core.EnrollRequest{}, core.Heartbeat{Hostname: "office-pc"}, nil
}

type fakeHeartbeatClient struct {
	mu     sync.Mutex
	calls  int
	errors []error
}

type recordingHeartbeatClient struct {
	heartbeats []core.Heartbeat
}

func (client *recordingHeartbeatClient) Heartbeat(_ context.Context, _ string, heartbeat core.Heartbeat) error {
	client.heartbeats = append(client.heartbeats, heartbeat)
	return nil
}

func (client *fakeHeartbeatClient) Heartbeat(context.Context, string, core.Heartbeat) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.calls++
	if len(client.errors) == 0 {
		return nil
	}
	err := client.errors[0]
	client.errors = client.errors[1:]
	return err
}

func (client *fakeHeartbeatClient) Calls() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.calls
}
