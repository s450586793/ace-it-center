package update

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maximumHealthTimeout    = 60 * time.Second
	maximumInstallerTimeout = 10 * time.Minute
	defaultRestoreTimeout   = 5 * time.Second
	defaultRestoreInterval  = 250 * time.Millisecond
)

var silentInstallerArguments = []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/FORCECLOSEAPPLICATIONS", "/UPDATEHELPER"}

// HelperOperations 将 Windows Service、进程、pipe 与文件操作从回滚状态机中隔离。
type HelperOperations interface {
	StopService(context.Context) error
	BackupExecutable(currentPath, backupPath string) error
	RunInstaller(context.Context, string, []string) error
	StartService(context.Context) error
	WaitHealthy(context.Context, time.Duration) error
	RestoreExecutable(backupPath, executablePath string) error
	ApplyServiceConfiguration(executablePath string) error
	Cleanup(...string) error
}

type trayLifecycleOperations interface {
	StopTray(context.Context) (bool, error)
	StartTray(context.Context, string) error
}

// HelperRuntime 验证 helper 文件身份并提供跨进程执行锁。
type HelperRuntime interface {
	ValidateRunningHelper(installedExecutable, stagingDirectory string) error
	AcquireExecutionLock() (release func() error, err error)
}

type executionObjectLockOperations interface {
	Create(initialOwner bool) (uintptr, error)
	Close(uintptr) error
	IsAlreadyExists(error) bool
}

func acquireExecutionObjectLock(operations executionObjectLockOperations) (func() error, error) {
	handle, err := operations.Create(false)
	if err != nil {
		if handle != 0 {
			_ = operations.Close(handle)
		}
		if operations.IsAlreadyExists(err) {
			return nil, errors.Join(errors.New("another update helper is already running"), err)
		}
		return nil, fmt.Errorf("create update helper mutex: %w", err)
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			releaseErr = operations.Close(handle)
		})
		return releaseErr
	}, nil
}

type helperIdentityOperations interface {
	RunningExecutable() (string, error)
	FinalPath(string) (string, error)
	SameFile(left, right string) (bool, error)
}

type selfCleanupOperations interface {
	StartDeferredRemoval(string) error
	DelayRemovalUntilReboot(string) error
}

// HelperOptions 只包含本地路径和发布元数据，绝不能包含 enrollment token 或 Agent credential。
type HelperOptions struct {
	InstallerPath    string
	ExecutablePath   string
	BackupPath       string
	StagingDir       string
	Version          string
	InstallerTimeout time.Duration
	HealthTimeout    time.Duration
	Operations       HelperOperations
	Runtime          HelperRuntime
	CleanupWarning   func(error)
	selfCleanup      func() error
	cleanupMarker    func(string, error)
	restoreTimeout   time.Duration
	restoreInterval  time.Duration
}

// LaunchOperations 隔离临时副本与 detached 进程创建操作。
type LaunchOperations interface {
	CopyTemporaryHelper(sourcePath, stagingDirectory string) (string, error)
	StartDetached(context.Context, string, []string, DetachedLaunchOptions) error
	Remove(string) error
}

// DetachedLaunchOptions 描述 helper 停止父 Windows Service 后仍需存活的进程隔离策略。
type DetachedLaunchOptions struct {
	BreakawayFromJob bool
}

// LaunchOptions 包含传给 update-helper 的非敏感参数。
type LaunchOptions struct {
	ExecutablePath string
	InstallerPath  string
	BackupPath     string
	StagingDir     string
	Version        string
	Operations     LaunchOperations
}

