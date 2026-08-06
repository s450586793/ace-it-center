//go:build windows

package windowsservice

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	stopTimeout = 15 * time.Second
	stopPoll    = 100 * time.Millisecond
)

func Install(executable string) error {
	if err := validateExecutable(executable); err != nil {
		return err
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer manager.Disconnect()
	return installWithManager(&windowsManager{manager: manager}, executable)
}

// RestoreServiceExecutable 恢复 Service 到指定的 Agent 可执行文件，且不更改 recovery 设置。
func RestoreServiceExecutable(executable string) error {
	if err := validateExecutable(executable); err != nil {
		return err
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer manager.Disconnect()
	return restoreServiceExecutableWithManager(&windowsManager{manager: manager}, executable)
}

// Stop stops the Agent Service for an in-place upgrade without deleting its SCM registration.
func Stop() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer manager.Disconnect()
	return stopWithManager(context.Background(), &windowsManager{manager: manager}, stopTimeout, stopPoll)
}

func Uninstall() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer manager.Disconnect()
	return uninstallWithManager(context.Background(), &windowsManager{manager: manager}, stopTimeout, stopPoll)
}

type windowsManager struct {
	manager *mgr.Mgr
}

func (m *windowsManager) OpenService(name string) (scmService, error) {
	service, err := m.manager.OpenService(name)
	if err != nil {
		return nil, mapSCMError(err)
	}
	return &windowsService{service: service}, nil
}

func (m *windowsManager) CreateService(name, executable string, configuration serviceConfig, arguments ...string) (scmService, error) {
	service, err := m.manager.CreateService(name, executable, toWindowsConfig(configuration), arguments...)
	if err != nil {
		return nil, mapSCMError(err)
	}
	return &windowsService{service: service}, nil
}

type windowsService struct {
	service *mgr.Service
}

func (s *windowsService) UpdateConfig(configuration serviceConfig) error {
	return s.service.UpdateConfig(toWindowsConfig(configuration))
}

func (s *windowsService) SetRecoveryActions(actions []recoveryAction) error {
	windowsActions := make([]mgr.RecoveryAction, len(actions))
	for index, action := range actions {
		windowsActions[index] = mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: action.Delay}
	}
	return s.service.SetRecoveryActions(windowsActions, 24*60*60)
}

func (s *windowsService) SetRecoveryActionsOnNonCrashFailures(enabled bool) error {
	return s.service.SetRecoveryActionsOnNonCrashFailures(enabled)
}

func (s *windowsService) Query() (serviceState, error) {
	status, err := s.service.Query()
	if err != nil {
		return 0, mapSCMError(err)
	}
	switch status.State {
	case svc.Stopped:
		return serviceStopped, nil
	case svc.StopPending:
		return serviceStopPending, nil
	default:
		return serviceRunning, nil
	}
}

func (s *windowsService) Stop() error {
	_, err := s.service.Control(svc.Stop)
	if err == windows.ERROR_SERVICE_NOT_ACTIVE {
		return nil
	}
	return mapSCMError(err)
}

func (s *windowsService) Delete() error {
	return mapSCMError(s.service.Delete())
}

func (s *windowsService) Close() error {
	return s.service.Close()
}

func toWindowsConfig(configuration serviceConfig) mgr.Config {
	return mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		DisplayName:      configuration.DisplayName,
		DelayedAutoStart: configuration.DelayedAutoStart,
		ServiceStartName: configuration.ServiceStartName,
		BinaryPathName:   windowsCommandLine(configuration.Executable, configuration.Arguments),
	}
}

func windowsCommandLine(executable string, arguments []string) string {
	command := `"` + executable + `"`
	for _, argument := range arguments {
		command += " " + argument
	}
	return command
}

func mapSCMError(err error) error {
	if err == nil {
		return nil
	}
	if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
		return errServiceMissing
	}
	if err == windows.ERROR_SERVICE_MARKED_FOR_DELETE {
		return errServiceMarkedForDelete
	}
	return err
}
