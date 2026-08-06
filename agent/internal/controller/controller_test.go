package controller

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	agentconfig "aceitcenter.local/platform/agent/internal/agent"
	"aceitcenter.local/platform/agent/internal/app"
	"aceitcenter.local/platform/internal/core"
)

type fakeEnroller struct {
	mu     sync.Mutex
	calls  int
	result EnrollmentResult
	err    error
}

type enrollerFunc func(context.Context, string, string) (EnrollmentResult, error)

func (f enrollerFunc) Enroll(ctx context.Context, serverURL, token string) (EnrollmentResult, error) {
	return f(ctx, serverURL, token)
}

func (f *fakeEnroller) Enroll(_ context.Context, _, _ string) (EnrollmentResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.result, f.err
}

func (f *fakeEnroller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeWorker struct {
	started chan agentconfig.Config
}

type fakePairer struct {
	mu       sync.Mutex
	start    PairingStartResult
	startErr error
	polls    []core.PairingPollResult
	pollErrs []error
	pollCall int
}

func (f *fakePairer) StartPairing(context.Context, string) (PairingStartResult, error) {
	return f.start, f.startErr
}

func (f *fakePairer) PollPairing(_ context.Context, _ agentconfig.PendingPairing) (core.PairingPollResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := f.pollCall
	f.pollCall++
	var result core.PairingPollResult
	if index < len(f.polls) {
		result = f.polls[index]
	}
	var err error
	if index < len(f.pollErrs) {
		err = f.pollErrs[index]
	}
	return result, err
}

func TestStartPairingPersistsThenWaitsForApproval(t *testing.T) {
	fixedNow := time.Unix(1_700_000_000, 0).UTC()
	var saved agentconfig.Config
	controller := New(Dependencies{
		Pairer: &fakePairer{start: PairingStartResult{
			PairingID:  "pairing-1",
			Credential: "pairing-secret",
			ExpiresAt:  fixedNow.Add(10 * time.Minute),
			PollAfter:  time.Hour,
		}},
		PreflightConfig: func() error { return nil },
		SaveConfig:      func(config agentconfig.Config) error { saved = config; return nil },
		Now:             func() time.Time { return fixedNow },
	})

	if err := controller.StartPairing(context.Background(), "http://it.ace-station.top:1111"); err != nil {
		t.Fatal(err)
	}
	if !saved.IsPendingPairing() || controller.Status().State != "waiting_for_approval" {
		t.Fatalf("saved=%#v status=%#v", saved, controller.Status())
	}
	if output := fmt.Sprintf("%#v", controller.Status()); strings.Contains(output, "pairing-secret") {
		t.Fatalf("status exposed pairing credential: %s", output)
	}
}

func TestPairingApprovalWritesCredentialAndStartsWorker(t *testing.T) {
	fixedNow := time.Unix(1_700_000_000, 0).UTC()
	worker := &fakeWorker{started: make(chan agentconfig.Config, 1)}
	saved := make(chan agentconfig.Config, 2)
	controller := New(Dependencies{
		Pairer: &fakePairer{
			start: PairingStartResult{PairingID: "pairing-1", Credential: "pairing-secret", ExpiresAt: fixedNow.Add(time.Hour), PollAfter: time.Millisecond},
			polls: []core.PairingPollResult{{ID: "pairing-1", State: core.PairingApproved, Node: &core.Node{ID: "node-1"}}},
		},
		PreflightConfig: func() error { return nil },
		SaveConfig:      func(config agentconfig.Config) error { saved <- config; return nil },
		Worker:          worker,
		Now:             func() time.Time { return fixedNow },
	})

	if err := controller.StartPairing(context.Background(), "https://it.example"); err != nil {
		t.Fatal(err)
	}
	select {
	case config := <-saved:
		if !config.IsPendingPairing() {
			t.Fatalf("first saved config = %#v", config)
		}
	case <-time.After(time.Second):
		t.Fatal("pending pairing was not saved")
	}
	select {
	case config := <-saved:
		if !config.IsEnrolled() || config.NodeID != "node-1" || config.Credential != "pairing-secret" || config.PendingPairing != nil {
			t.Fatalf("approved config = %#v", config)
		}
	case <-time.After(time.Second):
		t.Fatal("approved pairing was not saved")
	}
	select {
	case config := <-worker.started:
		if config.NodeID != "node-1" || config.Credential != "pairing-secret" {
			t.Fatalf("worker config = %#v", config)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not start after pairing approval")
	}
}

func TestBootstrapExpiredPendingPairingDoesNotPoll(t *testing.T) {
	fixedNow := time.Unix(1_700_000_000, 0).UTC()
	pairer := &fakePairer{}
	controller := New(Dependencies{
		LoadConfig: func() (agentconfig.Config, error) {
			return agentconfig.Config{PendingPairing: &agentconfig.PendingPairing{ServerURL: "https://it.example", PairingID: "pairing-1", Credential: "pairing-secret", ExpiresAt: fixedNow.Add(-time.Second)}}, nil
		},
		Pairer: pairer,
		Now:    func() time.Time { return fixedNow },
	})

	if err := controller.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := controller.Status(); status.State != "pairing_expired" {
		t.Fatalf("status = %#v", status)
	}
	time.Sleep(20 * time.Millisecond)
	pairer.mu.Lock()
	polls := pairer.pollCall
	pairer.mu.Unlock()
	if polls != 0 {
		t.Fatalf("expired pairing poll calls = %d", polls)
	}
}

func (f *fakeWorker) Run(ctx context.Context, config agentconfig.Config, _ time.Duration) error {
	select {
	case f.started <- config:
	case <-ctx.Done():
	}
	<-ctx.Done()
	return nil
}

func TestEnrollRejectsInvalidURLAndTokenBoundsBeforeNetwork(t *testing.T) {
	tests := []struct {
		name      string
		serverURL string
		token     string
	}{
		{name: "invalid URL", serverURL: "ftp://it.example", token: "token"},
		{name: "URL user info", serverURL: "https://token@it.example", token: "token"},
		{name: "URL query", serverURL: "https://it.example?access_token=secret", token: "token"},
		{name: "URL fragment", serverURL: "https://it.example#token", token: "token"},
		{name: "empty token", serverURL: "https://it.example", token: ""},
		{name: "token exceeds byte limit", serverURL: "https://it.example", token: strings.Repeat("x", 4097)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			enroller := &fakeEnroller{}
			controller := New(Dependencies{PreflightConfig: func() error { return nil }, Enroller: enroller})

			err := controller.Enroll(context.Background(), test.serverURL, test.token)

			if err == nil {
				t.Fatal("expected validation error")
			}
			if enroller.callCount() != 0 {
				t.Fatalf("enrollment calls = %d, want 0", enroller.callCount())
			}
		})
	}
}

func TestEnrollPreflightsConfigBeforeConsumingToken(t *testing.T) {
	enroll := &fakeEnroller{}
	controller := New(Dependencies{PreflightConfig: func() error { return fs.ErrPermission }, Enroller: enroll})

	err := controller.Enroll(context.Background(), "https://it.example", "one-time")

	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
	if enroll.callCount() != 0 {
		t.Fatalf("enrollment calls = %d, want 0", enroll.callCount())
	}
}

func TestEnrollSavesConfigurationAndExposesOnlySafeStatus(t *testing.T) {
	const token = "one-time-token"
	const credential = "persistent-credential"
	worker := &fakeWorker{started: make(chan agentconfig.Config, 1)}
	var saved agentconfig.Config
	controller := New(Dependencies{
		PreflightConfig: func() error { return nil },
		Enroller:        &fakeEnroller{result: EnrollmentResult{NodeID: "node-1", Credential: credential}},
		SaveConfig: func(config agentconfig.Config) error {
			saved = config
			return nil
		},
		Worker: worker,
	})

	if err := controller.Enroll(context.Background(), "https://it.example/", token); err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}
	if saved != (agentconfig.Config{ServerURL: "https://it.example", NodeID: "node-1", Credential: credential}) {
		t.Fatalf("saved = %#v", saved)
	}

	select {
	case started := <-worker.started:
		if started != saved {
			t.Fatalf("worker config = %#v, want %#v", started, saved)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	status := controller.Status()
	if status.NodeID != "node-1" || status.ServerURL != "https://it.example" {
		t.Fatalf("status = %#v", status)
	}
	if output := fmt.Sprintf("%#v", status); strings.Contains(output, token) || strings.Contains(output, credential) {
		t.Fatalf("status exposed secret: %s", output)
	}
}

func TestEnrollDoesNotStartWorkerWhenSaveFails(t *testing.T) {
	worker := &fakeWorker{started: make(chan agentconfig.Config, 1)}
	controller := New(Dependencies{
		PreflightConfig: func() error { return nil },
		Enroller:        &fakeEnroller{result: EnrollmentResult{NodeID: "node-1", Credential: "credential"}},
		SaveConfig:      func(agentconfig.Config) error { return fs.ErrPermission },
		Worker:          worker,
	})

	err := controller.Enroll(context.Background(), "https://it.example", "one-time-token")

	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
	select {
	case config := <-worker.started:
		t.Fatalf("worker started with %#v after save failure", config)
	default:
	}
}

func TestCheckUpdateDoesNotBlockStatusWhileOperationRuns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	controller := New(Dependencies{
		CheckUpdate: func(context.Context) (UpdateStatus, error) {
			close(started)
			<-release
			return UpdateStatus{Available: true, Version: "2.0.0"}, nil
		},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = controller.CheckUpdate(context.Background())
	}()
	<-started

	statusDone := make(chan Status, 1)
	go func() { statusDone <- controller.Status() }()
	select {
	case <-statusDone:
	case <-time.After(time.Second):
		t.Fatal("Status blocked behind update operation")
	}
	close(release)
	<-done
}

func TestRestartWorkerUsesSavedConfiguration(t *testing.T) {
	worker := &fakeWorker{started: make(chan agentconfig.Config, 2)}
	controller := New(Dependencies{
		PreflightConfig: func() error { return nil },
		Enroller:        &fakeEnroller{result: EnrollmentResult{NodeID: "node-1", Credential: "credential"}},
		SaveConfig:      func(agentconfig.Config) error { return nil },
		Worker:          worker,
	})
	if err := controller.Enroll(context.Background(), "https://it.example", "one-time-token"); err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}
	<-worker.started

	if err := controller.RestartWorker(context.Background()); err != nil {
		t.Fatalf("RestartWorker() error = %v", err)
	}
	select {
	case restarted := <-worker.started:
		if restarted.NodeID != "node-1" || restarted.Credential != "credential" {
			t.Fatalf("restarted config = %#v", restarted)
		}
	case <-time.After(time.Second):
		t.Fatal("restarted worker did not start")
	}
}

func TestCreateDiagnosticsUsesSafeStatus(t *testing.T) {
	controller := New(Dependencies{
		PreflightConfig: func() error { return nil },
		Enroller:        &fakeEnroller{result: EnrollmentResult{NodeID: "node-1", Credential: "credential"}},
		SaveConfig:      func(agentconfig.Config) error { return nil },
		CreateDiagnostics: func(_ context.Context, status Status) (string, error) {
			if status.NodeID != "node-1" || status.ServerURL != "https://it.example" {
				t.Fatalf("diagnostic status = %#v", status)
			}
			if strings.Contains(fmt.Sprintf("%#v", status), "credential") {
				t.Fatalf("diagnostic status exposed a credential: %#v", status)
			}
			return "diagnostics.zip", nil
		},
	})
	if err := controller.Enroll(context.Background(), "https://it.example", "one-time-token"); err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}

	path, err := controller.CreateDiagnostics(context.Background())

	if err != nil || path != "diagnostics.zip" {
		t.Fatalf("CreateDiagnostics() = %q, %v", path, err)
	}
}