// LaunchHelper 启动临时副本，使 helper 运行时可替换已安装的 Agent 可执行文件。
func LaunchHelper(ctx context.Context, options LaunchOptions) error {
	if ctx == nil {
		return errors.New("update launch context is required")
	}
	if options.ExecutablePath == "" || options.InstallerPath == "" || options.BackupPath == "" || options.StagingDir == "" || options.Version == "" {
		return errors.New("update launch requires executable, installer, backup, staging directory, and version")
	}
	for _, path := range []string{options.ExecutablePath, options.InstallerPath, options.BackupPath, options.StagingDir} {
		if !filepath.IsAbs(path) {
			return errors.New("update launch paths must be absolute")
		}
	}
	operations := options.Operations
	if operations == nil {
		operations = defaultLaunchOperations()
		if operations == nil {
			return errors.New("update launch is unavailable on this platform")
		}
	}
	helperPath, err := operations.CopyTemporaryHelper(options.ExecutablePath, options.StagingDir)
	if err != nil {
		return fmt.Errorf("copy temporary update helper: %w", err)
	}
	arguments := []string{
		"update-helper",
		"--installer", options.InstallerPath,
		"--executable", options.ExecutablePath,
		"--backup", options.BackupPath,
		"--version", options.Version,
	}
	detached := DetachedLaunchOptions{BreakawayFromJob: true}
	if err := operations.StartDetached(ctx, helperPath, arguments, detached); err != nil {
		return errors.Join(fmt.Errorf("start detached update helper: %w", err), wrapOptional("remove temporary update helper", operations.Remove(helperPath)))
	}
	return nil
}

