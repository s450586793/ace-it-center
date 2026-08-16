package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentconfig "aceitcenter.local/platform/agent/internal/agent"
	"aceitcenter.local/platform/agent/internal/app"
	"aceitcenter.local/platform/agent/internal/controller"
	"aceitcenter.local/platform/agent/internal/update"
	"aceitcenter.local/platform/internal/core"
)

type fakeServiceUpdateClient struct {
	result       update.CheckResult
	checkOptions update.CheckOptions
	applyOptions update.ApplyOptions
	checkErr     error
	launchErr    error
}

type fakeForegroundController struct {
	bootstraps int
	enrolls    int
	pairings   int
	serverURL  string
	token      string
}

type foregroundPairer struct {
	polls int
}

func (f *foregroundPairer) StartPairing(context.Context, string) (controller.PairingStartResult, error) {
	return controller.PairingStartResult{}, errors.New("unexpected pairing start")
}

func (f *foregroundPairer) PollPairing(context.Context, agentconfig.PendingPairing) (core.PairingPollResult, error) {
	f.polls++
	return core.PairingPollResult{ID: "pairing-1", State: core.PairingApproved, Node: &core.Node{ID: "node-1"}}, nil
}

type foregroundWorker struct {
	started chan agentconfig.Config
}

type fakeLogUploadClient struct {
	credential string
	agentLog   string
	updateLog  string
}

func (client *fakeLogUploadClient) UploadLogs(_ context.Context, credential, agentLog, updateLog string) error {
	client.credential = credential
	client.agentLog = agentLog
	client.updateLog = updateLog
	return nil
}

func (w foregroundWorker) Run(ctx context.Context, config agentconfig.Config, _ time.Duration) error {
	w.started <- config
	<-ctx.Done()
	return nil
}

func (f *fakeForegroundController) Bootstrap(context.Context) error {
	f.bootstraps++
	return nil
}

func (f *fakeForegroundController) Enroll(_ context.Context, serverURL, token string) error {
	f.enrolls++
	f.serverURL = serverURL
	f.token = token
	return nil
}

func (f *fakeForegroundController) StartPairing(_ context.Context, serverURL string) error {
	f.pairings++
	f.serverURL = serverURL
	return nil
}

func TestLogServiceHeartbeatErrorIncludesSafeCause(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	logServiceHeartbeatState(logger, app.StatusSnapshot{
		State:   app.StateError,
		NodeID:  "node-1",
		Error:   "send request: context deadline exceeded",
		Version: "0.4.9",
	})

	logged := output.String()
	if !strings.Contains(logged, `"level":"WARN"`) ||
		!strings.Contains(logged, `"state":"error"`) ||
		!strings.Contains(logged, `"node_id":"node-1"`) ||
		!strings.Contains(logged, `"error":"send request: context deadline exceeded"`) {
		t.Fatalf("heartbeat error log = %q", logged)
	}
}

func TestForegroundLifecycleStartsPairingWithoutEnrollmentToken(t *testing.T) {
	controller := &fakeForegroundController{}

	if err := runForegroundLifecycle(context.Background(), "https://it.example", "", controller); err != nil {
		t.Fatal(err)
	}
	if controller.bootstraps != 1 || controller.pairings != 1 || controller.enrolls != 0 || controller.serverURL != "https://it.example" {
		t.Fatalf("controller = %#v", controller)
	}
}

func TestForegroundLifecycleKeepsEnrollmentTokenCompatibility(t *testing.T) {
	controller := &fakeForegroundController{}

	if err := runForegroundLifecycle(context.Background(), "https://it.example", "legacy-token", controller); err != nil {
		t.Fatal(err)
	}
	if controller.bootstraps != 1 || controller.enrolls != 1 || controller.pairings != 0 || controller.token != "legacy-token" {
		t.Fatalf("controller = %#v", controller)
	}
}

