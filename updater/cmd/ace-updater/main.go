package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"aceitcenter.local/platform/internal/systemupdate"
)

const (
	serverReadTimeout     = 5 * time.Second
	serverWriteTimeout    = 30 * time.Second
	serverShutdownTimeout = 10 * time.Second
)

type updaterService interface {
	systemupdate.UpdateManager
	Recover(context.Context) error
}

type updaterRuntime struct {
	build    func(context.Context, UpdaterConfig) (updaterService, http.Handler, error)
	serve    func(*http.Server) error
	shutdown func(*http.Server, context.Context) error
}

var productionUpdaterRuntime = updaterRuntime{
	build: buildProductionUpdater,
	serve: func(server *http.Server) error {
		return server.ListenAndServe()
	},
	shutdown: func(server *http.Server, context context.Context) error {
		return server.Shutdown(context)
	},
}

func main() {
	config, err := LoadUpdaterConfig()
	if err != nil {
		log.Print("ace-updater configuration invalid")
		os.Exit(1)
	}
	context, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(context, config); err != nil {
		log.Print("ace-updater stopped")
		os.Exit(1)
	}
}

// run starts the updater only after persisted update state has been recovered.
func run(rootContext context.Context, config UpdaterConfig) error {
	return runWithRuntime(rootContext, config, productionUpdaterRuntime)
}

func runWithRuntime(rootContext context.Context, config UpdaterConfig, runtime updaterRuntime) error {
	if rootContext == nil {
		return errors.New("updater root context is required")
	}
	service, handler, err := runtime.build(rootContext, config)
	if err != nil {
		return errors.New("configure updater")
	}
	if err := service.Recover(rootContext); err != nil {
		return errors.New("recover update state")
	}

	server := &http.Server{
		Addr:              config.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: serverReadTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
	}
	result := make(chan error, 1)
	go func() { result <- runtime.serve(server) }()

	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-rootContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		if err := runtime.shutdown(server, shutdownContext); err != nil {
			return errors.New("shutdown updater server")
		}
		if err := <-result; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func buildProductionUpdater(rootContext context.Context, config UpdaterConfig) (updaterService, http.Handler, error) {
	platform, err := systemupdate.NewCLIPlatform(systemupdate.PlatformConfig{
		ProjectName:       config.ComposeProject,
		ComposeFile:       config.ComposeFile,
		ComposeEnvFile:    config.ComposeEnvFile,
		StateDir:          filepath.Dir(config.StateFile),
		BackupDir:         config.BackupDir,
		BackendRepository: config.BackendRepository,
		WebRepository:     config.WebRepository,
		BackendHealthURL:  "http://backend:8080/api/v1/health",
		WebHealthURL:      "http://web/api/v1/health",
		HealthTimeout:     serverReadTimeout,
		HTTPClient:        &http.Client{Timeout: serverReadTimeout},
		PGHost:            config.PGHost,
		PGPort:            config.PGPort,
		PGDatabase:        config.PGDatabase,
		PGUser:            config.PGUser,
		PGPassword:        config.PGPassword,
	}, commandRunner{})
	if err != nil {
		return nil, nil, err
	}
	checker := systemupdate.NewChecker(
		&systemupdate.RegistryResolver{},
		platform,
		config.BackendRepository,
		config.WebRepository,
		time.Now,
	)
	manager, err := systemupdate.NewManager(systemupdate.ManagerOptions{
		Store:       systemupdate.NewFileStore(config.StateFile),
		Checker:     checker,
		Platform:    platform,
		RootContext: rootContext,
		Logger:      slog.Default(),
	})
	if err != nil {
		return nil, nil, err
	}
	handler, err := systemupdate.NewHTTPHandler(manager, config.Token)
	if err != nil {
		return nil, nil, err
	}
	return manager, handler, nil
}

type commandRunner struct{}

func (commandRunner) Run(context context.Context, environment []string, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(context, name, arguments...)
	if len(environment) > 0 {
		command.Env = append(os.Environ(), environment...)
	}
	return command.Output()
}

var _ systemupdate.CommandRunner = commandRunner{}