// RunHelper 安装已暂存更新；安装、Service 启动或 pipe 健康校验失败时恢复 LKG 二进制。
func RunHelper(ctx context.Context, options HelperOptions) (resultErr error) {
	if ctx == nil {
		return errors.New("update helper context is required")
	}
	if err := validateHelperOptions(options); err != nil {
		return err
	}
	usingDefaultOperations := options.Operations == nil
	selfCleanup := options.selfCleanup
	if selfCleanup == nil && usingDefaultOperations {
		selfCleanup = cleanupRunningHelper
	}
	runtime := options.Runtime
	if runtime == nil {
		runtime = defaultHelperRuntime()
		if runtime == nil {
			return errors.New("update helper runtime is unavailable on this platform")
		}
	}
	if err := runtime.ValidateRunningHelper(options.ExecutablePath, options.StagingDir); err != nil {
		return fmt.Errorf("validate running update helper: %w", err)
	}
	if selfCleanup != nil {
		defer func() {
			if err := selfCleanup(); err != nil {
				reportCleanupWarning(options, fmt.Errorf("schedule running helper cleanup: %w", err))
			}
		}()
	}
	release, err := runtime.AcquireExecutionLock()
	if err != nil {
		return fmt.Errorf("acquire update helper execution lock: %w", err)
	}
	defer func() {
		if err := release(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release update helper execution lock: %w", err))
		}
	}()

	operations := options.Operations
	if operations == nil {
		operations = defaultHelperOperations(options)
		if operations == nil {
			return errors.New("update helper is unavailable on this platform")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := operations.StopService(ctx); err != nil {
		restartErr := operations.StartService(ctx)
		return errors.Join(
			fmt.Errorf("stop Agent Service: %w", err),
			wrapOptional("restart unchanged Agent Service", restartErr),
		)
	}
	if trayOperations, supported := operations.(trayLifecycleOperations); supported {
		trayWasRunning, err := trayOperations.StopTray(ctx)
		if err != nil {
			restartErr := operations.StartService(ctx)
			return errors.Join(
				fmt.Errorf("stop Agent tray: %w", err),
				wrapOptional("restart unchanged Agent Service", restartErr),
			)
		}
		if trayWasRunning {
			defer func() {
				if err := trayOperations.StartTray(ctx, options.ExecutablePath); err != nil {
					reportCleanupWarning(options, fmt.Errorf("restart Agent tray: %w", err))
				}
			}()
		}
	}
	if err := operations.BackupExecutable(options.ExecutablePath, options.BackupPath); err != nil {
		restartErr := operations.StartService(ctx)
		return errors.Join(fmt.Errorf("store last-known-good Agent: %w", err), wrapOptional("restart unchanged Agent Service", restartErr))
	}
	installerTimeout := options.InstallerTimeout
	if installerTimeout <= 0 || installerTimeout > maximumInstallerTimeout {
		installerTimeout = maximumInstallerTimeout
	}
	installerContext, cancelInstaller := context.WithTimeout(ctx, installerTimeout)
	installerErr := operations.RunInstaller(installerContext, options.InstallerPath, append([]string(nil), silentInstallerArguments...))
	cancelInstaller()
	if installerErr != nil {
		return rollbackHelper(ctx, operations, options, fmt.Errorf("run silent installer: %w", installerErr))
	}
	if err := operations.ApplyServiceConfiguration(options.ExecutablePath); err != nil {
		return rollbackHelper(ctx, operations, options, fmt.Errorf("configure updated Agent Service: %w", err))
	}
	if err := operations.StartService(ctx); err != nil {
		return rollbackHelper(ctx, operations, options, fmt.Errorf("start updated Agent Service: %w", err))
	}
	healthTimeout := options.HealthTimeout
	if healthTimeout <= 0 || healthTimeout > maximumHealthTimeout {
		healthTimeout = maximumHealthTimeout
	}
	if err := operations.WaitHealthy(ctx, healthTimeout); err != nil {
		return rollbackHelper(ctx, operations, options, fmt.Errorf("validate updated Agent health: %w", err))
	}
	if err := operations.Cleanup(options.InstallerPath, options.BackupPath); err != nil {
		reportCleanupWarning(options, fmt.Errorf("clean successful update files: %w", err))
	}
	return nil
}

func rollbackHelper(ctx context.Context, operations HelperOperations, options HelperOptions, updateErr error) error {
	stopErr := operations.StopService(ctx)
	if stopErr != nil {
		return errors.Join(updateErr, wrapOptional("stop failed updated Agent Service", stopErr))
	}
	restoreErr := restoreLastKnownGood(ctx, operations, options)
	configurationErr := operations.ApplyServiceConfiguration(options.ExecutablePath)
	if configurationErr != nil {
		return errors.Join(
			updateErr,
			wrapOptional("restore last-known-good Agent", restoreErr),
			wrapOptional("reapply last-known-good Agent Service configuration", configurationErr),
		)
	}
	restartErr := operations.StartService(ctx)
	if restartErr != nil {
		return errors.Join(
			updateErr,
			wrapOptional("restore last-known-good Agent", restoreErr),
			wrapOptional("reapply last-known-good Agent Service configuration", configurationErr),
			wrapOptional("restart last-known-good Agent Service", restartErr),
		)
	}
	healthTimeout := options.HealthTimeout
	if healthTimeout <= 0 || healthTimeout > maximumHealthTimeout {
		healthTimeout = maximumHealthTimeout
	}
	healthErr := operations.WaitHealthy(ctx, healthTimeout)
	return errors.Join(
		updateErr,
		wrapOptional("restore last-known-good Agent", restoreErr),
		wrapOptional("reapply last-known-good Agent Service configuration", configurationErr),
		wrapOptional("restart last-known-good Agent Service", restartErr),
		wrapOptional("validate last-known-good Agent health", healthErr),
	)
}

func restoreLastKnownGood(ctx context.Context, operations HelperOperations, options HelperOptions) error {
	timeout := options.restoreTimeout
	if timeout <= 0 {
		timeout = defaultRestoreTimeout
	}
	interval := options.restoreInterval
	if interval <= 0 {
		interval = defaultRestoreInterval
	}
	restoreContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		if err := operations.RestoreExecutable(options.BackupPath, options.ExecutablePath); err == nil {
			return nil
		} else {
			lastErr = err
		}
		timer := time.NewTimer(interval)
		select {
		case <-restoreContext.Done():
			if !timer.Stop() {
				<-timer.C
			}
			if err := ctx.Err(); err != nil {
				return errors.Join(lastErr, err)
			}
			return lastErr
		case <-timer.C:
		}
	}
}

func waitForHealthy(ctx context.Context, timeout, pollInterval time.Duration, attempt func(context.Context) (bool, error)) error {
	if timeout <= 0 || pollInterval <= 0 || attempt == nil {
		return errors.New("health wait requires positive durations and an attempt function")
	}
	healthContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		healthy, err := attempt(healthContext)
		if healthy {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-healthContext.Done():
			if err := ctx.Err(); err != nil {
				return err
			}
			if lastErr != nil {
				return fmt.Errorf("Agent pipe did not report healthy before timeout: %w", lastErr)
			}
			return errors.New("Agent pipe did not report healthy before timeout")
		case <-ticker.C:
		}
	}
}

