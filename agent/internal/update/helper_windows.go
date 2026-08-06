//go:build windows

package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"aceitcenter.local/platform/agent/internal/ipc"
	"aceitcenter.local/platform/agent/internal/windowsservice"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	stagingDirectorySDDL = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	stagingFileSDDL      = "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
)

const (
	serviceOperationTimeout = 30 * time.Second
	healthPollInterval      = 250 * time.Millisecond
	healthAttemptTimeout    = 2 * time.Second
	trayGracefulStopTimeout = 2 * time.Second
	trayForcedStopTimeout   = 5 * time.Second
)

type windowsHelperOperations struct{}

type windowsLaunchOperations struct{}

type windowsHelperRuntime struct{}

type windowsIdentityOperations struct{}

type windowsSelfCleanupOperations struct{}

type nativeWindowsExecutionLockOperations struct{}

func defaultHelperOperations(options HelperOptions) HelperOperations {
	return &windowsHelperOperations{}
}

func defaultLaunchOperations() LaunchOperations { return windowsLaunchOperations{} }

func defaultHelperRuntime() HelperRuntime { return windowsHelperRuntime{} }

func (operations *windowsHelperOperations) StopService(ctx context.Context) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(windowsservice.ServiceName)
	if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open Agent Service: %w", err)
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("query Agent Service: %w", err)
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status.State != svc.StopPending {
		if _, err := service.Control(svc.Stop); err != nil && err != windows.ERROR_SERVICE_NOT_ACTIVE {
			return fmt.Errorf("request Agent Service stop: %w", err)
		}
	}
	return waitForWindowsServiceState(ctx, service, svc.Stopped)
}

func (operations *windowsHelperOperations) StopTray(ctx context.Context) (bool, error) {
	name, err := windows.UTF16PtrFromString(ipc.WindowsTrayUpdateEventName)
	if err != nil {
		return false, fmt.Errorf("encode tray update event name: %w", err)
	}
	event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open tray update event: %w", err)
	}
	if err := windows.SetEvent(event); err != nil {
		windows.CloseHandle(event)
		return true, fmt.Errorf("signal tray update event: %w", err)
	}
	windows.CloseHandle(event)

	gracefulContext, cancelGraceful := context.WithTimeout(ctx, trayGracefulStopTimeout)
	gracefulErr := waitForTrayEventClosed(gracefulContext, name)
	cancelGraceful()
	if gracefulErr == nil {
		return true, nil
	}
	if !errors.Is(gracefulErr, context.DeadlineExceeded) {
		return true, gracefulErr
	}

	killContext, cancelKill := context.WithTimeout(ctx, trayForcedStopTimeout)
	kill := exec.CommandContext(killContext, "taskkill.exe", "/IM", "AceAgent.exe", "/T", "/F")
	kill.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
	killErr := kill.Run()
	cancelKill()

	waitContext, cancelWait := context.WithTimeout(ctx, trayForcedStopTimeout)
	waitErr := waitForTrayEventClosed(waitContext, name)
	cancelWait()
	if waitErr != nil {
		return true, errors.Join(fmt.Errorf("force-close Agent tray: %w", killErr), waitErr)
	}
	return true, nil
}

func (operations *windowsHelperOperations) StartTray(ctx context.Context, executable string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID := windows.WTSGetActiveConsoleSessionId()
	if sessionID == ^uint32(0) {
		return nil
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &token); err != nil {
		if errors.Is(err, windows.ERROR_NO_TOKEN) {
			return nil
		}
		return fmt.Errorf("query active user token: %w", err)
	}
	defer token.Close()

	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return fmt.Errorf("encode tray executable: %w", err)
	}
	commandLine, err := windows.UTF16PtrFromString(`"` + executable + `" tray`)
	if err != nil {
		return fmt.Errorf("encode tray command line: %w", err)
	}
	desktop, err := windows.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return fmt.Errorf("encode interactive desktop: %w", err)
	}
	currentDirectory, err := windows.UTF16PtrFromString(filepath.Dir(executable))
	if err != nil {
		return fmt.Errorf("encode tray working directory: %w", err)
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: desktop}
	var process windows.ProcessInformation
	if err := windows.CreateProcessAsUser(
		token, application, commandLine, nil, nil, false,
		windows.CREATE_NEW_PROCESS_GROUP, nil, currentDirectory, &startup, &process,
	); err != nil {
		return fmt.Errorf("start tray in active user session: %w", err)
	}
	windows.CloseHandle(process.Thread)
	windows.CloseHandle(process.Process)
	return nil
}

