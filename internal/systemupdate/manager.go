package systemupdate

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/mod/semver"
)

const (
	defaultCheckTTL       = 2 * time.Minute
	defaultStableWindow   = 30 * time.Second
	defaultStableInterval = 2 * time.Second
	cleanupPendingMessage = "升级成功，旧镜像仍被引用，需在 DSM 中处理"
)

type ManagerOptions struct {
	Store          *FileStore
	Checker        *Checker
	Platform       Platform
	Now            func() time.Time
	NewID          func() string
	Launch         func(func())
	RootContext    context.Context
	CheckTTL       time.Duration
	StableWindow   time.Duration
	StableInterval time.Duration
	Logger         *slog.Logger
}

// Manager serializes update checks and starts one persisted upgrade at a time.
type Manager struct {
	store          *FileStore
	checker        *Checker
	platform       Platform
	now            func() time.Time
	newID          func() string
	launch         func(func())
	rootContext    context.Context
	checkTTL       time.Duration
	stableWindow   time.Duration
	stableInterval time.Duration
	logger         *slog.Logger
	mu             sync.Mutex
}

// NewManager constructs the managed update state machine.
func NewManager(options ManagerOptions) (*Manager, error) {
	if options.Store == nil {
		return nil, errors.New("system update store is required")
	}
	if options.Checker == nil {
		return nil, errors.New("system update checker is required")
	}
	if options.Platform == nil {
		return nil, errors.New("system update platform is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewID == nil {
		options.NewID = uuid.NewString
	}
	if options.Launch == nil {
		options.Launch = func(job func()) { go job() }
	}
	if options.RootContext == nil {
		options.RootContext = context.Background()
	}
	if options.CheckTTL <= 0 {
		options.CheckTTL = defaultCheckTTL
	}
	if options.StableWindow <= 0 {
		options.StableWindow = defaultStableWindow
	}
	if options.StableInterval <= 0 {
		options.StableInterval = defaultStableInterval
	}
	return &Manager{
		store:          options.Store,
		checker:        options.Checker,
		platform:       options.Platform,
		now:            options.Now,
		newID:          options.NewID,
		launch:         options.Launch,
		rootContext:    options.RootContext,
		checkTTL:       options.CheckTTL,
		stableWindow:   options.StableWindow,
		stableInterval: options.StableInterval,
		logger:         options.Logger,
	}, nil
}

// Status returns the latest public-safe persisted state.
func (manager *Manager) Status() StatusView {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state, err := manager.store.Load()
	if err != nil {
		return StatusView{}
	}
	return statusView(state)
}

// Check always checks the registries and persists the fresh result.
func (manager *Manager) Check(ctx context.Context) (StatusView, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result, err := manager.checker.Check(ctx)
	if err != nil {
		return StatusView{}, err
	}
	state, err := manager.store.Load()
	if err != nil {
		return StatusView{}, err
	}
	state.LastCheck = &result
	if err := manager.store.Save(state); err != nil {
		return StatusView{}, err
	}
	return statusView(state), nil
}

// Start validates the last check, persists a task, and launches its detached job.
func (manager *Manager) Start(ctx context.Context, targetVersion string) (TaskView, error) {
	if ctx == nil {
		return TaskView{}, errors.New("system update request context is required")
	}
	if err := ValidateVersion(targetVersion); err != nil {
		return TaskView{}, err
	}

	manager.mu.Lock()
	state, err := manager.store.Load()
	if err != nil {
		manager.mu.Unlock()
		return TaskView{}, err
	}
	if state.Task != nil && (!state.Task.Stage.Terminal() || state.Task.Stage == StageManualIntervention) {
		manager.mu.Unlock()
		return TaskView{}, errors.New("system update task is already active")
	}
	check := state.LastCheck
	now := manager.now().UTC()
	if check == nil || check.CheckedAt.After(now) || now.Sub(check.CheckedAt) >= manager.checkTTL {
		manager.mu.Unlock()
		return TaskView{}, errors.New("system update check has expired")
	}
	if !check.Available {
		manager.mu.Unlock()
		return TaskView{}, errors.New("system update is not available")
	}
	if check.Target.Backend.Version != targetVersion || check.Target.Web.Version != targetVersion {
		manager.mu.Unlock()
		return TaskView{}, errors.New("system update target does not match the last check")
	}
	startedAt := now
	task := Task{
		ID:         manager.newID(),
		Original:   check.Current,
		Target:     check.Target,
		Stage:      StageChecking,
		CreatedAt:  now,
		StartedAt:  &startedAt,
		Cleanup:    CleanupNotRun,
		RolledBack: false,
	}
	state.Task = &task
	if err := manager.store.Save(state); err != nil {
		manager.mu.Unlock()
		return TaskView{}, err
	}
	manager.mu.Unlock()

	manager.launch(func() { manager.run(task) })

	manager.mu.Lock()
	defer manager.mu.Unlock()
	state, err = manager.store.Load()
	if err != nil {
		return TaskView{}, err
	}
	if state.Task == nil || state.Task.ID != task.ID {
		return TaskView{}, errors.New("system update task state is invalid")
	}
	return publicTaskView(*state.Task), nil
}

func (manager *Manager) run(task Task) {
	ctx := manager.rootContext
	var image Image
	if err := manager.perform(&task, StageChecking, func() error {
		var err error
		image, err = manager.platform.InspectService(ctx, ServiceBackend)
		if err == nil {
			task.Original.Backend = image
		}
		return err
	}); err != nil {
		manager.handleFailure(&task, "state_invalid", false, err)
		return
	}
	if err := manager.perform(&task, StageChecking, func() error {
		var err error
		image, err = manager.platform.InspectService(ctx, ServiceWeb)
		if err == nil {
			task.Original.Web = image
		}
		return err
	}); err != nil {
		manager.handleFailure(&task, "state_invalid", false, err)
		return
	}
	if err := manager.perform(&task, StageBackingUp, func() error {
		var err error
		task.Original.Backend, err = manager.platform.CreateRollbackAlias(ctx, ServiceBackend, task.Original.Backend, task.ID)
		return err
	}); err != nil {
		manager.handleFailure(&task, "backup_failed", false, err)
		return
	}
	if err := manager.perform(&task, StageBackingUp, func() error {
		var err error
		task.Original.Web, err = manager.platform.CreateRollbackAlias(ctx, ServiceWeb, task.Original.Web, task.ID)
		return err
	}); err != nil {
		manager.handleFailure(&task, "backup_failed", false, err)
		return
	}
	if err := manager.perform(&task, StageBackingUp, func() error {
		var err error
		task.BackupPath, err = manager.platform.BackupDatabase(ctx, task.ID)
		return err
	}); err != nil {
		manager.handleFailure(&task, "backup_failed", false, err)
		return
	}
	if err := manager.perform(&task, StagePulling, func() error {
		return manager.platform.PullTarget(ctx, ServiceBackend, task.Target.Backend)
	}); err != nil {
		manager.handleFailure(&task, "pull_failed", false, err)
		return
	}
	if err := manager.perform(&task, StagePulling, func() error {
		return manager.platform.PullTarget(ctx, ServiceWeb, task.Target.Web)
	}); err != nil {
		manager.handleFailure(&task, "pull_failed", false, err)
		return
	}
	if err := manager.perform(&task, StageSwitchingBackend, func() error {
		return manager.platform.DeployTarget(ctx, ServiceBackend, task.Target, task.ID)
	}); err != nil {
		manager.handleFailure(&task, "backend_switch_failed", true, err)
		return
	}
	if err := manager.perform(&task, StageCheckingBackend, func() error {
		return manager.platform.WaitHealthy(ctx, ServiceBackend)
	}); err != nil {
		manager.handleFailure(&task, "backend_unhealthy", true, err)
		return
	}
	if err := manager.perform(&task, StageSwitchingWeb, func() error {
		return manager.platform.DeployTarget(ctx, ServiceWeb, task.Target, task.ID)
	}); err != nil {
		manager.handleFailure(&task, "web_switch_failed", true, err)
		return
	}
	if err := manager.perform(&task, StageCheckingWeb, func() error {
		return manager.platform.WaitHealthy(ctx, ServiceWeb)
	}); err != nil {
		manager.handleFailure(&task, "web_unhealthy", true, err)
		return
	}
	if err := manager.stabilize(ctx, &task); err != nil {
		manager.handleFailure(&task, "stability_failed", true, err)
		return
	}

	cleanupPending := false
	for _, service := range []ServiceName{ServiceBackend, ServiceWeb} {
		old := task.Original.Backend
		if service == ServiceWeb {
			old = task.Original.Web
		}
		task.Stage = StageCleaning
		if manager.saveTask(task) != nil {
			return
		}
		if manager.platform.RemoveOldImage(ctx, service, old) != nil {
			cleanupPending = true
		}
		if manager.saveTask(task) != nil {
			return
		}
	}

	task.Stage = StageSucceeded
	task.FinishedAt = timePointer(manager.now().UTC())
	if cleanupPending {
		task.Cleanup = CleanupPending
		task.ErrorCode = "cleanup_pending"
	} else {
		task.Cleanup = CleanupComplete
		task.ErrorCode = ""
	}
	task.ErrorMessage = ""
	_ = manager.finishTask(task)
}

// Recover restores a safe terminal state after the updater process restarts.
func (manager *Manager) Recover(ctx context.Context) error {
	if ctx == nil {
		return errors.New("system update recovery context is required")
	}
	manager.mu.Lock()
	state, err := manager.store.Load()
	manager.mu.Unlock()
	if err != nil {
		return err
	}
	if state.Task == nil || state.Task.Stage.Terminal() {
		return nil
	}
	task := *state.Task

	actualBackend, err := manager.platform.InspectService(ctx, ServiceBackend)
	if err != nil {
		manager.logFailure(task, "state_invalid", err)
		return manager.markManualIntervention(&task, "state_invalid")
	}
	actualWeb, err := manager.platform.InspectService(ctx, ServiceWeb)
	if err != nil {
		manager.logFailure(task, "state_invalid", err)
		return manager.markManualIntervention(&task, "state_invalid")
	}
	actual := ImagePair{Backend: actualBackend, Web: actualWeb}

	switch task.Stage {
	case StageChecking, StageBackingUp, StagePulling:
		if imagePairMatches(actual, task.Original) {
			return manager.markFailed(&task, "updater_restarted", false)
		}
		return manager.markManualIntervention(&task, "state_invalid")
	case StageSwitchingBackend, StageCheckingBackend, StageSwitchingWeb, StageCheckingWeb, StageStabilizing, StageRollingBack:
		return manager.rollback(ctx, &task, "updater_restarted")
	case StageCleaning:
		if !hasRollbackImages(task.Original) {
			return manager.markManualIntervention(&task, "state_invalid")
		}
		if !imagePairMatches(actual, task.Target) {
			return manager.rollback(ctx, &task, "updater_restarted")
		}
		for _, service := range []ServiceName{ServiceBackend, ServiceWeb} {
			if err := manager.platform.WaitHealthy(ctx, service); err != nil {
				manager.logFailure(task, "rollback_failed", err)
				return manager.rollback(ctx, &task, "updater_restarted")
			}
		}
		return manager.resumeCleanup(ctx, &task)
	default:
		return manager.markManualIntervention(&task, "state_invalid")
	}
}

func imagePairMatches(actual, expected ImagePair) bool {
	return actual.Backend.Digest == expected.Backend.Digest && actual.Web.Digest == expected.Web.Digest
}

func (manager *Manager) resumeCleanup(ctx context.Context, task *Task) error {
	cleanupPending := false
	for _, service := range []ServiceName{ServiceBackend, ServiceWeb} {
		old := task.Original.Backend
		if service == ServiceWeb {
			old = task.Original.Web
		}
		task.Stage = StageCleaning
		if err := manager.saveTask(*task); err != nil {
			return err
		}
		if err := manager.platform.RemoveOldImage(ctx, service, old); err != nil {
			cleanupPending = true
		}
		if err := manager.saveTask(*task); err != nil {
			return err
		}
	}
	task.Stage = StageSucceeded
	task.FinishedAt = timePointer(manager.now().UTC())
	if cleanupPending {
		task.Cleanup = CleanupPending
		task.ErrorCode = "cleanup_pending"
	} else {
		task.Cleanup = CleanupComplete
		task.ErrorCode = ""
	}
	task.ErrorMessage = ""
	return manager.finishTask(*task)
}

func (manager *Manager) stabilize(ctx context.Context, task *Task) error {
	deadline := time.Now().Add(manager.stableWindow)
	for {
		wait := manager.stableInterval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait > 0 {
			if err := sleepContext(ctx, wait); err != nil {
				return err
			}
		}
		if err := manager.perform(task, StageStabilizing, func() error {
			return manager.platform.WaitHealthy(ctx, ServiceBackend)
		}); err != nil {
			return err
		}
		if err := manager.perform(task, StageStabilizing, func() error {
			return manager.platform.WaitHealthy(ctx, ServiceWeb)
		}); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return nil
		}
	}
}