func validateHelperOptions(options HelperOptions) error {
	if options.InstallerPath == "" || options.ExecutablePath == "" || options.BackupPath == "" || options.StagingDir == "" || options.Version == "" {
		return errors.New("update helper requires installer, executable, backup, staging directory, and version")
	}
	if !filepath.IsAbs(options.InstallerPath) || !filepath.IsAbs(options.ExecutablePath) || !filepath.IsAbs(options.BackupPath) || !filepath.IsAbs(options.StagingDir) {
		return errors.New("update helper paths must be absolute")
	}
	installer := filepath.Clean(options.InstallerPath)
	executable := filepath.Clean(options.ExecutablePath)
	backup := filepath.Clean(options.BackupPath)
	if installer == executable || installer == backup || executable == backup {
		return errors.New("update helper paths must be distinct")
	}
	return nil
}

func validateHelperIdentity(installedExecutable, stagingDirectory string, operations helperIdentityOperations) error {
	if operations == nil {
		return errors.New("helper identity operations are required")
	}
	runningExecutable, err := operations.RunningExecutable()
	if err != nil {
		return fmt.Errorf("locate running helper executable: %w", err)
	}
	runningFinal, err := operations.FinalPath(runningExecutable)
	if err != nil {
		return fmt.Errorf("resolve running helper path: %w", err)
	}
	installedFinal, err := operations.FinalPath(installedExecutable)
	if err != nil {
		return fmt.Errorf("resolve installed Agent path: %w", err)
	}
	stagingFinal, err := operations.FinalPath(stagingDirectory)
	if err != nil {
		return fmt.Errorf("resolve update staging path: %w", err)
	}
	same, err := operations.SameFile(runningFinal, installedFinal)
	if err != nil {
		return fmt.Errorf("compare helper and installed Agent identity: %w", err)
	}
	if same {
		return errors.New("installed Agent executable cannot run as update helper")
	}
	relative, err := filepath.Rel(stagingFinal, runningFinal)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("running update helper is outside the expected staging directory")
	}
	name := filepath.Base(runningFinal)
	if !strings.HasPrefix(name, ".AceAgent-update-helper-") || !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return errors.New("running executable is not a staged update helper")
	}
	return nil
}

func reportCleanupWarning(options HelperOptions, err error) {
	if err == nil {
		return
	}
	if options.CleanupWarning != nil {
		options.CleanupWarning(err)
	}
	marker := options.cleanupMarker
	if marker == nil {
		marker = recordCleanupWarning
	}
	marker(options.StagingDir, err)
}

func scheduleSelfCleanup(path string, operations selfCleanupOperations) error {
	if operations == nil {
		return errors.New("self cleanup operations are required")
	}
	startErr := operations.StartDeferredRemoval(path)
	delayErr := operations.DelayRemovalUntilReboot(path)
	if delayErr == nil {
		return nil
	}
	if startErr == nil {
		return fmt.Errorf("schedule helper removal at reboot: %w", delayErr)
	}
	return errors.Join(
		fmt.Errorf("start deferred helper removal: %w", startErr),
		fmt.Errorf("schedule helper removal at reboot: %w", delayErr),
	)
}

func wrapOptional(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