func TestForegroundPendingConfigResumesPairingWithoutStartingOldWorker(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	pairer := &foregroundPairer{}
	worker := foregroundWorker{started: make(chan agentconfig.Config, 1)}
	saved := make(chan agentconfig.Config, 1)
	lifecycle := controller.New(controller.Dependencies{
		LoadConfig: func() (agentconfig.Config, error) {
			return agentconfig.Config{PendingPairing: &agentconfig.PendingPairing{ServerURL: "https://it.example", PairingID: "pairing-1", Credential: "pairing-secret", ExpiresAt: now.Add(time.Hour)}}, nil
		},
		Pairer:              pairer,
		PairingPollInterval: time.Millisecond,
		SaveConfig:          func(config agentconfig.Config) error { saved <- config; return nil },
		Worker:              worker,
		Now:                 func() time.Time { return now },
	})

	handled, err := runForegroundLifecycleForConfig(context.Background(), agentconfig.Config{PendingPairing: &agentconfig.PendingPairing{ServerURL: "https://it.example", PairingID: "pairing-1", Credential: "pairing-secret", ExpiresAt: now.Add(time.Hour)}}, nil, "", "", lifecycle)
	if err != nil || !handled {
		t.Fatalf("runForegroundLifecycleForConfig() = (%v, %v)", handled, err)
	}
	select {
	case config := <-saved:
		if !config.IsEnrolled() || config.PendingPairing != nil || config.Credential != "pairing-secret" {
			t.Fatalf("saved config = %#v", config)
		}
	case <-time.After(time.Second):
		t.Fatal("approved pending configuration was not persisted")
	}
	select {
	case config := <-worker.started:
		if !config.IsEnrolled() || config.PendingPairing != nil {
			t.Fatalf("worker received pending configuration: %#v", config)
		}
	case <-time.After(time.Second):
		t.Fatal("approved pending configuration did not start worker")
	}
	if pairer.polls == 0 {
		t.Fatal("pending pairing was not polled")
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lifecycle.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestUploadServiceLogsSendsRedactedBoundedTails(t *testing.T) {
	directory := t.TempDir()
	agentLogPath := filepath.Join(directory, "agent.log")
	updateLogPath := filepath.Join(directory, "update.log")
	if err := os.WriteFile(agentLogPath, []byte("old\ncredential=device-secret\nagent tail"), 0o600); err != nil {
		t.Fatalf("write agent log: %v", err)
	}
	if err := os.WriteFile(updateLogPath, []byte("update tail"), 0o600); err != nil {
		t.Fatalf("write update log: %v", err)
	}
	client := &fakeLogUploadClient{}

	err := uploadServiceLogs(context.Background(), client, agentconfig.Config{Credential: "device-secret"}, agentLogPath, updateLogPath)

	if err != nil {
		t.Fatalf("uploadServiceLogs returned error: %v", err)
	}
	if client.credential != "device-secret" || client.updateLog != "update tail" || !strings.Contains(client.agentLog, "agent tail") || strings.Contains(client.agentLog, "device-secret") {
		t.Fatalf("uploaded logs = %#v", client)
	}
}

func TestNetworkUsagePathUsesAgentConfigDirectory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "AceITCenter", "agent.json")
	want := filepath.Join(filepath.Dir(configPath), "network-usage.json")

	if got := networkUsagePath(configPath); got != want {
		t.Fatalf("networkUsagePath() = %q, want %q", got, want)
	}
}

func TestServiceWorkerStartsFixedUpdaterMaintenanceWithoutBlocking(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "AceITCenter", "agent.json")
	executablePath := filepath.Join(t.TempDir(), "Ace IT Center", "AceAgent.exe")
	started := make(chan update.PromotionOptions, 1)
	release := make(chan struct{})
	worker := serviceWorker{
		configPath:     configPath,
		executablePath: executablePath,
		promoteUpdater: func(_ context.Context, options update.PromotionOptions) error {
			started <- options
			<-release
			return nil
		},
	}

	returned := make(chan struct{})
	go func() {
		worker.startUpdaterMaintenance(context.Background())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("startUpdaterMaintenance blocked on promotion")
	}
	select {
	case options := <-started:
		if options.AgentVersion == "" || options.InstalledPath != filepath.Join(filepath.Dir(executablePath), "AceAgentUpdater.exe") || options.PendingPath != filepath.Join(filepath.Dir(executablePath), "AceAgentUpdater.next.exe") || options.StagingDirectory != filepath.Join(filepath.Dir(configPath), "updates") || options.CurrentProcessPath != executablePath {
			t.Fatalf("promotion options = %#v", options)
		}
	case <-time.After(time.Second):
		t.Fatal("updater maintenance did not start")
	}
	close(release)
}

func (f *fakeServiceUpdateClient) Check(_ context.Context, options update.CheckOptions) (update.CheckResult, error) {
	f.checkOptions = options
	return f.result, f.checkErr
}

func (f *fakeServiceUpdateClient) LaunchApply(_ context.Context, options update.ApplyOptions) error {
	f.applyOptions = options
	return f.launchErr
}