func TestReportStatusMapsWorkerSnapshotWithoutCredential(t *testing.T) {
	controller := New(Dependencies{
		PreflightConfig: func() error { return nil },
		Enroller:        &fakeEnroller{result: EnrollmentResult{NodeID: "node-1", Credential: "persistent-credential"}},
		SaveConfig:      func(agentconfig.Config) error { return nil },
	})
	if err := controller.Enroll(context.Background(), "https://it.example", "one-time-token"); err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}

	controller.ReportStatus(app.StatusSnapshot{
		State:         app.StateError,
		NodeID:        "node-1",
		ServerURL:     "https://it.example",
		Version:       "1.2.3",
		LastHeartbeat: time.Unix(1, 0).UTC(),
		Error:         "heartbeat rejected: persistent-credential",
	})

	status := controller.Status()
	if status.State != "error" || status.Version != "1.2.3" || status.LastHeartbeat != time.Unix(1, 0).UTC() {
		t.Fatalf("status = %#v", status)
	}
	if strings.Contains(status.Error, "persistent-credential") {
		t.Fatalf("status error exposed credential: %q", status.Error)
	}
}

func TestBootstrapRestoresConfigurationAndStopsWorkerWithLifetime(t *testing.T) {
	worker := &fakeWorker{started: make(chan agentconfig.Config, 1)}
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller := New(Dependencies{
		LoadConfig: func() (agentconfig.Config, error) {
			return agentconfig.Config{ServerURL: "https://it.example", NodeID: "node-1", Credential: "credential"}, nil
		},
		Worker: worker,
	})

	if err := controller.Bootstrap(lifetime); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	select {
	case config := <-worker.started:
		if config.NodeID != "node-1" {
			t.Fatalf("worker config = %#v", config)
		}
	case <-time.After(time.Second):
		t.Fatal("bootstrap did not start worker")
	}

	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestBootstrapMissingConfigurationWaitsForEnrollment(t *testing.T) {
	controller := New(Dependencies{
		LoadConfig: func() (agentconfig.Config, error) { return agentconfig.Config{}, os.ErrNotExist },
	})

	if err := controller.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if status := controller.Status(); status.State != "waiting" {
		t.Fatalf("status = %#v", status)
	}
}

func TestBootstrapInvalidConfigurationReportsDegradedStateWithoutRawError(t *testing.T) {
	logged := 0
	controller := New(Dependencies{
		LoadConfig: func() (agentconfig.Config, error) { return agentconfig.Config{}, errors.New("credential=secret") },
		LogBootstrapFailure: func() {
			logged++
		},
	})

	if err := controller.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if status := controller.Status(); status.State != "degraded" || status.Error != "agent operation failed" {
		t.Fatalf("status = %#v", status)
	}
	if logged != 1 {
		t.Fatalf("bootstrap logs = %d, want 1", logged)
	}
}

func TestReportStatusProjectsUnsafeValues(t *testing.T) {
	controller := New(Dependencies{
		PreflightConfig: func() error { return nil },
		Enroller:        &fakeEnroller{result: EnrollmentResult{NodeID: "node-1", Credential: "persistent-credential"}},
		SaveConfig:      func(agentconfig.Config) error { return nil },
	})
	if err := controller.Enroll(context.Background(), "https://it.example", "one-time-token"); err != nil {
		t.Fatalf("Enroll() error = %v", err)
	}

	controller.ReportStatus(app.StatusSnapshot{
		State:     app.StateError,
		NodeID:    "node-1",
		ServerURL: "https://credential@it.example?token=secret",
		Error:     "heartbeat rejected: persistent-credential and one-time-token",
	})

	status := controller.Status()
	if status.ServerURL != "" {
		t.Fatalf("status URL = %q, want empty", status.ServerURL)
	}
	if status.Error != "agent operation failed" {
		t.Fatalf("status error = %q", status.Error)
	}
}

func TestCheckUpdateOmitsUnsafeURL(t *testing.T) {
	controller := New(Dependencies{
		CheckUpdate: func(context.Context) (UpdateStatus, error) {
			return UpdateStatus{Available: true, Version: "2.0.0", URL: "https://token@updates.example/download?signature=secret#fragment"}, nil
		},
	})

	status, err := controller.CheckUpdate(context.Background())

	if err != nil {
		t.Fatalf("CheckUpdate() error = %v", err)
	}
	if status.URL != "" {
		t.Fatalf("update URL = %q, want empty", status.URL)
	}
}

func TestBootstrapChecksUpdateImmediatelyAndContinuesAfterNetworkFailure(t *testing.T) {
	calls := make(chan int, 3)
	callCount := 0
	controller := New(Dependencies{
		LoadConfig: func() (agentconfig.Config, error) {
			return agentconfig.Config{ServerURL: "https://it.example", NodeID: "node-1", Credential: "credential"}, nil
		},
		CheckUpdate: func(context.Context) (UpdateStatus, error) {
			callCount++
			calls <- callCount
			if callCount == 1 {
				return UpdateStatus{}, errors.New("network unavailable: credential")
			}
			return UpdateStatus{}, nil
		},
		UpdateInterval: 10 * time.Millisecond,
		UpdateJitter:   func(time.Duration) time.Duration { return 0 },
	})
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := controller.Bootstrap(lifetime); err != nil {
		t.Fatal(err)
	}

	for want := 1; want <= 2; want++ {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("update call = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for update call %d", want)
		}
	}
	if status := controller.Status(); strings.Contains(fmt.Sprintf("%#v", status), "credential") {
		t.Fatalf("status exposed update error secret: %#v", status)
	}
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollChecksUpdateImmediately(t *testing.T) {
	checked := make(chan struct{}, 1)
	controller := New(Dependencies{
		PreflightConfig: func() error { return nil },
		Enroller:        &fakeEnroller{result: EnrollmentResult{NodeID: "node-1", Credential: "credential"}},
		SaveConfig:      func(agentconfig.Config) error { return nil },
		CheckUpdate: func(context.Context) (UpdateStatus, error) {
			checked <- struct{}{}
			return UpdateStatus{}, nil
		},
		UpdateInterval: time.Hour,
		UpdateJitter:   func(time.Duration) time.Duration { return 0 },
	})
	if err := controller.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Enroll(context.Background(), "https://it.example", "one-time-token"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("enrollment did not trigger immediate update check")
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestNewDefaultsUpdateScheduleToHourly(t *testing.T) {
	controller := New(Dependencies{})

	if got := controller.dependencies.UpdateInterval; got != time.Hour {
		t.Fatalf("default update interval = %s, want 1h", got)
	}
}

func TestScheduledUpdateUsesInjectedJitterBound(t *testing.T) {
	calls := make(chan struct{}, 2)
	jitterMaximum := make(chan time.Duration, 1)
	controller := New(Dependencies{
		LoadConfig: func() (agentconfig.Config, error) {
			return agentconfig.Config{ServerURL: "https://it.example", NodeID: "node-1", Credential: "credential"}, nil
		},
		CheckUpdate: func(context.Context) (UpdateStatus, error) {
			calls <- struct{}{}
			return UpdateStatus{}, nil
		},
		UpdateInterval: 10 * time.Millisecond,
		UpdateJitter: func(maximum time.Duration) time.Duration {
			select {
			case jitterMaximum <- maximum:
			default:
			}
			return 5 * time.Millisecond
		},
	})
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := controller.Bootstrap(lifetime); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatal("scheduled check did not run")
		}
	}
	select {
	case got := <-jitterMaximum:
		if got != 10*time.Minute {
			t.Fatalf("jitter maximum = %s, want 10m", got)
		}
	case <-time.After(time.Second):
		t.Fatal("jitter function was not called")
	}
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestManualAndScheduledChecksShareSingleFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int
	var callsMu sync.Mutex
	controller := New(Dependencies{
		LoadConfig: func() (agentconfig.Config, error) {
			return agentconfig.Config{ServerURL: "https://it.example", NodeID: "node-1", Credential: "credential"}, nil
		},
		CheckUpdate: func(context.Context) (UpdateStatus, error) {
			callsMu.Lock()
			calls++
			if calls == 1 {
				close(started)
			}
			callsMu.Unlock()
			<-release
			return UpdateStatus{Available: true, Version: "0.2.0"}, nil
		},
		UpdateInterval: time.Hour,
		UpdateJitter:   func(time.Duration) time.Duration { return 0 },
	})
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := controller.Bootstrap(lifetime); err != nil {
		t.Fatal(err)
	}
	<-started
	manualDone := make(chan UpdateStatus, 1)
	go func() {
		status, _ := controller.CheckUpdate(context.Background())
		manualDone <- status
	}()
	time.Sleep(10 * time.Millisecond)
	callsMu.Lock()
	gotCalls := calls
	callsMu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("concurrent pipeline calls = %d, want 1", gotCalls)
	}
	close(release)
	select {
	case status := <-manualDone:
		if !status.Available || status.Version != "0.2.0" {
			t.Fatalf("manual status = %#v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("manual check did not receive shared result")
	}
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownCancelsInFlightScheduledUpdate(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	controller := New(Dependencies{
		LoadConfig: func() (agentconfig.Config, error) {
			return agentconfig.Config{ServerURL: "https://it.example", NodeID: "node-1", Credential: "credential"}, nil
		},
		CheckUpdate: func(ctx context.Context) (UpdateStatus, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return UpdateStatus{}, ctx.Err()
		},
		UpdateInterval: time.Hour,
		UpdateJitter:   func(time.Duration) time.Duration { return 0 },
	})
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	defer cancelLifetime()
	if err := controller.Bootstrap(lifetime); err != nil {
		t.Fatal(err)
	}
	<-started
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("scheduled update context was not canceled")
	}
}

func TestCheckUpdateDoesNotStartFlightAfterShutdownReturns(t *testing.T) {
	updateCalled := make(chan struct{}, 1)
	controller := New(Dependencies{CheckUpdate: func(context.Context) (UpdateStatus, error) {
		updateCalled <- struct{}{}
		return UpdateStatus{}, nil
	}})

	if err := controller.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if _, err := controller.CheckUpdate(context.Background()); err == nil {
		t.Fatal("CheckUpdate() started a flight after Shutdown() returned")
	}
	select {
	case <-updateCalled:
		t.Fatal("update dependency ran after Shutdown() returned")
	default:
	}
}

func TestShutdownReturnsContextErrorWhileCheckUpdateHoldsLifecycleLocks(t *testing.T) {
	const nowUnix = 10_000
	checkHoldingState := make(chan struct{})
	releaseCheck := make(chan struct{})
	controller := New(Dependencies{
		CheckUpdate: func(context.Context) (UpdateStatus, error) { return UpdateStatus{}, nil },
		Now: func() time.Time {
			close(checkHoldingState)
			<-releaseCheck
			return time.Unix(nowUnix, 0)
		},
		UpdatePendingTTL: time.Minute,
	})
	controller.updateMu.Lock()
	controller.updatePending = true
	controller.pendingAt = time.Unix(nowUnix, 0)
	controller.pendingUpdate = UpdateStatus{Available: true, Version: "0.2.0"}
	controller.updateMu.Unlock()

	checkResult := make(chan error, 1)
	go func() {
		_, err := controller.CheckUpdate(context.Background())
		checkResult <- err
	}()
	<-checkHoldingState
	shutdownContext, cancel := context.WithCancel(context.Background())
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- controller.Shutdown(shutdownContext) }()
	cancel()

	select {
	case err := <-shutdownResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Shutdown() error = %v", err)
		}
	case <-time.After(time.Second):
		close(releaseCheck)
		<-checkResult
		t.Fatal("Shutdown() ignored context while CheckUpdate held lifecycle locks")
	}
	close(releaseCheck)
	<-checkResult
}

