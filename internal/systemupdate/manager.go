package systemupdate

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
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
	if state.Task != nil && !state.Task.Stage.Terminal() {
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
	if manager.perform(&task, StageChecking, func() error {
		var err error
		image, err = manager.platform.InspectService(ctx, ServiceBackend)
		if err == nil {
			task.Original.Backend = image
		}
		return err
	}) != nil {
		return
	}
	if manager.perform(&task, StageChecking, func() error {
		var err error
		image, err = manager.platform.InspectService(ctx, ServiceWeb)
		if err == nil {
			task.Original.Web = image
		}
		return err
	}) != nil {
		return
	}
	if manager.perform(&task, StageBackingUp, func() error {
		var err error
		task.Original.Backend, err = manager.platform.CreateRollbackAlias(ctx, ServiceBackend, task.Original.Backend, task.ID)
		return err
	}) != nil {
		return
	}
	if manager.perform(&task, StageBackingUp, func() error {
		var err error
		task.Original.Web, err = manager.platform.CreateRollbackAlias(ctx, ServiceWeb, task.Original.Web, task.ID)
		return err
	}) != nil {
		return
	}
	if manager.perform(&task, StageBackingUp, func() error {
		var err error
		task.BackupPath, err = manager.platform.BackupDatabase(ctx, task.ID)
		return err
	}) != nil {
		return
	}
	if manager.perform(&task, StagePulling, func() error {
		return manager.platform.PullTarget(ctx, ServiceBackend, task.Target.Backend)
	}) != nil {
		return
	}
	if manager.perform(&task, StagePulling, func() error {
		return manager.platform.PullTarget(ctx, ServiceWeb, task.Target.Web)
	}) != nil {
		return
	}
	if manager.perform(&task, StageSwitchingBackend, func() error {
		return manager.platform.DeployTarget(ctx, ServiceBackend, task.Target, task.ID)
	}) != nil {
		return
	}
	if manager.perform(&task, StageCheckingBackend, func() error {
		return manager.platform.WaitHealthy(ctx, ServiceBackend)
	}) != nil {
		return
	}
	if manager.perform(&task, StageSwitchingWeb, func() error {
		return manager.platform.DeployTarget(ctx, ServiceWeb, task.Target, task.ID)
	}) != nil {
		return
	}
	if manager.perform(&task, StageCheckingWeb, func() error {
		return manager.platform.WaitHealthy(ctx, ServiceWeb)
	}) != nil {
		return
	}
	if manager.stabilize(ctx, &task) != nil {
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
	state.LastCheck.Available = false
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
