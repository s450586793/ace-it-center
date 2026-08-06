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

type fakeServiceUpdateChecker struct {
	candidate update.Candidate
	staged    update.StagedUpdate
	checked   bool
	stagedArg update.Candidate
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

func (f *fakeServiceUpdateChecker) Check(context.Context) (update.Candidate, error) {
	f.checked = true
	return f.candidate, nil
}

func (f *fakeServiceUpdateChecker) Stage(_ context.Context, candidate update.Candidate) (update.StagedUpdate, error) {
	f.stagedArg = candidate
	return f.staged, nil
}

func TestUpdateHelperModeDoesNotAttachConsole(t *testing.T) {
	if shouldAttachConsole("windows", []string{"update-helper", "--installer", `C:\ProgramData\setup.exe`}) {
		t.Fatal("update-helper attempted to attach a console")
	}
}

func TestParseUpdateHelperOptionsPreservesPathsWithSpaces(t *testing.T) {
	arguments := []string{
		"--installer", `C:\ProgramData\Ace IT Center\setup.exe`,
		"--executable", `C:\Program Files\Ace IT Center\AceAgent.exe`,
		"--backup", `C:\ProgramData\Ace IT Center\AceAgent.lkg.exe`,
		"--version", "0.2.0",
	}

	options, err := parseUpdateHelperOptions(arguments)

	if err != nil {
		t.Fatalf("parseUpdateHelperOptions() error = %v", err)
	}
	if options.InstallerPath != arguments[1] || options.ExecutablePath != arguments[3] || options.BackupPath != arguments[5] || options.Version != "0.2.0" {
		t.Fatalf("options = %#v", options)
	}
}

func TestParseUpdateHelperOptionsRejectsMissingAndUnknownArguments(t *testing.T) {
	for _, arguments := range [][]string{
		{"--installer", `C:\setup.exe`},
		{"--credential", "secret"},
		{"--installer", `C:\setup.exe`, "extra"},
	} {
		if _, err := parseUpdateHelperOptions(arguments); err == nil {
			t.Fatalf("arguments %v were accepted", arguments)
		}
	}
}

func TestConfigureUpdateHelperUsesTrustedConfigDirectoryAndWarningSink(t *testing.T) {
	options := update.HelperOptions{InstallerPath: "/untrusted/setup.exe"}
	warningCalled := false
	warning := func(error) { warningCalled = true }

	configured := configureUpdateHelperOptions(options, "/ProgramData/AceITCenter/agent.json", warning)

	if configured.StagingDir != "/ProgramData/AceITCenter/updates" {
		t.Fatalf("staging directory = %q", configured.StagingDir)
	}
	if configured.CleanupWarning == nil {
		t.Fatal("cleanup warning sink was not configured")
	}
	configured.CleanupWarning(errors.New("cleanup"))
	if !warningCalled {
		t.Fatal("cleanup warning sink was not invoked")
	}
}

func TestRunUpdateHelperWithRunnerWritesSafeFailureAudit(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	configPath := filepath.Join(t.TempDir(), "AceITCenter", "agent.json")
	arguments := []string{
		"--installer", filepath.Join(t.TempDir(), "AceAgentSetup.exe"),
		"--executable", filepath.Join(t.TempDir(), "AceAgent.exe"),
		"--backup", filepath.Join(t.TempDir(), "AceAgent.lkg.exe"),
		"--version", "0.3.0",
	}
	updateErr := errors.New("run silent installer: authorization=secret")

	err := runUpdateHelperWithRunner(context.Background(), arguments, configPath, logger, func(context.Context, update.HelperOptions) error {
		return updateErr
	})

	if !errors.Is(err, updateErr) {
		t.Fatalf("runUpdateHelperWithRunner() error = %v, want %v", err, updateErr)
	}
	got := output.String()
	if !strings.Contains(got, "update helper started") || !strings.Contains(got, "update helper failed") || !strings.Contains(got, `"stage":"installer"`) {
		t.Fatalf("audit log = %q", got)
	}
	if strings.Contains(got, "authorization=secret") {
		t.Fatalf("audit log leaked update error details: %q", got)
	}
}

func TestUpdateFailureStageKeepsPrimaryInstallerFailureWhenRollbackRestoreFails(t *testing.T) {
	err := errors.Join(
		errors.New("run silent installer: exit status 16"),
		errors.New("restore last-known-good Agent: sharing violation"),
	)

	if got := updateFailureStage(err); got != "installer" {
		t.Fatalf("updateFailureStage() = %q, want installer", got)
	}
}

func TestUpdateFailureStageIdentifiesTrayStopFailure(t *testing.T) {
	err := errors.New("stop Agent tray: tray did not exit")

	if got := updateFailureStage(err); got != "stop_tray" {
		t.Fatalf("updateFailureStage() = %q, want stop_tray", got)
	}
}

func TestRunUpdateHelperFailureAuditIncludesRollbackStageWithoutErrorDetails(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	arguments := []string{
		"--installer", filepath.Join(t.TempDir(), "AceAgentSetup.exe"),
		"--executable", filepath.Join(t.TempDir(), "AceAgent.exe"),
		"--backup", filepath.Join(t.TempDir(), "AceAgent.lkg.exe"),
		"--version", "0.4.2",
	}
	updateErr := errors.Join(
		errors.New("run silent installer: exit status 16"),
		errors.New("restore last-known-good Agent: path=private-machine-name"),
	)

	_ = runUpdateHelperWithRunner(context.Background(), arguments, filepath.Join(t.TempDir(), "agent.json"), logger, func(context.Context, update.HelperOptions) error {
		return updateErr
	})

	got := output.String()
	if !strings.Contains(got, `"stage":"installer"`) || !strings.Contains(got, `"recovery_stage":"restore"`) {
		t.Fatalf("audit log = %q", got)
	}
	if strings.Contains(got, "private-machine-name") {
		t.Fatalf("audit log leaked failure details: %q", got)
	}
}

func TestExecuteServiceUpdateStagesAndLaunchesTemporaryHelper(t *testing.T) {
	stagingDir := filepath.Join(t.TempDir(), "update staging")
	candidate := update.Candidate{Manifest: update.Manifest{Version: "0.2.0"}, InstallerURL: "https://it.example/download/setup.exe"}
	staged := update.StagedUpdate{Version: "0.2.0", InstallerPath: filepath.Join(stagingDir, "setup.exe"), Manifest: candidate.Manifest}
	checker := &fakeServiceUpdateChecker{candidate: candidate, staged: staged}
	var launched update.LaunchOptions
	finished := false
	runtime := serviceUpdateRuntime{
		checker:        checker,
		executablePath: filepath.Join(t.TempDir(), "Program Files", "AceAgent.exe"),
		stagingDir:     stagingDir,
		launch: func(_ context.Context, options update.LaunchOptions) error {
			launched = options
			return nil
		},
		authorize: func() (func(controller.UpdateStatus, bool), error) {
			return func(status controller.UpdateStatus, launched bool) {
				finished = launched && status.Version == "0.2.0"
			}, nil
		},
	}

	status, err := executeServiceUpdate(context.Background(), runtime)

	if err != nil {
		t.Fatalf("executeServiceUpdate() error = %v", err)
	}
	if !checker.checked || checker.stagedArg != candidate {
		t.Fatalf("checker = %#v", checker)
	}
	wantBackup := filepath.Join(stagingDir, "AceAgent.lkg.exe")
	if launched.ExecutablePath != runtime.executablePath || launched.InstallerPath != staged.InstallerPath || launched.BackupPath != wantBackup || launched.StagingDir != stagingDir || launched.Version != "0.2.0" {
		t.Fatalf("launch options = %#v", launched)
	}
	wantStatus := (controller.UpdateStatus{Available: true, Version: "0.2.0", URL: candidate.InstallerURL})
	if status != wantStatus {
		t.Fatalf("status = %#v, want %#v", status, wantStatus)
	}
	if !finished {
		t.Fatal("successful helper launch did not finish generation authorization")
	}
}

func TestExecuteServiceUpdateLaunchFailureFinishesAuthorizationWithoutPending(t *testing.T) {
	launchErr := errors.New("CreateProcess failed")
	stagingDir := t.TempDir()
	candidate := update.Candidate{Manifest: update.Manifest{Version: "0.2.0"}, InstallerURL: "https://it.example/setup.exe"}
	checker := &fakeServiceUpdateChecker{
		candidate: candidate,
		staged:    update.StagedUpdate{Version: "0.2.0", InstallerPath: filepath.Join(stagingDir, "setup.exe")},
	}
	finishedLaunch := true
	runtime := serviceUpdateRuntime{
		checker:        checker,
		executablePath: filepath.Join(t.TempDir(), "AceAgent.exe"),
		stagingDir:     stagingDir,
		launch:         func(context.Context, update.LaunchOptions) error { return launchErr },
		authorize: func() (func(controller.UpdateStatus, bool), error) {
			return func(_ controller.UpdateStatus, launched bool) { finishedLaunch = launched }, nil
		},
	}

	_, err := executeServiceUpdate(context.Background(), runtime)

	if !errors.Is(err, launchErr) {
		t.Fatalf("executeServiceUpdate() error = %v", err)
	}
	if finishedLaunch {
		t.Fatal("failed helper launch was recorded as pending")
	}
}

func TestExecuteServiceUpdateDoesNotLaunchAfterGenerationAuthorizationFails(t *testing.T) {
	generationErr := errors.New("configuration changed")
	stagingDir := t.TempDir()
	candidate := update.Candidate{Manifest: update.Manifest{Version: "0.2.0"}}
	checker := &fakeServiceUpdateChecker{
		candidate: candidate,
		staged:    update.StagedUpdate{Version: "0.2.0", InstallerPath: filepath.Join(stagingDir, "setup.exe")},
	}
	launched := false
	runtime := serviceUpdateRuntime{
		checker:        checker,
		executablePath: filepath.Join(t.TempDir(), "AceAgent.exe"),
		stagingDir:     stagingDir,
		launch: func(context.Context, update.LaunchOptions) error {
			launched = true
			return nil
		},
		authorize: func() (func(controller.UpdateStatus, bool), error) { return nil, generationErr },
	}

	_, err := executeServiceUpdate(context.Background(), runtime)

	if !errors.Is(err, generationErr) {
		t.Fatalf("executeServiceUpdate() error = %v", err)
	}
	if launched {
		t.Fatal("helper launched after generation authorization failed")
	}
}
