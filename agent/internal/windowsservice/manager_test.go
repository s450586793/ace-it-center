package windowsservice

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestServiceConfigurationUsesRequiredSCMSettings(t *testing.T) {
	configuration := serviceConfiguration(`C:\AceAgent.exe`)

	if configuration.DisplayName != ServiceDisplayName {
		t.Fatalf("display name = %q, want %q", configuration.DisplayName, ServiceDisplayName)
	}
	if !configuration.AutomaticStart || !configuration.DelayedAutoStart {
		t.Fatalf("start settings = %#v", configuration)
	}
	if configuration.ServiceStartName != "LocalSystem" {
		t.Fatalf("service account = %q", configuration.ServiceStartName)
	}
}

func TestInstallWithManagerCreatesLocalSystemServiceAndRecovery(t *testing.T) {
	service := &fakeSCMService{}
	manager := &fakeSCMManager{openErr: errServiceMissing, service: service}

	err := installWithManager(manager, `C:\Program Files\Ace IT Center\AceAgent.exe`)

	if err != nil {
		t.Fatalf("installWithManager() error = %v", err)
	}
	if manager.createName != ServiceName || manager.createExecutable != `C:\Program Files\Ace IT Center\AceAgent.exe` {
		t.Fatalf("CreateService() = (%q, %q)", manager.createName, manager.createExecutable)
	}
	if len(manager.createArgs) != 1 || manager.createArgs[0] != "service" {
		t.Fatalf("CreateService args = %#v", manager.createArgs)
	}
	if manager.createConfig.ServiceStartName != "LocalSystem" || !manager.createConfig.DelayedAutoStart || !manager.createConfig.AutomaticStart {
		t.Fatalf("CreateService config = %#v", manager.createConfig)
	}
	if !service.recoveryOnNonCrash || len(service.recoveryActions) != 3 {
		t.Fatalf("recovery = %#v", service)
	}
}

func TestInstallWithManagerDoesNotReconfigureNewService(t *testing.T) {
	service := &fakeSCMService{updateErr: errors.New("invalid service type")}
	manager := &fakeSCMManager{openErr: errServiceMissing, service: service}

	err := installWithManager(manager, `C:\Program Files\Ace IT Center\AceAgent.exe`)

	if err != nil {
		t.Fatalf("installWithManager() error = %v", err)
	}
	if service.updateCalls != 0 {
		t.Fatalf("UpdateConfig calls after CreateService = %d, want 0", service.updateCalls)
	}
}

func TestInstallWithManagerPropagatesRecoveryFlagError(t *testing.T) {
	service := &fakeSCMService{recoveryFlagErr: errors.New("set flag")}
	manager := &fakeSCMManager{service: service}

	err := installWithManager(manager, `C:\AceAgent.exe`)

	if !errors.Is(err, service.recoveryFlagErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallWithManagerUpdatesExistingServiceBinaryAndArguments(t *testing.T) {
	service := &fakeSCMService{}
	manager := &fakeSCMManager{service: service}

	err := installWithManager(manager, `C:\Program Files\Ace IT Center\AceAgent.exe`)

	if err != nil {
		t.Fatalf("installWithManager() error = %v", err)
	}
	if service.updatedConfig.Executable != `C:\Program Files\Ace IT Center\AceAgent.exe` || !slices.Equal(service.updatedConfig.Arguments, []string{"service"}) {
		t.Fatalf("UpdateConfig() = %#v", service.updatedConfig)
	}
}

func TestRestoreServiceExecutableUpdatesServiceWithoutReconfiguringRecovery(t *testing.T) {
	service := &fakeSCMService{}
	manager := &fakeSCMManager{service: service}

	err := restoreServiceExecutableWithManager(manager, `C:\Program Files\Ace IT Center\AceAgent.exe`)

	if err != nil {
		t.Fatalf("restoreServiceExecutableWithManager() error = %v", err)
	}
	if service.updatedConfig.Executable != `C:\Program Files\Ace IT Center\AceAgent.exe` || !slices.Equal(service.updatedConfig.Arguments, []string{"service"}) {
		t.Fatalf("UpdateConfig() = %#v", service.updatedConfig)
	}
	if service.recoveryOnNonCrash || len(service.recoveryActions) != 0 {
		t.Fatalf("rollback changed service recovery settings: %#v", service)
	}
}

func TestRestoreServiceExecutableRecreatesMissingServiceWithoutRecoveryConfiguration(t *testing.T) {
	service := &fakeSCMService{updateErr: errors.New("redundant update failed")}
	manager := &fakeSCMManager{openErr: errServiceMissing, service: service}

	err := restoreServiceExecutableWithManager(manager, `C:\Program Files\Ace IT Center\AceAgent.exe`)

	if err != nil {
		t.Fatalf("restoreServiceExecutableWithManager() error = %v", err)
	}
	if manager.createName != ServiceName || manager.createExecutable != `C:\Program Files\Ace IT Center\AceAgent.exe` {
		t.Fatalf("CreateService() = (%q, %q)", manager.createName, manager.createExecutable)
	}
	if !slices.Equal(manager.createArgs, []string{"service"}) {
		t.Fatalf("CreateService args = %#v", manager.createArgs)
	}
	if service.updateCalls != 0 {
		t.Fatalf("UpdateConfig calls after CreateService = %d, want 0", service.updateCalls)
	}
	if service.recoveryOnNonCrash || len(service.recoveryActions) != 0 {
		t.Fatalf("rollback configured optional recovery settings: %#v", service)
	}
}

func TestInstallWithManagerRejectsEmptyExecutableBeforeSCM(t *testing.T) {
	manager := &fakeSCMManager{}

	err := installWithManager(manager, "")

	if err == nil || err.Error() != "service executable is required" {
		t.Fatalf("err = %v", err)
	}
	if manager.openCalls != 0 {
		t.Fatalf("SCM open calls = %d, want 0", manager.openCalls)
	}
}

func TestStopWithManagerStopsServiceWithoutDeletingRegistration(t *testing.T) {
	service := &fakeSCMService{states: []serviceState{serviceRunning, serviceStopped}}
	manager := &fakeSCMManager{service: service}

	err := stopWithManager(context.Background(), manager, time.Second, time.Millisecond)

	if err != nil {
		t.Fatalf("stopWithManager() error = %v", err)
	}
	if service.stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1", service.stopCalls)
	}
	if service.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0", service.deleteCalls)
	}
}