func TestCheckUpdateFlightCreationInterleavesWithConfigurationActivationWithoutDeadlock(t *testing.T) {
	type operationResult struct {
		name string
		err  error
	}

	tests := []struct {
		name       string
		bootstrap  bool
		wantNodeID string
		wantURL    string
	}{
		{name: "enroll", wantNodeID: "enrolled-node", wantURL: "https://new.example"},
		{name: "bootstrap", bootstrap: true, wantNodeID: "bootstrapped-node", wantURL: "https://saved.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pendingRecordedAt := time.Unix(10_000, 0)
			currentTime := pendingRecordedAt
			blockNow := false
			var clockMu sync.Mutex
			pendingCheckStarted := make(chan struct{})
			releasePendingCheck := make(chan struct{})
			activationHoldingLifecycle := make(chan struct{})
			releaseConfigurationActivation := make(chan struct{})
			var blockOnce sync.Once
			dependencies := Dependencies{
				Now: func() time.Time {
					clockMu.Lock()
					now, shouldBlock := currentTime, blockNow
					clockMu.Unlock()
					if shouldBlock {
						blockOnce.Do(func() {
							close(pendingCheckStarted)
							<-releasePendingCheck
						})
					}
					return now
				},
				UpdatePendingTTL: time.Minute,
				UpdateInterval:   time.Hour,
				UpdateJitter:     func(time.Duration) time.Duration { return 0 },
				RunUpdate: func(_ context.Context, _ agentconfig.Config, authorize UpdateLaunchAuthorizer) (UpdateStatus, error) {
					finish, err := authorize()
					if err != nil {
						return UpdateStatus{}, err
					}
					status := UpdateStatus{Available: true, Version: "2.0.0"}
					finish(status, true)
					return status, nil
				},
			}
			var controller *Controller
			stageExpiredPending := func() error {
				if _, err := controller.CheckUpdate(context.Background()); err != nil {
					return err
				}
				return nil
			}
			blockExpiredPending := func() {
				clockMu.Lock()
				currentTime = pendingRecordedAt.Add(2 * time.Minute)
				blockNow = true
				clockMu.Unlock()
			}
			var lifecycleOperation func() error
			if test.bootstrap {
				dependencies.LoadConfig = func() (agentconfig.Config, error) {
					return agentconfig.Config{ServerURL: test.wantURL, NodeID: test.wantNodeID, Credential: "saved-secret"}, nil
				}
				controller = New(dependencies)
				if err := stageExpiredPending(); err != nil {
					t.Fatalf("stage expired pending update: %v", err)
				}
				blockExpiredPending()
				lifecycleOperation = func() error { return controller.Bootstrap(context.Background()) }
			} else {
				dependencies.PreflightConfig = func() error { return nil }
				dependencies.SaveConfig = func(agentconfig.Config) error { return nil }
				dependencies.Enroller = enrollerFunc(func(context.Context, string, string) (EnrollmentResult, error) {
					if err := stageExpiredPending(); err != nil {
						return EnrollmentResult{}, err
					}
					blockExpiredPending()
					return EnrollmentResult{NodeID: test.wantNodeID, Credential: "new-secret"}, nil
				})
				controller = New(dependencies)
				lifecycleOperation = func() error {
					return controller.Enroll(context.Background(), test.wantURL, "one-time")
				}
			}
			originalGenerationStop := controller.generationStop
			controller.generationStop = func() {
				originalGenerationStop()
				close(activationHoldingLifecycle)
				<-releaseConfigurationActivation
			}

			results := make(chan operationResult, 2)
			go func() { results <- operationResult{name: "lifecycle", err: lifecycleOperation()} }()
			select {
			case <-pendingCheckStarted:
			case <-time.After(time.Second):
				t.Fatal("lifecycle operation did not reach the expired pending check")
			}
			go func() {
				_, err := controller.CheckUpdate(context.Background())
				results <- operationResult{name: "check update", err: err}
			}()
			time.Sleep(10 * time.Millisecond)
			close(releasePendingCheck)
			select {
			case <-activationHoldingLifecycle:
			case <-time.After(time.Second):
				t.Fatal("lifecycle operation did not enter configuration activation")
			}
			// lifecycle 此时持有 mu；先让排队的 CheckUpdate 获得 updateMu，
			// 再恢复配置激活以复现旧锁环。
			time.Sleep(10 * time.Millisecond)
			close(releaseConfigurationActivation)

			for completed := 0; completed < 2; completed++ {
				select {
				case result := <-results:
					if result.err != nil {
						t.Fatalf("%s error = %v", result.name, result.err)
					}
				case <-time.After(time.Second):
					t.Fatal("CheckUpdate flight creation and configuration activation deadlocked")
				}
			}
			if status := controller.Status(); status.NodeID != test.wantNodeID || status.ServerURL != test.wantURL {
				t.Fatalf("configuration activation status = %#v, want node ID %q and URL %q", status, test.wantNodeID, test.wantURL)
			}
			shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := controller.Shutdown(shutdownContext); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
		})
	}
}