func (manager *Manager) perform(task *Task, stage Stage, action func() error) error {
	task.Stage = stage
	if err := manager.saveTask(*task); err != nil {
		return err
	}
	if err := action(); err != nil {
		return err
	}
	return manager.saveTask(*task)
}

func (manager *Manager) handleFailure(task *Task, code string, shouldRollback bool, cause error) {
	manager.logFailure(*task, code, cause)
	if shouldRollback {
		if err := manager.rollback(manager.rootContext, task, code); err != nil {
			manager.logFailure(*task, "rollback_failed", err)
		}
		return
	}
	if err := manager.markFailed(task, code, false); err != nil {
		manager.logFailure(*task, code, err)
	}
}

func (manager *Manager) rollback(ctx context.Context, task *Task, code string) error {
	if !hasRollbackImages(task.Original) {
		return manager.markManualIntervention(task, "state_invalid")
	}
	task.Stage = StageRollingBack
	task.ErrorCode = code
	task.ErrorMessage = ""
	if err := manager.saveTask(*task); err != nil {
		return err
	}

	var rollbackErr error
	for _, service := range []ServiceName{ServiceBackend, ServiceWeb} {
		if err := manager.platform.DeployRollback(ctx, service, task.Original, task.ID); err != nil && rollbackErr == nil {
			rollbackErr = err
		}
		if err := manager.platform.WaitHealthy(ctx, service); err != nil && rollbackErr == nil {
			rollbackErr = err
		}
	}
	if rollbackErr != nil {
		manager.logFailure(*task, "rollback_failed", rollbackErr)
		return manager.markManualIntervention(task, "rollback_failed")
	}
	return manager.markFailed(task, code, true)
}

