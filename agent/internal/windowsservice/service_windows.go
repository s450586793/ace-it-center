//go:build windows

package windowsservice

import (
	"context"
	"fmt"
	"time"

	"aceitcenter.local/platform/agent/internal/ipc"
	"golang.org/x/sys/windows/svc"
)

func Run(ctx context.Context, controller RuntimeController) error {
	if ctx == nil {
		return fmt.Errorf("service context is required")
	}
	if controller == nil {
		return fmt.Errorf("service controller is required")
	}
	return svc.Run(ServiceName, &handler{parent: ctx, controller: controller})
}

type handler struct {
	parent     context.Context
	controller RuntimeController
}

func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(h.parent)
	defer cancel()
	if err := h.controller.Bootstrap(ctx); err != nil {
		changes <- svc.Status{State: svc.Stopped}
		return false, 1
	}
	ready := make(chan error, 1)
	done := make(chan error, 1)
	go func() { done <- ipc.ListenWindowsReady(ctx, ipc.NewRouter(h.controller), ready) }()
	select {
	case err := <-ready:
		if err != nil {
			return h.stop(ctx, cancel, done, changes, 1)
		}
	case <-h.parent.Done():
		return h.stop(ctx, cancel, done, changes, 0)
	}
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case svc.Stop, svc.Shutdown:
				return h.stop(ctx, cancel, done, changes, 0)
			}
		case err := <-done:
			shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer shutdownCancel()
			shutdownErr := h.controller.Shutdown(shutdownContext)
			changes <- svc.Status{State: svc.Stopped}
			if shutdownErr != nil {
				return false, 1
			}
			if err != nil && h.parent.Err() == nil {
				return false, 1
			}
			return false, 0
		case <-h.parent.Done():
			return h.stop(ctx, cancel, done, changes, 0)
		}
	}
}

func (h *handler) stop(_ context.Context, cancel context.CancelFunc, done <-chan error, changes chan<- svc.Status, exitCode uint32) (bool, uint32) {
	changes <- svc.Status{State: svc.StopPending}
	cancel()
	<-done
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := h.controller.Shutdown(shutdownContext); err != nil {
		exitCode = 1
	}
	changes <- svc.Status{State: svc.Stopped}
	return false, exitCode
}
