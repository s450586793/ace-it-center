package systemupdate

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManagerCheckForcesAndPersistsFreshResult(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
	old := managerCheckResult(now.Add(-time.Minute), "v0.3.9", "v0.4.0", true)
	if err := store.Save(PersistentState{LastCheck: &old}); err != nil {
		t.Fatal(err)
	}
	runtime := &managerRuntime{images: managerPair("v0.4.0")}
	resolver := &managerResolver{images: managerPair("v0.4.1")}
	checker := NewChecker(resolver, runtime, testBackendRepository, testWebRepository, func() time.Time { return now })
	manager, err := NewManager(ManagerOptions{Store: store, Checker: checker, Platform: &managerPlatform{}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	view, err := manager.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 2 || resolver.calls != 2 {
		t.Fatalf("forced check calls = runtime %d, resolver %d", runtime.calls, resolver.calls)
	}
	if view.Current.Backend != "v0.4.0" || view.Current.Web != "v0.4.0" || view.Latest == nil || view.Latest.Backend != "v0.4.1" || !view.UpdateAvailable || view.CheckedAt == nil || !view.CheckedAt.Equal(now) {
		t.Fatalf("Check() = %#v", view)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.LastCheck == nil || !reflect.DeepEqual(*state.LastCheck, managerCheckResult(now, "v0.4.0", "v0.4.1", true)) {
		t.Fatalf("persisted state = %#v", state)
	}
}

func TestManagerStartValidatesFreshExactAvailableCheckAndSingleTask(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		state  PersistentState
		target string
	}{
		{name: "missing check", target: "v0.4.1"},
		{name: "two minute expiry", state: PersistentState{LastCheck: managerCheckResultPointer(now.Add(-2*time.Minute), "v0.4.0", "v0.4.1", true)}, target: "v0.4.1"},
		{name: "different target", state: PersistentState{LastCheck: managerCheckResultPointer(now, "v0.4.0", "v0.4.1", true)}, target: "v0.4.2"},
		{name: "no update", state: PersistentState{LastCheck: managerCheckResultPointer(now, "v0.4.1", "v0.4.1", false)}, target: "v0.4.1"},
		{name: "active task", state: PersistentState{LastCheck: managerCheckResultPointer(now, "v0.4.0", "v0.4.1", true), Task: &Task{ID: "active", Stage: StagePulling}}, target: "v0.4.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
			if err := store.Save(test.state); err != nil {
				t.Fatal(err)
			}
			launched := false
			manager, err := NewManager(ManagerOptions{
				Store: store, Checker: newTestChecker(managerPair("v0.4.0"), managerPair("v0.4.1"), func() time.Time { return now }),
				Platform: &managerPlatform{}, Now: func() time.Time { return now },
				Launch: func(func()) { launched = true },
			})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := manager.Start(context.Background(), test.target); err == nil {
				t.Fatal("Start() accepted invalid state")
			}
			if launched {
				t.Fatal("Start() launched an invalid task")
			}
		})
	}
}

func TestManagerSuccessfulUpgradeUsesDetachedContextAndRequiredOrder(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
	check := managerCheckResult(now, "v0.4.0", "v0.4.1", true)
	if err := store.Save(PersistentState{LastCheck: &check}); err != nil {
		t.Fatal(err)
	}
	root := context.WithValue(context.Background(), managerContextKey{}, "root")
	platform := &managerPlatform{
		store:       store,
		rootContext: root,
		images:      managerPair("v0.4.0"),
		expectedStages: []Stage{
			StageChecking, StageChecking,
			StageBackingUp, StageBackingUp, StageBackingUp,
			StagePulling, StagePulling,
			StageSwitchingBackend, StageCheckingBackend,
			StageSwitchingWeb, StageCheckingWeb,
			StageStabilizing, StageStabilizing,
			StageCleaning, StageCleaning,
		},
	}
	manager, err := NewManager(ManagerOptions{
		Store: store, Checker: newTestChecker(managerPair("v0.4.0"), managerPair("v0.4.1"), func() time.Time { return now }),
		Platform: platform, Now: func() time.Time { return now }, NewID: func() string { return "123e4567-e89b-12d3-a456-426614174000" },
		Launch: func(job func()) { job() }, RootContext: root, StableWindow: time.Nanosecond, StableInterval: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()

	view, err := manager.Start(requestContext, "v0.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Stage != StageSucceeded || view.Cleanup != CleanupComplete || view.StartedAt == nil || view.FinishedAt == nil {
		t.Fatalf("view=%#v", view)
	}
	wantCalls := []string{
		"inspect:backend", "inspect:web",
		"alias:backend", "alias:web", "backup",
		"pull:backend", "pull:web",
		"deploy:backend", "health:backend",
		"deploy:web", "health:web",
		"health:backend", "health:web",
		"clean:backend", "clean:web",
	}
	if !reflect.DeepEqual(platform.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", platform.calls, wantCalls)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Task == nil || state.Task.Stage != StageSucceeded || state.Task.Cleanup != CleanupComplete || state.LastCheck == nil || state.LastCheck.Current.Backend.Version != "v0.4.1" || state.LastCheck.Current.Web.Version != "v0.4.1" {
		t.Fatalf("final state = %#v", state)
	}
	status := manager.Status()
	if status.Task == nil || status.Task.Stage != StageSucceeded || status.Current.Backend != "v0.4.1" || status.UpdateAvailable {
		t.Fatalf("Status() = %#v", status)
	}
}

func TestManagerSuccessfulUpgradeReportsCleanupPendingWithoutInternalDetails(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
	check := managerCheckResult(now, "v0.4.0", "v0.4.1", true)
	if err := store.Save(PersistentState{LastCheck: &check}); err != nil {
		t.Fatal(err)
	}
	platform := &managerPlatform{
		images:       managerPair("v0.4.0"),
		cleanupError: map[ServiceName]error{ServiceBackend: errors.New("cleanup pending: daemon secret output")},
	}
	manager, err := NewManager(ManagerOptions{
		Store: store, Checker: newTestChecker(managerPair("v0.4.0"), managerPair("v0.4.1"), func() time.Time { return now }),
		Platform: platform, Now: func() time.Time { return now }, NewID: func() string { return "123e4567-e89b-12d3-a456-426614174000" },
		Launch: func(job func()) { job() }, StableWindow: time.Nanosecond, StableInterval: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := manager.Start(context.Background(), "v0.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Stage != StageSucceeded || view.Cleanup != CleanupPending || view.ErrorCode != "cleanup_pending" || view.ErrorMessage != "升级成功，旧镜像仍被引用，需在 DSM 中处理" {
		t.Fatalf("view = %#v", view)
	}
	if strings.Contains(view.ErrorMessage, "secret") || !reflect.DeepEqual(platform.calls[len(platform.calls)-2:], []string{"clean:backend", "clean:web"}) {
		t.Fatalf("unsafe view or incomplete cleanup attempts: view=%#v calls=%v", view, platform.calls)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Task == nil || state.Task.ErrorMessage != "" {
		t.Fatalf("persisted cleanup result exposed internal detail: %#v", state.Task)
	}
}

func TestManagerSuccessfulUpgradePreservesNewerConcurrentCheck(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
	check := managerCheckResult(now, "v0.4.0", "v0.4.1", true)
	if err := store.Save(PersistentState{LastCheck: &check}); err != nil {
		t.Fatal(err)
	}
	beforeFinish := make(chan struct{})
	allowFinish := make(chan struct{})
	platform := &managerPlatform{
		images:       managerPair("v0.4.0"),
		beforeFinish: beforeFinish,
		allowFinish:  allowFinish,
	}
	checker := newTestChecker(managerPair("v0.4.1"), managerPair("v0.4.2"), func() time.Time { return now })
	manager, err := NewManager(ManagerOptions{
		Store: store, Checker: checker, Platform: platform, Now: func() time.Time { return now },
		NewID:  func() string { return "123e4567-e89b-12d3-a456-426614174000" },
		Launch: func(job func()) { job() }, StableWindow: time.Nanosecond, StableInterval: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	type startResult struct {
		view TaskView
		err  error
	}
	started := make(chan startResult, 1)
	go func() {
		view, startErr := manager.Start(context.Background(), "v0.4.1")
		started <- startResult{view: view, err: startErr}
	}()

	<-beforeFinish
	if _, err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(allowFinish)
	result := <-started
	if result.err != nil {
		t.Fatal(result.err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if result.view.Stage != StageSucceeded || state.Task == nil || state.Task.Stage != StageSucceeded || state.LastCheck == nil {
		t.Fatalf("result = %#v, state = %#v", result, state)
	}
	if state.LastCheck.Current.Backend.Version != "v0.4.1" || state.LastCheck.Current.Web.Version != "v0.4.1" || state.LastCheck.Target.Backend.Version != "v0.4.2" || state.LastCheck.Target.Web.Version != "v0.4.2" || !state.LastCheck.Available {
		t.Fatalf("last check = %#v", state.LastCheck)
	}
}

func TestManagerSuccessfulUpgradeClearsConcurrentSameVersionRepublish(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
	check := managerCheckResult(now, "v0.4.0", "v0.4.1", true)
	if err := store.Save(PersistentState{LastCheck: &check}); err != nil {
		t.Fatal(err)
	}
	beforeFinish := make(chan struct{})
	allowFinish := make(chan struct{})
	platform := &managerPlatform{
		images:       managerPair("v0.4.0"),
		beforeFinish: beforeFinish,
		allowFinish:  allowFinish,
	}
	republished := managerPair("v0.4.1")
	republished.Backend.Digest = "sha256:republished-backend"
	republished.Web.Digest = "sha256:republished-web"
	checker := newTestChecker(managerPair("v0.4.0"), republished, func() time.Time { return now })
	manager, err := NewManager(ManagerOptions{
		Store: store, Checker: checker, Platform: platform, Now: func() time.Time { return now },
		NewID:  func() string { return "123e4567-e89b-12d3-a456-426614174000" },
		Launch: func(job func()) { job() }, StableWindow: time.Nanosecond, StableInterval: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	type startResult struct {
		view TaskView
		err  error
	}
	started := make(chan startResult, 1)
	go func() {
		view, startErr := manager.Start(context.Background(), "v0.4.1")
		started <- startResult{view: view, err: startErr}
	}()

	<-beforeFinish
	if _, err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(allowFinish)
	result := <-started
	if result.err != nil {
		t.Fatal(result.err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if result.view.Stage != StageSucceeded || state.Task == nil || state.Task.Stage != StageSucceeded || state.LastCheck == nil {
		t.Fatalf("result = %#v, state = %#v", result, state)
	}
	if state.LastCheck.Current.Backend.Digest != "sha256:backend-v0.4.1" || state.LastCheck.Current.Web.Digest != "sha256:web-v0.4.1" || state.LastCheck.Target.Backend.Digest != "sha256:republished-backend" || state.LastCheck.Target.Web.Digest != "sha256:republished-web" || state.LastCheck.Available {
		t.Fatalf("last check = %#v", state.LastCheck)
	}
}

func TestManagerFailureMatrixRollsBackOnlyAfterBackendSwitch(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		failureKey string
		code       string
		rolledBack bool
	}{
		{name: "backend alias", failureKey: "alias:backend#1", code: "backup_failed"},
		{name: "web alias", failureKey: "alias:web#1", code: "backup_failed"},
		{name: "database backup", failureKey: "backup#1", code: "backup_failed"},
		{name: "backend pull", failureKey: "pull:backend#1", code: "pull_failed"},
		{name: "web pull", failureKey: "pull:web#1", code: "pull_failed"},
		{name: "backend switch", failureKey: "deploy:backend#1", code: "backend_switch_failed", rolledBack: true},
		{name: "backend health", failureKey: "health:backend#1", code: "backend_unhealthy", rolledBack: true},
		{name: "web switch", failureKey: "deploy:web#1", code: "web_switch_failed", rolledBack: true},
		{name: "web health", failureKey: "health:web#1", code: "web_unhealthy", rolledBack: true},
		{name: "stability", failureKey: "health:backend#2", code: "stability_failed", rolledBack: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
			check := managerCheckResult(now, "v0.4.0", "v0.4.1", true)
			if err := store.Save(PersistentState{LastCheck: &check}); err != nil {
				t.Fatal(err)
			}
			platform := &failurePlatform{
				images:   managerPair("v0.4.0"),
				failures: map[string]error{test.failureKey: errors.New("runner output TOKEN=private PGPASSWORD=private")},
			}
			manager, err := NewManager(ManagerOptions{
				Store: store, Checker: newTestChecker(managerPair("v0.4.0"), managerPair("v0.4.1"), func() time.Time { return now }),
				Platform: platform, Now: func() time.Time { return now }, NewID: func() string { return "123e4567-e89b-12d3-a456-426614174000" },
				Launch: func(job func()) { job() }, StableWindow: time.Nanosecond, StableInterval: time.Nanosecond,
			})
			if err != nil {
				t.Fatal(err)
			}

			view, err := manager.Start(context.Background(), "v0.4.1")
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if state.Task == nil || state.Task.Stage != StageFailed || state.Task.ErrorCode != test.code || state.Task.RolledBack != test.rolledBack || state.Task.ErrorMessage != "" {
				t.Fatalf("persisted failure = %#v", state.Task)
			}
			if view.Stage != StageFailed || view.ErrorCode != test.code || strings.Contains(view.ErrorMessage, "private") {
				t.Fatalf("public failure = %#v", view)
			}
			if hasManagerCall(platform.calls, "clean:backend") || hasManagerCall(platform.calls, "clean:web") {
				t.Fatalf("failed transaction performed cleanup: %v", platform.calls)
			}
			rollbackCalls := []string{"rollback:backend", "health:backend", "rollback:web", "health:web"}
			if test.rolledBack && !reflect.DeepEqual(platform.calls[len(platform.calls)-4:], rollbackCalls) {
				t.Fatalf("rollback calls = %v, want suffix %v", platform.calls, rollbackCalls)
			}
			if !test.rolledBack && (hasManagerCall(platform.calls, "rollback:backend") || hasManagerCall(platform.calls, "rollback:web")) {
				t.Fatalf("pre-switch failure rolled back: %v", platform.calls)
			}
		})
	}
}

func TestManagerRollbackFailureRequiresManualIntervention(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
	check := managerCheckResult(now, "v0.4.0", "v0.4.1", true)
	if err := store.Save(PersistentState{LastCheck: &check}); err != nil {
		t.Fatal(err)
	}
	platform := &failurePlatform{
		images: managerPair("v0.4.0"),
		failures: map[string]error{
			"health:backend#1":   errors.New("backend unhealthy"),
			"rollback:backend#1": errors.New("rollback output TOKEN=private"),
		},
	}
	manager, err := NewManager(ManagerOptions{
		Store: store, Checker: newTestChecker(managerPair("v0.4.0"), managerPair("v0.4.1"), func() time.Time { return now }),
		Platform: platform, Now: func() time.Time { return now }, NewID: func() string { return "123e4567-e89b-12d3-a456-426614174000" },
		Launch: func(job func()) { job() }, StableWindow: time.Nanosecond, StableInterval: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Start(context.Background(), "v0.4.1"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Task == nil || state.Task.Stage != StageManualIntervention || state.Task.ErrorCode != "rollback_failed" || state.Task.RolledBack {
		t.Fatalf("manual intervention state = %#v", state.Task)
	}
	if hasManagerCall(platform.calls, "clean:backend") || hasManagerCall(platform.calls, "clean:web") {
		t.Fatalf("rollback failure performed cleanup: %v", platform.calls)
	}
	if _, err := manager.Start(context.Background(), "v0.4.1"); err == nil {
		t.Fatal("Start() accepted a manual intervention task")
	}
	if _, err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Task == nil || state.Task.Stage != StageManualIntervention {
		t.Fatalf("Check() replaced manual intervention task: %#v", state.Task)
	}
}

func TestManagerFailureLogsOnlySafeClassification(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		detail string
		leaks  []string
	}{
		{name: "docker daemon detail", detail: "docker daemon error: image internal sha256:private-image", leaks: []string{"docker daemon", "private-image"}},
		{name: "stack detail", detail: "goroutine 42 [running]:\ninternal/platform.(*runner).Run(...)", leaks: []string{"goroutine 42", "internal/platform"}},
		{name: "unknown environment detail", detail: "opaque_config: private-value", leaks: []string{"opaque_config", "private-value"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
			check := managerCheckResult(now, "v0.4.0", "v0.4.1", true)
			if err := store.Save(PersistentState{LastCheck: &check}); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			manager, err := NewManager(ManagerOptions{
				Store: store, Checker: newTestChecker(managerPair("v0.4.0"), managerPair("v0.4.1"), func() time.Time { return now }),
				Platform: &failurePlatform{images: managerPair("v0.4.0"), failures: map[string]error{
					"backup#1": errors.New(test.detail),
				}},
				Now: func() time.Time { return now }, NewID: func() string { return "123e4567-e89b-12d3-a456-426614174000" },
				Launch: func(job func()) { job() }, Logger: slog.New(slog.NewTextHandler(&logs, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := manager.Start(context.Background(), "v0.4.1"); err != nil {
				t.Fatal(err)
			}
			output := logs.String()
			if !strings.Contains(output, "stage=backing_up") || !strings.Contains(output, "error_code=backup_failed") {
				t.Fatalf("logger omitted safe classification: %q", output)
			}
			for _, leak := range test.leaks {
				if strings.Contains(output, leak) {
					t.Fatalf("logger exposed %q: %q", leak, output)
				}
			}
		})
	}
}

func TestManagerRecoverUsesPersistedStageAndActualServices(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		stage          Stage
		images         ImagePair
		withoutAlias   bool
		withoutDigest  bool
		failures       map[string]error
		wantStage      Stage
		wantCode       string
		wantRolledBack bool
		wantCalls      []string
	}{
		{name: "terminal", stage: StageSucceeded, images: managerPair("v0.4.1"), wantStage: StageSucceeded},
		{name: "checking with originals", stage: StageChecking, images: managerPair("v0.4.0"), wantStage: StageFailed, wantCode: "updater_restarted", wantCalls: []string{"inspect:backend", "inspect:web"}},
		{name: "backing up with originals", stage: StageBackingUp, images: managerPair("v0.4.0"), wantStage: StageFailed, wantCode: "updater_restarted", wantCalls: []string{"inspect:backend", "inspect:web"}},
		{name: "pulling with originals", stage: StagePulling, images: managerPair("v0.4.0"), wantStage: StageFailed, wantCode: "updater_restarted", wantCalls: []string{"inspect:backend", "inspect:web"}},
		{name: "switching backend", stage: StageSwitchingBackend, images: managerPair("v0.4.1"), wantStage: StageFailed, wantCode: "updater_restarted", wantRolledBack: true, wantCalls: []string{"inspect:backend", "inspect:web", "rollback:backend", "health:backend", "rollback:web", "health:web"}},
		{name: "checking web", stage: StageCheckingWeb, images: managerPair("v0.4.1"), wantStage: StageFailed, wantCode: "updater_restarted", wantRolledBack: true, wantCalls: []string{"inspect:backend", "inspect:web", "rollback:backend", "health:backend", "rollback:web", "health:web"}},
		{name: "stabilizing", stage: StageStabilizing, images: managerPair("v0.4.1"), wantStage: StageFailed, wantCode: "updater_restarted", wantRolledBack: true, wantCalls: []string{"inspect:backend", "inspect:web", "rollback:backend", "health:backend", "rollback:web", "health:web"}},
		{name: "cleaning targets healthy", stage: StageCleaning, images: managerPair("v0.4.1"), wantStage: StageSucceeded, wantCalls: []string{"inspect:backend", "inspect:web", "health:backend", "health:web", "clean:backend", "clean:web"}},
		{name: "missing aliases", stage: StageSwitchingBackend, images: managerPair("v0.4.1"), withoutAlias: true, wantStage: StageManualIntervention, wantCode: "state_invalid", wantCalls: []string{"inspect:backend", "inspect:web"}},
		{name: "cleaning missing aliases", stage: StageCleaning, images: managerPair("v0.4.1"), withoutAlias: true, wantStage: StageManualIntervention, wantCode: "state_invalid", wantCalls: []string{"inspect:backend", "inspect:web"}},
		{name: "cleaning missing digest", stage: StageCleaning, images: managerPair("v0.4.1"), withoutDigest: true, wantStage: StageManualIntervention, wantCode: "state_invalid", wantCalls: []string{"inspect:backend", "inspect:web"}},
		{name: "rollback health failure", stage: StageCheckingBackend, images: managerPair("v0.4.1"), failures: map[string]error{"health:backend#1": errors.New("recovery health failed")}, wantStage: StageManualIntervention, wantCode: "rollback_failed", wantCalls: []string{"inspect:backend", "inspect:web", "rollback:backend", "health:backend", "rollback:web", "health:web"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
			task := managerRecoveryTask(now, test.stage)
			if test.withoutAlias {
				task.Original.Backend.RollbackAlias = ""
			}
			if test.withoutDigest {
				task.Original.Backend.Digest = ""
			}
			check := managerCheckResult(now, "v0.4.0", "v0.4.1", true)
			if err := store.Save(PersistentState{LastCheck: &check, Task: &task}); err != nil {
				t.Fatal(err)
			}
			platform := &failurePlatform{images: test.images, failures: test.failures}
			manager, err := NewManager(ManagerOptions{
				Store: store, Checker: newTestChecker(managerPair("v0.4.0"), managerPair("v0.4.1"), func() time.Time { return now }),
				Platform: platform, Now: func() time.Time { return now }, RootContext: context.Background(),
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := manager.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if state.Task == nil || state.Task.Stage != test.wantStage || state.Task.ErrorCode != test.wantCode || state.Task.RolledBack != test.wantRolledBack {
				t.Fatalf("recovered task = %#v", state.Task)
			}
			if !reflect.DeepEqual(platform.calls, test.wantCalls) {
				t.Fatalf("recovery calls = %v, want %v", platform.calls, test.wantCalls)
			}
			for _, forbidden := range []string{"alias:backend", "alias:web", "backup", "pull:backend", "pull:web", "deploy:backend", "deploy:web"} {
				if hasManagerCall(platform.calls, forbidden) {
					t.Fatalf("Recover() repeated %s: %v", forbidden, platform.calls)
				}
			}
		})
	}
}

func managerRecoveryTask(now time.Time, stage Stage) Task {
	original := managerPair("v0.4.0")
	original.Backend.RollbackAlias = "ace-it-center-rollback-backend:123e4567-e89b-12d3-a456-426614174000"
	original.Web.RollbackAlias = "ace-it-center-rollback-web:123e4567-e89b-12d3-a456-426614174000"
	return Task{
		ID: "123e4567-e89b-12d3-a456-426614174000", Original: original, Target: managerPair("v0.4.1"), Stage: stage,
		CreatedAt: now, StartedAt: timePointer(now), Cleanup: CleanupNotRun,
	}
}

func hasManagerCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

type managerContextKey struct{}

type managerResolver struct {
	images ImagePair
	calls  int
}

func (resolver *managerResolver) Resolve(_ context.Context, repository, tag string) (Image, error) {
	resolver.calls++
	if tag != stableTag {
		return Image{}, errors.New("unexpected tag")
	}
	switch repository {
	case testBackendRepository:
		return resolver.images.Backend, nil
	case testWebRepository:
		return resolver.images.Web, nil
	default:
		return Image{}, errors.New("unexpected repository")
	}
}

type managerRuntime struct {
	images ImagePair
	calls  int
}

func (runtime *managerRuntime) InspectService(_ context.Context, service ServiceName) (Image, error) {
	runtime.calls++
	if service == ServiceBackend {
		return runtime.images.Backend, nil
	}
	if service == ServiceWeb {
		return runtime.images.Web, nil
	}
	return Image{}, errors.New("unexpected service")
}

type managerPlatform struct {
	store          *FileStore
	rootContext    context.Context
	images         ImagePair
	calls          []string
	expectedStages []Stage
	cleanupError   map[ServiceName]error
	beforeFinish   chan struct{}
	allowFinish    chan struct{}
}

type failurePlatform struct {
	images   ImagePair
	failures map[string]error
	calls    []string
	counts   map[string]int
}

func (platform *failurePlatform) InspectService(_ context.Context, service ServiceName) (Image, error) {
	if err := platform.record("inspect:" + string(service)); err != nil {
		return Image{}, err
	}
	if service == ServiceBackend {
		return platform.images.Backend, nil
	}
	return platform.images.Web, nil
}

func (platform *failurePlatform) CreateRollbackAlias(_ context.Context, service ServiceName, image Image, taskID string) (Image, error) {
	if err := platform.record("alias:" + string(service)); err != nil {
		return Image{}, err
	}
	image.RollbackAlias = "ace-it-center-rollback-" + string(service) + ":" + strings.ToLower(taskID)
	return image, nil
}

func (platform *failurePlatform) BackupDatabase(_ context.Context, _ string) (string, error) {
	if err := platform.record("backup"); err != nil {
		return "", err
	}
	return "/private/upgrade.dump", nil
}

func (platform *failurePlatform) PullTarget(_ context.Context, service ServiceName, _ Image) error {
	return platform.record("pull:" + string(service))
}

func (platform *failurePlatform) DeployTarget(_ context.Context, service ServiceName, _ ImagePair, _ string) error {
	return platform.record("deploy:" + string(service))
}

func (platform *failurePlatform) DeployRollback(_ context.Context, service ServiceName, pair ImagePair, _ string) error {
	if pair.Backend.RollbackAlias == "" || pair.Web.RollbackAlias == "" {
		return errors.New("rollback aliases were not provided")
	}
	return platform.record("rollback:" + string(service))
}

func (platform *failurePlatform) WaitHealthy(_ context.Context, service ServiceName) error {
	return platform.record("health:" + string(service))
}

func (platform *failurePlatform) RemoveOldImage(_ context.Context, service ServiceName, _ Image) error {
	return platform.record("clean:" + string(service))
}

func (platform *failurePlatform) record(call string) error {
	if platform.counts == nil {
		platform.counts = make(map[string]int)
	}
	platform.counts[call]++
	platform.calls = append(platform.calls, call)
	if err := platform.failures[call+"#"+strconv.Itoa(platform.counts[call])]; err != nil {
		return err
	}
	return platform.failures[call]
}

func (platform *managerPlatform) InspectService(ctx context.Context, service ServiceName) (Image, error) {
	if err := platform.record(ctx, "inspect:"+string(service)); err != nil {
		return Image{}, err
	}
	if service == ServiceBackend {
		return platform.images.Backend, nil
	}
	return platform.images.Web, nil
}

func (platform *managerPlatform) CreateRollbackAlias(ctx context.Context, service ServiceName, old Image, taskID string) (Image, error) {
	if err := platform.record(ctx, "alias:"+string(service)); err != nil {
		return Image{}, err
	}
	old.RollbackAlias = "ace-it-center-rollback-" + string(service) + ":" + strings.ToLower(taskID)
	return old, nil
}

func (platform *managerPlatform) BackupDatabase(ctx context.Context, _ string) (string, error) {
	if err := platform.record(ctx, "backup"); err != nil {
		return "", err
	}
	return "/private/upgrade.dump", nil
}

func (platform *managerPlatform) PullTarget(ctx context.Context, service ServiceName, _ Image) error {
	return platform.record(ctx, "pull:"+string(service))
}

func (platform *managerPlatform) DeployTarget(ctx context.Context, service ServiceName, _ ImagePair, _ string) error {
	return platform.record(ctx, "deploy:"+string(service))
}

func (platform *managerPlatform) DeployRollback(context.Context, ServiceName, ImagePair, string) error {
	return errors.New("unexpected rollback")
}

func (platform *managerPlatform) WaitHealthy(ctx context.Context, service ServiceName) error {
	return platform.record(ctx, "health:"+string(service))
}

func (platform *managerPlatform) RemoveOldImage(ctx context.Context, service ServiceName, _ Image) error {
	if err := platform.record(ctx, "clean:"+string(service)); err != nil {
		return err
	}
	if service == ServiceWeb && platform.beforeFinish != nil {
		close(platform.beforeFinish)
		<-platform.allowFinish
	}
	return platform.cleanupError[service]
}

func (platform *managerPlatform) record(ctx context.Context, call string) error {
	if platform.rootContext != nil && ctx != platform.rootContext {
		return errors.New("job did not use root context")
	}
	if platform.store != nil {
		state, err := platform.store.Load()
		if err != nil {
			return err
		}
		if state.Task == nil || len(platform.calls) >= len(platform.expectedStages) || state.Task.Stage != platform.expectedStages[len(platform.calls)] {
			return errors.New("external action observed unexpected persisted stage")
		}
	}
	platform.calls = append(platform.calls, call)
	return nil
}

func managerPair(version string) ImagePair {
	return ImagePair{
		Backend: Image{Repository: testBackendRepository, Version: version, Digest: "sha256:backend-" + version, ID: "sha256:backend-id-" + version},
		Web:     Image{Repository: testWebRepository, Version: version, Digest: "sha256:web-" + version, ID: "sha256:web-id-" + version},
	}
}

func managerCheckResult(checkedAt time.Time, current, target string, available bool) CheckResult {
	return CheckResult{Current: managerPair(current), Target: managerPair(target), Available: available, CheckedAt: checkedAt}
}

func managerCheckResultPointer(checkedAt time.Time, current, target string, available bool) *CheckResult {
	result := managerCheckResult(checkedAt, current, target, available)
	return &result
}