func waitForTrayEventClosed(ctx context.Context, name *uint16) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("query tray update event: %w", err)
		}
		windows.CloseHandle(event)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (operations *windowsHelperOperations) BackupExecutable(currentPath, backupPath string) error {
	return copyWindowsFileAtomic(currentPath, backupPath, true)
}

func (operations *windowsHelperOperations) RunInstaller(ctx context.Context, installer string, arguments []string) error {
	command := exec.CommandContext(ctx, installer, arguments...)
	command.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("installer process failed: %w (%s)", err, boundedProcessOutput(output))
	}
	return nil
}

func (operations *windowsHelperOperations) StartService(ctx context.Context) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(windowsservice.ServiceName)
	if err != nil {
		return fmt.Errorf("open Agent Service: %w", err)
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("query Agent Service: %w", err)
	}
	if status.State == svc.Running {
		return nil
	}
	if err := service.Start(); err != nil {
		return fmt.Errorf("request Agent Service start: %w", err)
	}
	return waitForWindowsServiceState(ctx, service, svc.Running)
}

func (operations *windowsHelperOperations) WaitHealthy(ctx context.Context, timeout time.Duration) error {
	return waitForHealthy(ctx, timeout, healthPollInterval, func(healthContext context.Context) (bool, error) {
		attemptContext, cancel := context.WithTimeout(healthContext, healthAttemptTimeout)
		defer cancel()
		return pipeReportsHealthy(attemptContext)
	})
}

func (operations *windowsHelperOperations) RestoreExecutable(backupPath, executablePath string) error {
	return copyWindowsFileAtomic(backupPath, executablePath, false)
}

func (operations *windowsHelperOperations) ApplyServiceConfiguration(executablePath string) error {
	if err := windowsservice.RestoreServiceExecutable(executablePath); err != nil {
		return fmt.Errorf("apply Agent Service configuration: %w", err)
	}
	return nil
}

func (operations *windowsHelperOperations) Cleanup(paths ...string) error {
	var cleanupErrors []error
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove %s: %w", filepath.Base(path), err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func (windowsLaunchOperations) CopyTemporaryHelper(sourcePath, stagingDirectory string) (string, error) {
	if err := os.MkdirAll(stagingDirectory, 0o700); err != nil {
		return "", err
	}
	if err := secureStagingDirectory(stagingDirectory); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(stagingDirectory, ".AceAgent-update-helper-*.exe")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return "", err
	}
	_ = os.Remove(temporaryPath)
	if err := copyWindowsFileAtomic(sourcePath, temporaryPath, true); err != nil {
		_ = os.Remove(temporaryPath)
		return "", err
	}
	return temporaryPath, nil
}

func (windowsLaunchOperations) StartDetached(ctx context.Context, executable string, arguments []string, options DetachedLaunchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	creationFlags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS)
	if options.BreakawayFromJob {
		creationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	command := exec.Command(executable, arguments...)
	command.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: creationFlags,
		HideWindow:    true,
	}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func (windowsLaunchOperations) Remove(path string) error { return os.Remove(path) }

func (windowsHelperRuntime) ValidateRunningHelper(installedExecutable, stagingDirectory string) error {
	return validateHelperIdentity(installedExecutable, stagingDirectory, windowsIdentityOperations{})
}

func (windowsHelperRuntime) AcquireExecutionLock() (func() error, error) {
	return acquireExecutionObjectLock(nativeWindowsExecutionLockOperations{})
}

func (nativeWindowsExecutionLockOperations) Create(initialOwner bool) (uintptr, error) {
	name, err := windows.UTF16PtrFromString(`Global\AceITCenterAgentUpdate`)
	if err != nil {
		return 0, err
	}
	handle, createErr := windows.CreateMutex(nil, initialOwner, name)
	return uintptr(handle), createErr
}