func TestShutdownReturnsContextErrorWhileAuthorizedLaunchIsUnfinished(t *testing.T) {
	authorized := make(chan struct{})
	allowFinish := make(chan struct{})
	controller := New(Dependencies{
		RunUpdate: func(_ context.Context, _ agentconfig.Config, authorize UpdateLaunchAuthorizer) (UpdateStatus, error) {
			finish, err := authorize()
			if err != nil {
				return UpdateStatus{}, err
			}
			close(authorized)
			<-allowFinish
			finish(UpdateStatus{}, false)
			return UpdateStatus{}, nil
		},
	})
	updateResult := make(chan error, 1)
	go func() {
		_, err := controller.CheckUpdate(context.Background())
		updateResult <- err
	}()
	<-authorized
	shutdownContext, cancel := context.WithCancel(context.Background())
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- controller.Shutdown(shutdownContext) }()
	cancel()

	select {
	case err := <-shutdownResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Shutdown() error = %v", err)
		}
	case <-time.After(time.Second):
		close(allowFinish)
		<-updateResult
		t.Fatal("Shutdown() ignored caller context while waiting for launch authorization")
	}
	close(allowFinish)
	<-updateResult
	if err := controller.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown() error = %v", err)
	}
}

func TestLaunchedUpdatePendingExpiresAndAllowsRetry(t *testing.T) {
	now := time.Unix(100, 0)
	calls := 0
	controller := New(Dependencies{
		RunUpdate: func(_ context.Context, _ agentconfig.Config, authorize UpdateLaunchAuthorizer) (UpdateStatus, error) {
			calls++
			status := UpdateStatus{Available: true, Version: "0.2.0", URL: "https://it.example/download/setup.exe"}
			finish, err := authorize()
			if err != nil {
				return UpdateStatus{}, err
			}
			finish(status, true)
			return status, nil
		},
		Now:              func() time.Time { return now },
		UpdatePendingTTL: time.Minute,
	})

	first, err := controller.CheckUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status := controller.Status(); status.State != "updating" {
		t.Fatalf("status after launching update = %#v, want updating", status)
	}
	second, err := controller.CheckUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("update pipeline calls = %d, want 1", calls)
	}
	if second != first {
		t.Fatalf("second status = %#v, want %#v", second, first)
	}
	now = now.Add(time.Minute + time.Second)
	third, err := controller.CheckUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || third != first {
		t.Fatalf("after TTL calls = %d, status = %#v", calls, third)
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchFailureLeavesNoPendingAndRetriesImmediately(t *testing.T) {
	launchErr := errors.New("CreateProcess failed")
	calls := 0
	controller := New(Dependencies{
		RunUpdate: func(_ context.Context, _ agentconfig.Config, authorize UpdateLaunchAuthorizer) (UpdateStatus, error) {
			calls++
			finish, err := authorize()
			if err != nil {
				return UpdateStatus{}, err
			}
			finish(UpdateStatus{}, false)
			return UpdateStatus{}, launchErr
		},
	})

	for attempt := 0; attempt < 2; attempt++ {
		if _, err := controller.CheckUpdate(context.Background()); !errors.Is(err, launchErr) {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
	}
	if calls != 2 {
		t.Fatalf("pipeline calls = %d, want 2", calls)
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentCancelsOldGenerationAndStartsIndependentCheck(t *testing.T) {
	oldStarted := make(chan struct{})
	oldCanceled := make(chan struct{})
	newStarted := make(chan struct{})
	controller := New(Dependencies{
		LoadConfig: func() (agentconfig.Config, error) {
			return agentconfig.Config{ServerURL: "https://old.example", NodeID: "old", Credential: "old-secret"}, nil
		},
		PreflightConfig: func() error { return nil },
		Enroller:        &fakeEnroller{result: EnrollmentResult{NodeID: "new", Credential: "new-secret"}},
		SaveConfig:      func(agentconfig.Config) error { return nil },
		RunUpdate: func(ctx context.Context, config agentconfig.Config, _ UpdateLaunchAuthorizer) (UpdateStatus, error) {
			if config.ServerURL == "https://old.example" {
				close(oldStarted)
				<-ctx.Done()
				close(oldCanceled)
				return UpdateStatus{}, ctx.Err()
			}
			select {
			case <-newStarted:
			default:
				close(newStarted)
			}
			return UpdateStatus{}, nil
		},
		UpdateInterval: time.Hour,
		UpdateJitter:   func(time.Duration) time.Duration { return 0 },
	})
	if err := controller.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-oldStarted
	if err := controller.Enroll(context.Background(), "https://new.example", "one-time"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldCanceled:
	case <-time.After(time.Second):
		t.Fatal("old update generation was not canceled")
	}
	select {
	case <-newStarted:
	case <-time.After(time.Second):
		t.Fatal("new generation joined old update flight")
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestOldGenerationCannotAuthorizeLaunchAfterEnrollment(t *testing.T) {
	oldStaged := make(chan struct{})
	allowOldAuthorize := make(chan struct{})
	oldAuthorization := make(chan error, 1)
	controller := New(Dependencies{
		LoadConfig: func() (agentconfig.Config, error) {
			return agentconfig.Config{ServerURL: "https://old.example", NodeID: "old", Credential: "old-secret"}, nil
		},
		PreflightConfig: func() error { return nil },
		Enroller:        &fakeEnroller{result: EnrollmentResult{NodeID: "new", Credential: "new-secret"}},
		SaveConfig:      func(agentconfig.Config) error { return nil },
		RunUpdate: func(_ context.Context, config agentconfig.Config, authorize UpdateLaunchAuthorizer) (UpdateStatus, error) {
			if config.ServerURL != "https://old.example" {
				return UpdateStatus{}, nil
			}
			close(oldStaged)
			<-allowOldAuthorize
			_, err := authorize()
			oldAuthorization <- err
			return UpdateStatus{}, err
		},
		UpdateInterval: time.Hour,
		UpdateJitter:   func(time.Duration) time.Duration { return 0 },
	})
	if err := controller.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-oldStaged
	if err := controller.Enroll(context.Background(), "https://new.example", "one-time"); err != nil {
		t.Fatal(err)
	}
	close(allowOldAuthorize)
	select {
	case err := <-oldAuthorization:
		if err == nil {
			t.Fatal("old generation authorized helper launch")
		}
	case <-time.After(time.Second):
		t.Fatal("old generation authorization did not finish")
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentRejectsPendingHelperBeforeNetwork(t *testing.T) {
	enroller := &fakeEnroller{result: EnrollmentResult{NodeID: "new", Credential: "new-secret"}}
	controller := New(Dependencies{
		PreflightConfig: func() error { return nil },
		Enroller:        enroller,
		SaveConfig:      func(agentconfig.Config) error { return nil },
		RunUpdate: func(_ context.Context, _ agentconfig.Config, authorize UpdateLaunchAuthorizer) (UpdateStatus, error) {
			status := UpdateStatus{Available: true, Version: "0.2.0"}
			finish, err := authorize()
			if err != nil {
				return UpdateStatus{}, err
			}
			finish(status, true)
			return status, nil
		},
	})
	if _, err := controller.CheckUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Enroll(context.Background(), "https://new.example", "one-time"); err == nil {
		t.Fatal("Enroll() accepted configuration switch while helper was pending")
	}
	if enroller.callCount() != 0 {
		t.Fatalf("enrollment calls = %d, want 0", enroller.callCount())
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentAllowsExpiredPendingHelper(t *testing.T) {
	now := time.Unix(100, 0)
	enroller := &fakeEnroller{result: EnrollmentResult{NodeID: "new", Credential: "new-secret"}}
	controller := New(Dependencies{
		PreflightConfig:  func() error { return nil },
		Enroller:         enroller,
		SaveConfig:       func(agentconfig.Config) error { return nil },
		Now:              func() time.Time { return now },
		UpdatePendingTTL: time.Minute,
		RunUpdate: func(_ context.Context, _ agentconfig.Config, authorize UpdateLaunchAuthorizer) (UpdateStatus, error) {
			status := UpdateStatus{Available: true, Version: "0.2.0"}
			finish, err := authorize()
			if err != nil {
				return UpdateStatus{}, err
			}
			finish(status, true)
			return status, nil
		},
	})
	if _, err := controller.CheckUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute + time.Second)

	if err := controller.Enroll(context.Background(), "https://new.example", "one-time"); err != nil {
		t.Fatalf("Enroll() rejected an expired pending helper: %v", err)
	}
	if enroller.callCount() != 1 {
		t.Fatalf("enrollment calls = %d, want 1", enroller.callCount())
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownDoesNotCancelAuthorizedHelperLaunch(t *testing.T) {
	authorized := make(chan struct{})
	allowFinish := make(chan struct{})
	updateContext := make(chan context.Context, 1)
	controller := New(Dependencies{
		RunUpdate: func(ctx context.Context, _ agentconfig.Config, authorize UpdateLaunchAuthorizer) (UpdateStatus, error) {
			finish, err := authorize()
			if err != nil {
				return UpdateStatus{}, err
			}
			updateContext <- ctx
			close(authorized)
			<-allowFinish
			finish(UpdateStatus{}, false)
			return UpdateStatus{}, nil
		},
	})
	updateResult := make(chan error, 1)
	go func() {
		_, err := controller.CheckUpdate(context.Background())
		updateResult <- err
	}()
	<-authorized
	ctx := <-updateContext
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- controller.Shutdown(context.Background()) }()

	select {
	case <-ctx.Done():
		close(allowFinish)
		<-updateResult
		<-shutdownResult
		t.Fatal("Shutdown() canceled an update after helper launch authorization")
	case <-time.After(20 * time.Millisecond):
	}
	close(allowFinish)
	if err := <-updateResult; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatal(err)
	}
}
