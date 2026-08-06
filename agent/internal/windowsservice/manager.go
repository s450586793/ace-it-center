package windowsservice

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	ServiceName        = "AceITCenterAgent"
	ServiceDisplayName = "Ace IT Center Agent"
)

var (
	errServiceMissing         = errors.New("service does not exist")
	errServiceMarkedForDelete = errors.New("service is marked for deletion")
)

type serviceConfig struct {
	DisplayName      string
	AutomaticStart   bool
	DelayedAutoStart bool
	ServiceStartName string
	Executable       string
	Arguments        []string
}

type recoveryAction struct {
	Delay time.Duration
}

type serviceState uint8

const (
	serviceRunning serviceState = iota
	serviceStopPending
	serviceStopped
)

type scmManager interface {
	OpenService(string) (scmService, error)
	CreateService(string, string, serviceConfig, ...string) (scmService, error)
}

type scmService interface {
	UpdateConfig(serviceConfig) error
	SetRecoveryActions([]recoveryAction) error
	SetRecoveryActionsOnNonCrashFailures(bool) error
	Query() (serviceState, error)
	Stop() error
	Delete() error
	Close() error
}

func serviceConfiguration(executable string) serviceConfig {
	return serviceConfig{
		DisplayName:      ServiceDisplayName,
		AutomaticStart:   true,
		DelayedAutoStart: true,
		ServiceStartName: "LocalSystem",
		Executable:       executable,
		Arguments:        []string{"service"},
	}
}

func installWithManager(manager scmManager, executable string) error {
	if err := validateExecutable(executable); err != nil {
		return err
	}
	configuration := serviceConfiguration(executable)
	service, err := manager.OpenService(ServiceName)
	created := false
	if errors.Is(err, errServiceMissing) {
		service, err = manager.CreateService(ServiceName, executable, configuration, "service")
		created = err == nil
	}
	if err != nil {
		return fmt.Errorf("open or create service: %w", err)
	}
	defer service.Close()
	if !created {
		if err := service.UpdateConfig(configuration); err != nil {
			return fmt.Errorf("configure service: %w", err)
		}
	}
	actions := []recoveryAction{{Delay: time.Minute}, {Delay: time.Minute}, {Delay: time.Minute}}
	if err := service.SetRecoveryActions(actions); err != nil {
		return fmt.Errorf("configure service recovery: %w", err)
	}
	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("configure service recovery on non-crash failures: %w", err)
	}
	return nil
}

// restoreServiceExecutableWithManager 仅恢复回滚所需的 Service 映像路径配置，
// 避免重写 recovery 配置后因辅助设置失败阻断旧版本启动。
func restoreServiceExecutableWithManager(manager scmManager, executable string) error {
	if err := validateExecutable(executable); err != nil {
		return err
	}
	configuration := serviceConfiguration(executable)
	service, err := manager.OpenService(ServiceName)
	created := false
	if errors.Is(err, errServiceMissing) {
		service, err = manager.CreateService(ServiceName, executable, configuration, "service")
		created = err == nil
	}
	if err != nil {
		return fmt.Errorf("open or recreate service for executable restore: %w", err)
	}
	defer service.Close()
	if !created {
		if err := service.UpdateConfig(configuration); err != nil {
			return fmt.Errorf("restore service executable configuration: %w", err)
		}
	}
	return nil
}

func stopWithManager(ctx context.Context, manager scmManager, timeout, pollInterval time.Duration) error {
	if ctx == nil {
		return fmt.Errorf("stop context is required")
	}
	service, err := manager.OpenService(ServiceName)
	if errors.Is(err, errServiceMissing) {
		return nil
	}
	if errors.Is(err, errServiceMarkedForDelete) {
		return waitForDeleted(ctx, manager, timeout, pollInterval)
	}
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	if err := waitForStopped(ctx, service, timeout, pollInterval); err != nil {
		_ = service.Close()
		return err
	}
	if err := service.Close(); err != nil {
		return fmt.Errorf("close stopped service: %w", err)
	}
	return nil
}

func uninstallWithManager(ctx context.Context, manager scmManager, timeout, pollInterval time.Duration) error {
	if ctx == nil {
		return fmt.Errorf("uninstall context is required")
	}
	service, err := manager.OpenService(ServiceName)
	if errors.Is(err, errServiceMissing) {
		return nil
	}
	if errors.Is(err, errServiceMarkedForDelete) {
		return waitForDeleted(ctx, manager, timeout, pollInterval)
	}
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	if err := waitForStopped(ctx, service, timeout, pollInterval); err != nil {
		_ = service.Close()
		return err
	}
	if err := service.Delete(); err != nil && !errors.Is(err, errServiceMarkedForDelete) {
		_ = service.Close()
		return fmt.Errorf("delete service: %w", err)
	}
	if err := service.Close(); err != nil {
		return fmt.Errorf("close deleted service: %w", err)
	}
	return waitForDeleted(ctx, manager, timeout, pollInterval)
}

func waitForStopped(ctx context.Context, service scmService, timeout, pollInterval time.Duration) error {
	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("query service: %w", err)
	}
	if status == serviceStopped {
		return nil
	}
	if status != serviceStopPending {
		if err := service.Stop(); err != nil {
			return fmt.Errorf("stop service: %w", err)
		}
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for service to stop")
		case <-ticker.C:
			status, err := service.Query()
			if err != nil {
				return fmt.Errorf("query service while stopping: %w", err)
			}
			if status == serviceStopped {
				return nil
			}
		}
	}
}

func waitForDeleted(ctx context.Context, manager scmManager, timeout, pollInterval time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for service deletion")
		case <-ticker.C:
			service, err := manager.OpenService(ServiceName)
			if errors.Is(err, errServiceMissing) {
				return nil
			}
			if errors.Is(err, errServiceMarkedForDelete) {
				continue
			}
			if err != nil {
				return fmt.Errorf("query service deletion: %w", err)
			}
			if err := service.Close(); err != nil {
				return fmt.Errorf("close service while waiting for deletion: %w", err)
			}
		}
	}
}

func validateExecutable(executable string) error {
	if executable == "" {
		return fmt.Errorf("service executable is required")
	}
	return nil
}