func TestStopWithManagerAcceptsMissingService(t *testing.T) {
	manager := &fakeSCMManager{openErr: errServiceMissing}

	if err := stopWithManager(context.Background(), manager, time.Second, time.Millisecond); err != nil {
		t.Fatalf("stopWithManager() error = %v", err)
	}
	if manager.openCalls != 1 {
		t.Fatalf("open calls = %d, want 1", manager.openCalls)
	}
}

func TestUninstallWithManagerWaitsThroughStopPendingAndMarkedForDelete(t *testing.T) {
	service := &fakeSCMService{
		states:    []serviceState{serviceStopPending, serviceStopped},
		deleteErr: errServiceMarkedForDelete,
	}
	manager := &fakeSCMManager{service: service, openResults: []error{nil, errServiceMarkedForDelete, errServiceMissing}}
	service.events = &manager.events

	err := uninstallWithManager(context.Background(), manager, time.Second, time.Millisecond)

	if err != nil {
		t.Fatalf("uninstallWithManager() error = %v", err)
	}
	if service.stopCalls != 0 {
		t.Fatalf("stop calls = %d, want 0", service.stopCalls)
	}
	if !slices.Equal(manager.events, []string{"open", "delete", "close", "open", "open"}) {
		t.Fatalf("SCM calls = %#v", manager.events)
	}
}

func TestUninstallWithManagerTimesOutWhileMarkedForDeletion(t *testing.T) {
	service := &fakeSCMService{states: []serviceState{serviceStopped}}
	manager := &fakeSCMManager{service: service, openResults: []error{nil}}
	service.events = &manager.events
	operationContext, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	err := uninstallWithManager(operationContext, manager, time.Second, time.Millisecond)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

type fakeSCMManager struct {
	openErr          error
	service          *fakeSCMService
	createName       string
	createExecutable string
	createConfig     serviceConfig
	createArgs       []string
	openCalls        int
	openResults      []error
	events           []string
}

func (m *fakeSCMManager) OpenService(string) (scmService, error) {
	m.openCalls++
	m.events = append(m.events, "open")
	if len(m.openResults) > 0 {
		err := m.openResults[0]
		m.openResults = m.openResults[1:]
		if err != nil {
			return nil, err
		}
	}
	if m.openErr != nil {
		return nil, m.openErr
	}
	return m.service, nil
}

func (m *fakeSCMManager) CreateService(name, executable string, config serviceConfig, args ...string) (scmService, error) {
	m.createName = name
	m.createExecutable = executable
	m.createConfig = config
	m.createArgs = args
	return m.service, nil
}

type fakeSCMService struct {
	states             []serviceState
	stopCalls          int
	deleteCalls        int
	deleteErr          error
	recoveryFlagErr    error
	recoveryOnNonCrash bool
	recoveryActions    []recoveryAction
	updatedConfig      serviceConfig
	updateErr          error
	updateCalls        int
	events             *[]string
}

func (s *fakeSCMService) UpdateConfig(configuration serviceConfig) error {
	s.updateCalls++
	s.updatedConfig = configuration
	return s.updateErr
}

func (s *fakeSCMService) SetRecoveryActions(actions []recoveryAction) error {
	s.recoveryActions = actions
	return nil
}

func (s *fakeSCMService) SetRecoveryActionsOnNonCrashFailures(enabled bool) error {
	s.recoveryOnNonCrash = enabled
	return s.recoveryFlagErr
}

func (s *fakeSCMService) Query() (serviceState, error) {
	if len(s.states) == 0 {
		return serviceRunning, nil
	}
	state := s.states[0]
	s.states = s.states[1:]
	return state, nil
}

func (s *fakeSCMService) Stop() error {
	s.stopCalls++
	return nil
}

func (s *fakeSCMService) Delete() error {
	s.deleteCalls++
	if s.events != nil {
		*s.events = append(*s.events, "delete")
	}
	return s.deleteErr
}

func (s *fakeSCMService) Close() error {
	if s.events != nil {
		*s.events = append(*s.events, "close")
	}
	return nil
}