func hasRollbackImages(pair ImagePair) bool {
	return pair.Backend.Digest != "" && pair.Web.Digest != "" && pair.Backend.RollbackAlias != "" && pair.Web.RollbackAlias != ""
}

func (manager *Manager) markFailed(task *Task, code string, rolledBack bool) error {
	task.Stage = StageFailed
	task.RolledBack = rolledBack
	task.ErrorCode = code
	task.ErrorMessage = ""
	task.FinishedAt = timePointer(manager.now().UTC())
	return manager.saveTask(*task)
}

func (manager *Manager) markManualIntervention(task *Task, code string) error {
	task.Stage = StageManualIntervention
	task.RolledBack = false
	task.ErrorCode = code
	task.ErrorMessage = ""
	task.FinishedAt = timePointer(manager.now().UTC())
	return manager.saveTask(*task)
}

func (manager *Manager) logFailure(task Task, code string, err error) {
	if manager.logger == nil || err == nil {
		return
	}
	manager.logger.Error("system update operation failed", "task_id", task.ID, "stage", task.Stage, "error_code", code, "detail", "external operation failed")
}

func (manager *Manager) saveTask(task Task) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state, err := manager.store.Load()
	if err != nil {
		return err
	}
	if state.Task == nil || state.Task.ID != task.ID {
		return errors.New("system update task state is invalid")
	}
	state.Task = &task
	return manager.store.Save(state)
}