func (nativeWindowsExecutionLockOperations) Close(handle uintptr) error {
	return windows.CloseHandle(windows.Handle(handle))
}

func (nativeWindowsExecutionLockOperations) IsAlreadyExists(err error) bool {
	return errors.Is(err, windows.ERROR_ALREADY_EXISTS)
}

func (windowsIdentityOperations) RunningExecutable() (string, error) { return os.Executable() }

func (windowsIdentityOperations) FinalPath(path string) (string, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(
		pointer,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 512)
	for {
		count, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if count < uint32(len(buffer)) {
			resolved := windows.UTF16ToString(buffer[:count])
			resolved = strings.TrimPrefix(resolved, `\\?\`)
			if strings.HasPrefix(resolved, `UNC\`) {
				resolved = `\\` + strings.TrimPrefix(resolved, `UNC\`)
			}
			return filepath.Clean(resolved), nil
		}
		buffer = make([]uint16, count+1)
	}
}

func (windowsIdentityOperations) SameFile(left, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false, err
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func waitForWindowsServiceState(ctx context.Context, service *mgr.Service, wanted svc.State) error {
	operationContext, cancel := context.WithTimeout(ctx, serviceOperationTimeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-operationContext.Done():
			return operationContext.Err()
		case <-ticker.C:
			status, err := service.Query()
			if err != nil {
				return fmt.Errorf("query Agent Service transition: %w", err)
			}
			if status.State == wanted {
				return nil
			}
		}
	}
}

func copyWindowsFileAtomic(sourcePath, destinationPath string, secure bool) (resultErr error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	directory := filepath.Dir(destinationPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".AceAgent-replace-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := io.Copy(temporary, source); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if secure {
		if err := secureStagingFile(temporaryPath); err != nil {
			return err
		}
	}
	from, err := windows.UTF16PtrFromString(temporaryPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destinationPath)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return err
	}
	return nil
}

func pipeReportsHealthy(ctx context.Context) (bool, error) {
	client, err := ipc.DialWindows(ctx)
	if err != nil {
		return false, err
	}
	defer client.Close()
	response, err := client.Call(ctx, ipc.Request{ID: "update-health", Method: "status.get"})
	if err != nil {
		return false, err
	}
	if response.Error != nil {
		return false, errors.New("Agent pipe returned a status error")
	}
	contents, err := json.Marshal(response.Result)
	if err != nil {
		return false, err
	}
	var status struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(contents, &status); err != nil {
		return false, err
	}
	return status.State == "online", nil
}

func boundedProcessOutput(output []byte) string {
	const maximum = 1024
	if len(output) > maximum {
		output = output[:maximum]
	}
	return string(output)
}

func cleanupRunningHelper() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return scheduleSelfCleanup(executable, windowsSelfCleanupOperations{})
}

func (windowsSelfCleanupOperations) StartDeferredRemoval(executable string) error {
	script := `param([string]$Path); Start-Sleep -Milliseconds 500; Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue`
	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script, executable)
	command.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS, HideWindow: true}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func (windowsSelfCleanupOperations) DelayRemovalUntilReboot(path string) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(pointer, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}

func recordCleanupWarning(stagingDirectory string, _ error) {
	marker := filepath.Join(stagingDirectory, "cleanup-warning.marker")
	if err := os.WriteFile(marker, []byte("update cleanup requires attention\n"), 0o600); err == nil {
		_ = secureStagingFile(marker)
	}
}

func CurrentOSVersion() (string, error) {
	version := windows.RtlGetVersion()
	if version == nil {
		return "", errors.New("read Windows version")
	}
	return fmt.Sprintf("%d.%d.%d", version.MajorVersion, version.MinorVersion, version.BuildNumber), nil
}

func secureStagingDirectory(path string) error { return applyStagingACL(path, stagingDirectorySDDL) }

func secureStagingFile(path string) error { return applyStagingACL(path, stagingFileSDDL) }

func applyStagingACL(path, sddl string) error {
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse staging ACL: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read staging ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("apply staging ACL: %w", err)
	}
	return nil
}

// 安装器在 rename 前已 flush；Windows 未通过 os.File 暴露可移植的目录 fsync 等价操作。
func syncStagingDirectory(string) error { return nil }
