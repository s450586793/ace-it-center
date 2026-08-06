package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"aceitcenter.local/platform/internal/systemupdate"
)

func TestRunRecoversBeforeStartingServerAndSetsTimeouts(t *testing.T) {
	manager := &fakeProcessManager{}
	started := false
	runtime := updaterRuntime{
		build: func(rootContext context.Context, _ UpdaterConfig) (updaterService, http.Handler, error) {
			manager.rootContext = rootContext
			return manager, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
		},
		serve: func(server *http.Server) error {
			if !manager.recovered {
				t.Fatal("server started before recovery")
			}
			started = true
			if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 5*time.Second || server.WriteTimeout != 30*time.Second {
				t.Fatalf("server timeouts = header:%s read:%s write:%s", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout)
			}
			if server.Addr != ":18090" {
				t.Fatalf("server Addr = %q", server.Addr)
			}
			return http.ErrServerClosed
		},
		shutdown: func(*http.Server, context.Context) error { return nil },
	}

	if err := runWithRuntime(context.Background(), UpdaterConfig{ListenAddr: ":18090"}, runtime); err != nil {
		t.Fatalf("runWithRuntime() error = %v", err)
	}
	if !manager.recovered || !started {
		t.Fatalf("recovered=%v started=%v", manager.recovered, started)
	}
}

func TestRunDoesNotListenWhenRecoveryFails(t *testing.T) {
	manager := &fakeProcessManager{recoverErr: errors.New("docker raw registry secret")}
	listenCalls := 0
	runtime := updaterRuntime{
		build: func(rootContext context.Context, _ UpdaterConfig) (updaterService, http.Handler, error) {
			manager.rootContext = rootContext
			return manager, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
		},
		serve: func(*http.Server) error {
			listenCalls++
			return nil
		},
		shutdown: func(*http.Server, context.Context) error { return nil },
	}

	err := runWithRuntime(context.Background(), UpdaterConfig{}, runtime)
	if err == nil {
		t.Fatal("runWithRuntime() succeeded after recovery failure")
	}
	if listenCalls != 0 {
		t.Fatalf("listener calls = %d, want 0", listenCalls)
	}
	if strings.Contains(err.Error(), "raw registry secret") {
		t.Fatalf("recovery error leaked details: %v", err)
	}
}

func TestRunShutsDownWithinTenSecondsWhenRootContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := &fakeProcessManager{}
	started := make(chan struct{})
	stopped := make(chan struct{})
	shutdownCalled := make(chan time.Duration, 1)
	runtime := updaterRuntime{
		build: func(rootContext context.Context, _ UpdaterConfig) (updaterService, http.Handler, error) {
			manager.rootContext = rootContext
			return manager, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
		},
		serve: func(*http.Server) error {
			close(started)
			<-stopped
			return http.ErrServerClosed
		},
		shutdown: func(_ *http.Server, shutdownContext context.Context) error {
			select {
			case <-manager.rootContext.Done():
			default:
				t.Fatal("manager root context was not cancelled")
			}
			deadline, ok := shutdownContext.Deadline()
			if !ok {
				t.Fatal("shutdown context has no deadline")
			}
			shutdownCalled <- time.Until(deadline)
			close(stopped)
			return nil
		},
	}

	result := make(chan error, 1)
	go func() { result <- runWithRuntime(ctx, UpdaterConfig{}, runtime) }()
	<-started
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("runWithRuntime() error = %v", err)
	}
	remaining := <-shutdownCalled
	if remaining > 10*time.Second || remaining < 9*time.Second {
		t.Fatalf("shutdown deadline remaining = %s, want approximately 10s", remaining)
	}
}

type fakeProcessManager struct {
	recovered   bool
	recoverErr  error
	rootContext context.Context
}

func (manager *fakeProcessManager) Recover(context.Context) error {
	manager.recovered = true
	return manager.recoverErr
}

func (manager *fakeProcessManager) Status() systemupdate.StatusView {
	return systemupdate.StatusView{}
}

func (manager *fakeProcessManager) Check(context.Context) (systemupdate.StatusView, error) {
	return systemupdate.StatusView{}, nil
}

func (manager *fakeProcessManager) Start(context.Context, string) (systemupdate.TaskView, error) {
	return systemupdate.TaskView{}, nil
}

var _ updaterService = (*fakeProcessManager)(nil)