func (manager *Manager) finishTask(task Task) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state, err := manager.store.Load()
	if err != nil {
		return err
	}
	if state.Task == nil || state.Task.ID != task.ID || state.LastCheck == nil {
		return errors.New("system update task state is invalid")
	}
	state.Task = &task
	state.LastCheck.Current = task.Target
	state.LastCheck.Available = semver.Compare(state.LastCheck.Target.Backend.Version, state.LastCheck.Current.Backend.Version) > 0
	return manager.store.Save(state)
}

func statusView(state PersistentState) StatusView {
	view := StatusView{}
	if state.LastCheck != nil {
		view.Current = versionPairView(state.LastCheck.Current)
		view.Latest = &ReleaseView{
			VersionPairView: versionPairView(state.LastCheck.Target),
			PublishedAt:     state.LastCheck.Target.Backend.PublishedAt,
		}
		view.UpdateAvailable = state.LastCheck.Available
		checkedAt := state.LastCheck.CheckedAt
		view.CheckedAt = &checkedAt
	}
	if state.Task != nil {
		task := publicTaskView(*state.Task)
		view.Task = &task
	}
	return view
}

func versionPairView(pair ImagePair) VersionPairView {
	return VersionPairView{Backend: pair.Backend.Version, Web: pair.Web.Version}
}

func publicTaskView(task Task) TaskView {
	view := task.View()
	if task.Stage == StageSucceeded && task.Cleanup == CleanupPending {
		view.ErrorCode = "cleanup_pending"
		view.ErrorMessage = cleanupPendingMessage
	}
	return view
}

func timePointer(value time.Time) *time.Time {
	return &value
}
