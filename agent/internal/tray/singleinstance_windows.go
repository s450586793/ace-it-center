//go:build windows

package tray

import (
	"context"
	"errors"
	"fmt"

	"aceitcenter.local/platform/agent/internal/ipc"
	"golang.org/x/sys/windows"
)

const (
	trayMutexName      = `Local\AceITCenterAgentTray.v1`
	trayActivationName = `Local\AceITCenterAgentTray.Activate.v1`
)

type singleInstance struct {
	mutex      windows.Handle
	activation windows.Handle
	update     windows.Handle
}

func acquireSingleInstance() (*singleInstance, bool, error) {
	activationName, err := windows.UTF16PtrFromString(trayActivationName)
	if err != nil {
		return nil, false, fmt.Errorf("encode tray activation name: %w", err)
	}
	activation, err := windows.CreateEvent(nil, 0, 0, activationName)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, false, fmt.Errorf("create tray activation event: %w", err)
	}
	updateName, err := windows.UTF16PtrFromString(ipc.WindowsTrayUpdateEventName)
	if err != nil {
		windows.CloseHandle(activation)
		return nil, false, fmt.Errorf("encode tray update event name: %w", err)
	}
	update, err := windows.CreateEvent(nil, 0, 0, updateName)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(activation)
		return nil, false, fmt.Errorf("create tray update event: %w", err)
	}

	mutexName, err := windows.UTF16PtrFromString(trayMutexName)
	if err != nil {
		windows.CloseHandle(update)
		windows.CloseHandle(activation)
		return nil, false, fmt.Errorf("encode tray mutex name: %w", err)
	}
	mutex, mutexErr := windows.CreateMutex(nil, false, mutexName)
	if errors.Is(mutexErr, windows.ERROR_ALREADY_EXISTS) {
		signalErr := signalExistingInstance(func() error { return windows.SetEvent(activation) })
		windows.CloseHandle(mutex)
		windows.CloseHandle(update)
		windows.CloseHandle(activation)
		if signalErr != nil {
			return nil, false, signalErr
		}
		return nil, false, nil
	}
	if mutexErr != nil {
		windows.CloseHandle(update)
		windows.CloseHandle(activation)
		return nil, false, fmt.Errorf("create tray mutex: %w", mutexErr)
	}
	return &singleInstance{mutex: mutex, activation: activation, update: update}, true, nil
}

func (i *singleInstance) wake() {
	if i != nil {
		_ = windows.SetEvent(i.activation)
	}
}

func (i *singleInstance) Close() {
	if i == nil {
		return
	}
	windows.CloseHandle(i.activation)
	windows.CloseHandle(i.update)
	windows.CloseHandle(i.mutex)
}

func (i *singleInstance) waitForActivation(ctx context.Context) bool {
	return waitForEvent(ctx, i.activation)
}

func (i *singleInstance) waitForUpdate(ctx context.Context) bool {
	return waitForEvent(ctx, i.update)
}

func waitForEvent(ctx context.Context, event windows.Handle) bool {
	for ctx.Err() == nil {
		result, err := windows.WaitForSingleObject(event, 500)
		if err != nil {
			return false
		}
		if result == windows.WAIT_OBJECT_0 {
			return ctx.Err() == nil
		}
		if result != uint32(windows.WAIT_TIMEOUT) {
			return false
		}
	}
	return false
}