func TestExecuteServiceUpdateChecksAndLaunchesFixedUpdater(t *testing.T) {
	stagingDir := filepath.Join(t.TempDir(), "update staging")
	installerPath := filepath.Join(stagingDir, "setup.exe")
	client := &fakeServiceUpdateClient{result: update.CheckResult{Available: true, Version: "0.4.11", URL: "https://it.example/download/setup.exe", InstallerPath: installerPath}}
	checkOptions := update.CheckOptions{Origin: "https://it.example", CurrentVersion: "0.4.10", CurrentOS: "10.0.19045", StagingDir: stagingDir}
	finished := false
	runtime := serviceUpdateRuntime{
		client:       client,
		checkOptions: checkOptions,
		backupPath:   filepath.Join(stagingDir, "AceAgent.lkg.exe"),
		authorize: func() (func(controller.UpdateStatus, bool), error) {
			return func(status controller.UpdateStatus, launched bool) {
				finished = launched && status.Version == "0.4.11"
			}, nil
		},
	}

	status, err := executeServiceUpdate(context.Background(), runtime)

	if err != nil {
		t.Fatalf("executeServiceUpdate() error = %v", err)
	}
	if client.checkOptions != checkOptions {
		t.Fatalf("check options = %#v", client.checkOptions)
	}
	wantApply := update.ApplyOptions{InstallerPath: installerPath, BackupPath: runtime.backupPath, Version: "0.4.11"}
	if client.applyOptions != wantApply {
		t.Fatalf("apply options = %#v, want %#v", client.applyOptions, wantApply)
	}
	wantStatus := (controller.UpdateStatus{Available: true, Version: "0.4.11", URL: client.result.URL})
	if status != wantStatus {
		t.Fatalf("status = %#v, want %#v", status, wantStatus)
	}
	if !finished {
		t.Fatal("successful fixed updater launch did not finish generation authorization")
	}
}

func TestExecuteServiceUpdateReturnsUnavailableWithoutAuthorization(t *testing.T) {
	client := &fakeServiceUpdateClient{}
	authorized := false
	status, err := executeServiceUpdate(context.Background(), serviceUpdateRuntime{
		client:       client,
		checkOptions: update.CheckOptions{Origin: "https://it.example", CurrentVersion: "0.4.10", CurrentOS: "10.0.19045", StagingDir: t.TempDir()},
		backupPath:   filepath.Join(t.TempDir(), "AceAgent.lkg.exe"),
		authorize: func() (func(controller.UpdateStatus, bool), error) {
			authorized = true
			return func(controller.UpdateStatus, bool) {}, nil
		},
	})
	if err != nil || status != (controller.UpdateStatus{}) || authorized || client.applyOptions != (update.ApplyOptions{}) {
		t.Fatalf("status=%#v err=%v authorized=%t apply=%#v", status, err, authorized, client.applyOptions)
	}
}

func TestExecuteServiceUpdateLaunchFailureFinishesAuthorizationWithoutPending(t *testing.T) {
	launchErr := errors.New("CreateProcess failed")
	stagingDir := t.TempDir()
	installerPath := filepath.Join(stagingDir, "setup.exe")
	if err := os.WriteFile(installerPath, []byte("installer"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeServiceUpdateClient{result: update.CheckResult{Available: true, Version: "0.4.11", URL: "https://it.example/setup.exe", InstallerPath: installerPath}, launchErr: launchErr}
	finishedLaunch := true
	runtime := serviceUpdateRuntime{
		client:       client,
		checkOptions: update.CheckOptions{Origin: "https://it.example", CurrentVersion: "0.4.10", CurrentOS: "10.0.19045", StagingDir: stagingDir},
		backupPath:   filepath.Join(stagingDir, "AceAgent.lkg.exe"),
		authorize: func() (func(controller.UpdateStatus, bool), error) {
			return func(_ controller.UpdateStatus, launched bool) { finishedLaunch = launched }, nil
		},
	}

	_, err := executeServiceUpdate(context.Background(), runtime)

	if !errors.Is(err, launchErr) {
		t.Fatalf("executeServiceUpdate() error = %v", err)
	}
	if finishedLaunch {
		t.Fatal("failed fixed updater launch was recorded as pending")
	}
	if _, statErr := os.Stat(installerPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staged installer remains after launch failure: %v", statErr)
	}
}

func TestExecuteServiceUpdateDoesNotLaunchAfterGenerationAuthorizationFails(t *testing.T) {
	generationErr := errors.New("configuration changed")
	stagingDir := t.TempDir()
	installerPath := filepath.Join(stagingDir, "setup.exe")
	if err := os.WriteFile(installerPath, []byte("installer"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeServiceUpdateClient{result: update.CheckResult{Available: true, Version: "0.4.11", URL: "https://it.example/setup.exe", InstallerPath: installerPath}}
	runtime := serviceUpdateRuntime{
		client:       client,
		checkOptions: update.CheckOptions{Origin: "https://it.example", CurrentVersion: "0.4.10", CurrentOS: "10.0.19045", StagingDir: stagingDir},
		backupPath:   filepath.Join(stagingDir, "AceAgent.lkg.exe"),
		authorize:    func() (func(controller.UpdateStatus, bool), error) { return nil, generationErr },
	}

	_, err := executeServiceUpdate(context.Background(), runtime)

	if !errors.Is(err, generationErr) {
		t.Fatalf("executeServiceUpdate() error = %v", err)
	}
	if client.applyOptions != (update.ApplyOptions{}) {
		t.Fatal("fixed updater launched after generation authorization failed")
	}
	if _, statErr := os.Stat(installerPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staged installer remains after authorization failure: %v", statErr)
	}
}
