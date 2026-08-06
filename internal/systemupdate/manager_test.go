package systemupdate

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
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
