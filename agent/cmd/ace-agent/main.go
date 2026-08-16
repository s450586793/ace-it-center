package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	agentclient "aceitcenter.local/platform/agent/internal/agent"
	"aceitcenter.local/platform/agent/internal/agentpaths"
	"aceitcenter.local/platform/agent/internal/app"
	"aceitcenter.local/platform/agent/internal/buildinfo"
	agentcommand "aceitcenter.local/platform/agent/internal/command"
	"aceitcenter.local/platform/agent/internal/controller"
	"aceitcenter.local/platform/agent/internal/diagnostics"
	"aceitcenter.local/platform/agent/internal/ipc"
	"aceitcenter.local/platform/agent/internal/logging"
	agenttray "aceitcenter.local/platform/agent/internal/tray"
	"aceitcenter.local/platform/agent/internal/update"
	"aceitcenter.local/platform/agent/internal/windowsservice"
	"aceitcenter.local/platform/internal/core"
	"aceitcenter.local/platform/internal/security"
)

func main() {
	if shouldAttachConsole(runtime.GOOS, os.Args[1:]) {
		agenttray.AttachParentConsole()
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	mode, args, err := app.ParseMode(runtime.GOOS, os.Args[1:])
	if err != nil {
		logger.Error("parse agent mode", "error", err)
		os.Exit(2)
	}
	switch mode {
	case app.ModeForeground:
		if err := runForeground(logger, args); err != nil {
			logger.Error("run foreground agent", "error", err)
			os.Exit(1)
		}
	case app.ModeService:
		if err := runService(args); err != nil {
			logger.Error("run Windows Service command", "error", err)
			os.Exit(1)
		}
	case app.ModeTray:
		if err := runTray(args); err != nil {
			logger.Error("run Windows tray", "error", err)
			os.Exit(1)
		}
	default:
		logger.Error("agent mode is not implemented", "mode", mode)
	}
}

func shouldAttachConsole(goos string, args []string) bool {
	if goos != "windows" || len(args) == 0 || args[0] == "tray" {
		return false
	}
	return args[0] != "service" || len(args) > 1
}

func runTray(args []string) error {
	showOnStart := false
	switch len(args) {
	case 0:
	case 1:
		if args[0] != "--show" {
			return errors.New("tray mode only accepts --show")
		}
		showOnStart = true
	default:
		return errors.New("tray mode does not accept arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return agenttray.Run(ctx, nil, agenttray.Options{
		Dial:         ipc.DialWindows,
		LogDirectory: filepath.Dir(agentpaths.AgentLogPath(defaultAgentConfigPath())),
		ShowOnStart:  showOnStart,
	})
}

func runService(args []string) error {
	switch len(args) {
	case 0:
		return runWindowsService()
	case 1:
		switch args[0] {
		case "install":
			executable, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate service executable: %w", err)
			}
			return windowsservice.Install(executable)
		case "stop":
			return windowsservice.Stop()
		case "uninstall":
			return windowsservice.Uninstall()
		}
	}
	return errors.New("unknown service command")
}

func runWindowsService() error {
	logger, closer, err := logging.New(logging.Options{
		Path:  agentpaths.AgentLogPath(defaultAgentConfigPath()),
		Level: slog.LevelInfo,
	})
	if err != nil {
		return fmt.Errorf("configure service logging: %w", err)
	}
	defer closer.Close()

	if err := windowsservice.Run(context.Background(), newServiceController(defaultAgentConfigPath(), logger)); err != nil {
		logger.Error("Windows Service stopped unexpectedly")
		return err
	}
	return nil
}

func newServiceController(configPath string, logger *slog.Logger) *controller.Controller {
	var runtimeController *controller.Controller
	worker := serviceWorker{
		logger:     logger,
		configPath: configPath,
		statusSink: func(snapshot app.StatusSnapshot) {
			runtimeController.ReportStatus(snapshot)
		},
	}
	runtimeController = controller.New(controller.Dependencies{
		ConfigPath: configPath,
		Enroller:   serviceEnroller{},
		Pairer:     servicePairer{},
		Worker:     worker,
		RunUpdate: func(ctx context.Context, config agentclient.Config, authorize controller.UpdateLaunchAuthorizer) (controller.UpdateStatus, error) {
			currentOS, err := update.CurrentOSVersion()
			if err != nil {
				return controller.UpdateStatus{}, fmt.Errorf("read Windows version: %w", err)
			}
			executable, err := os.Executable()
			if err != nil {
				return controller.UpdateStatus{}, fmt.Errorf("locate Agent executable: %w", err)
			}
			stagingDirectory := agentpaths.StagingDirectory(configPath)
			return executeServiceUpdate(ctx, serviceUpdateRuntime{
				client: update.ProcessClient{AgentPath: executable},
				checkOptions: update.CheckOptions{
					Origin:         config.ServerURL,
					CurrentVersion: buildinfo.Version,
					CurrentOS:      currentOS,
					StagingDir:     stagingDirectory,
				},
				backupPath: filepath.Join(stagingDirectory, "AceAgent.lkg.exe"),
				authorize:  authorize,
			})
		},
		LogBootstrapFailure: func() {
			logger.Error("service configuration is unavailable")
		},
	})
	return runtimeController
}

type serviceUpdateClient interface {
	Check(context.Context, update.CheckOptions) (update.CheckResult, error)
	LaunchApply(context.Context, update.ApplyOptions) error
}

type serviceUpdateRuntime struct {
	client       serviceUpdateClient
	checkOptions update.CheckOptions
	backupPath   string
	authorize    controller.UpdateLaunchAuthorizer
}

func executeServiceUpdate(ctx context.Context, runtime serviceUpdateRuntime) (controller.UpdateStatus, error) {
	if runtime.client == nil || runtime.authorize == nil || runtime.backupPath == "" {
		return controller.UpdateStatus{}, errors.New("update runtime is incomplete")
	}
	result, err := runtime.client.Check(ctx, runtime.checkOptions)
	if err != nil {
		return controller.UpdateStatus{}, err
	}
	if !result.Available {
		return controller.UpdateStatus{}, nil
	}
	applyOptions := update.ApplyOptions{
		InstallerPath: result.InstallerPath,
		BackupPath:    runtime.backupPath,
		Version:       result.Version,
	}
	finish, err := runtime.authorize()
	if err != nil {
		_ = os.Remove(result.InstallerPath)
		return controller.UpdateStatus{}, fmt.Errorf("authorize fixed updater launch: %w", err)
	}
	status := controller.UpdateStatus{Available: true, Version: result.Version, URL: result.URL}
	if err := runtime.client.LaunchApply(ctx, applyOptions); err != nil {
		finish(controller.UpdateStatus{}, false)
		_ = os.Remove(result.InstallerPath)
		return controller.UpdateStatus{}, fmt.Errorf("launch fixed updater: %w", err)
	}
	finish(status, true)
	return status, nil
}

type serviceEnroller struct{}

func (serviceEnroller) Enroll(ctx context.Context, serverURL, token string) (controller.EnrollmentResult, error) {
	client, err := agentclient.NewClient(serverURL, &http.Client{Timeout: 20 * time.Second})
	if err != nil {
		return controller.EnrollmentResult{}, err
	}
	request, _, err := agentclient.Collect(buildinfo.Version)
	if err != nil {
		return controller.EnrollmentResult{}, err
	}
	request.Token = token
	result, err := client.Enroll(ctx, request)
	if err != nil {
		return controller.EnrollmentResult{}, err
	}
	return controller.EnrollmentResult{NodeID: result.Node.ID, Credential: result.Credential}, nil
}

type servicePairer struct{}

func (servicePairer) StartPairing(ctx context.Context, serverURL string) (controller.PairingStartResult, error) {
	client, err := agentclient.NewClient(serverURL, &http.Client{Timeout: 20 * time.Second})
	if err != nil {
		return controller.PairingStartResult{}, err
	}
	inventory, _, err := agentclient.Collect(buildinfo.Version)
	if err != nil {
		return controller.PairingStartResult{}, fmt.Errorf("collect pairing inventory: %w", err)
	}
	credential, _, err := security.NewOpaqueToken()
	if err != nil {
		return controller.PairingStartResult{}, fmt.Errorf("generate pairing credential: %w", err)
	}
	result, pollAfter, err := client.StartPairing(ctx, core.PairingCreateRequest{
		Hostname:          inventory.Hostname,
		Type:              inventory.Type,
		AgentVersion:      inventory.Version,
		MachineID:         inventory.MachineID,
		PairingCredential: credential,
	})
	if err != nil {
		return controller.PairingStartResult{}, err
	}
	return controller.PairingStartResult{PairingID: result.ID, Credential: credential, ExpiresAt: result.ExpiresAt, PollAfter: pollAfter}, nil
}

func (servicePairer) PollPairing(ctx context.Context, pending agentclient.PendingPairing) (core.PairingPollResult, error) {
	client, err := agentclient.NewClient(pending.ServerURL, &http.Client{Timeout: 20 * time.Second})
	if err != nil {
		return core.PairingPollResult{}, err
	}
	return client.PollPairing(ctx, pending.PairingID, pending.Credential)
}

type serviceWorker struct {
	logger         *slog.Logger
	configPath     string
	statusSink     app.StatusSink
	executablePath string
	promoteUpdater func(context.Context, update.PromotionOptions) error
}

const uploadedLogMaxBytes int64 = 64 << 10

type serviceLogUploadClient interface {
	UploadLogs(context.Context, string, string, string) error
}

func uploadServiceLogs(ctx context.Context, client serviceLogUploadClient, config agentclient.Config, agentLogPath, updateLogPath string) error {
	if client == nil {
		return errors.New("log upload client is required")
	}
	secrets := []string{config.Credential}
	if config.PendingPairing != nil {
		secrets = append(secrets, config.PendingPairing.Credential)
	}
	agentLog, err := diagnostics.ReadLogTail(agentLogPath, uploadedLogMaxBytes, secrets)
	if err != nil {
		return fmt.Errorf("read service log: %w", err)
	}
	updateLog, err := diagnostics.ReadLogTail(updateLogPath, uploadedLogMaxBytes, secrets)
	if err != nil {
		return fmt.Errorf("read update log: %w", err)
	}
	if err := client.UploadLogs(ctx, config.Credential, string(agentLog), string(updateLog)); err != nil {
		return fmt.Errorf("upload logs: %w", err)
	}
	return nil
}

func (w serviceWorker) Run(ctx context.Context, config agentclient.Config, interval time.Duration) error {
	client, err := agentclient.NewClient(config.ServerURL, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	collector := agentclient.NewHostCollectorWithNetworkUsage(networkUsagePath(w.configPath), func(err error) {
		if w.logger != nil {
			w.logger.Warn("network usage state", "error", err)
		}
	})
	dependencies := app.Dependencies{
		Client:  client,
		Collect: collector.Collect,
		Version: buildinfo.Version,
		LogUploader: func(ctx context.Context, config agentclient.Config) error {
			return uploadServiceLogs(ctx, client, config, agentpaths.AgentLogPath(defaultAgentConfigPath()), agentpaths.UpdateLogPath(defaultAgentConfigPath()))
		},
		LogErrorSink: func(message string) {
			if w.logger != nil {
				w.logger.Warn("upload service logs", "error", message)
			}
		},
		StatusSink: func(snapshot app.StatusSnapshot) {
			if w.statusSink != nil {
				w.statusSink(snapshot)
			}
			logServiceHeartbeatState(w.logger, snapshot)
		},
	}
	runner, supported := agentcommand.NewPlatformRunner()
	dependencies.CommandLoop = newServiceCommandLoop(client, runner, supported, w.logger)
	dependencies.CommandErrorSink = func(message string) {
		if w.logger != nil {
			w.logger.Warn("command worker stopped", "error", message)
		}
	}
	worker := app.NewWorker(dependencies)
	w.startUpdaterMaintenance(ctx)
	return worker.Run(ctx, config, interval)
}

func (w serviceWorker) startUpdaterMaintenance(ctx context.Context) {
	executablePath := w.executablePath
	if executablePath == "" {
		var err error
		executablePath, err = os.Executable()
		if err != nil {
			if w.logger != nil {
				w.logger.Warn("updater maintenance unavailable", "stage", "locate_agent")
			}
			return
		}
	}
	promote := w.promoteUpdater
	if promote == nil {
		promote = update.PromotePendingUpdater
	}
	options := update.PromotionOptions{
		AgentVersion:       buildinfo.Version,
		InstalledPath:      agentpaths.UpdaterPath(executablePath),
		PendingPath:        agentpaths.PendingUpdaterPath(executablePath),
		StagingDirectory:   agentpaths.StagingDirectory(w.configPath),
		CurrentProcessPath: executablePath,
	}
	go func() {
		if err := promote(ctx, options); err != nil && ctx.Err() == nil && w.logger != nil {
			w.logger.Warn("updater maintenance failed", "stage", "promotion")
		}
	}()
}

func logServiceHeartbeatState(logger *slog.Logger, snapshot app.StatusSnapshot) {
	if logger == nil {
		return
	}
	switch snapshot.State {
	case app.StateOnline:
		logger.Info("service heartbeat state", "state", snapshot.State, "node_id", snapshot.NodeID)
	case app.StateError:
		logger.Warn("service heartbeat state", "state", snapshot.State, "node_id", snapshot.NodeID, "error", snapshot.Error)
	}
}

func newServiceCommandLoop(
	client app.CommandClient,
	runner agentcommand.Runner,
	supported bool,
	logger *slog.Logger,
) app.CommandLoop {
	if !supported || client == nil || runner == nil {
		return nil
	}
	commandWorker := app.NewCommandWorker(app.CommandWorkerOptions{
		Client:   client,
		Executor: agentcommand.NewExecutor(runner),
		ErrorSink: func(message string) {
			if logger != nil {
				logger.Warn("command channel retry", "error", message)
			}
		},
	})
	return commandWorker.Run
}

func runForeground(logger *slog.Logger, args []string) error {
	serverURL := flag.String("server", "", "Ace IT Center server URL")
	enrollmentToken := flag.String("enrollment", "", "one-time enrollment token")
	configPath := flag.String("config", defaultAgentConfigPath(), "agent configuration path")
	once := flag.Bool("once", false, "send one heartbeat and exit")
	interval := flag.Duration("interval", 30*time.Second, "heartbeat interval")
	if err := flag.CommandLine.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	config, loadErr := agentclient.LoadConfig(*configPath)
	lifecycle := newServiceController(*configPath, logger)
	handled, err := runForegroundLifecycleForConfig(ctx, config, loadErr, *serverURL, *enrollmentToken, lifecycle)
	if err != nil {
		return err
	}
	if handled {
		if *once {
			return nil
		}
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return lifecycle.Shutdown(shutdownContext)
	}

	if *serverURL != "" {
		config.ServerURL = *serverURL
	}
	client, err := agentclient.NewClient(config.ServerURL, &http.Client{Timeout: 20 * time.Second})
	if err != nil {
		return fmt.Errorf("configure agent client: %w", err)
	}

	workerContext := ctx
	var stopWorker context.CancelFunc
	if *once {
		workerContext, stopWorker = context.WithCancel(ctx)
		defer stopWorker()
	}
	firstHeartbeatFailed := false
	collector := agentclient.NewHostCollectorWithNetworkUsage(networkUsagePath(*configPath), func(err error) {
		logger.Warn("network usage state", "error", err)
	})
	worker := app.NewWorker(app.Dependencies{
		Client:  client,
		Collect: collector.Collect,
		Version: buildinfo.Version,
		LogUploader: func(ctx context.Context, config agentclient.Config) error {
			return uploadServiceLogs(ctx, client, config, agentpaths.AgentLogPath(defaultAgentConfigPath()), agentpaths.UpdateLogPath(defaultAgentConfigPath()))
		},
		LogErrorSink: func(message string) {
			logger.Warn("upload service logs", "error", message)
		},
		StatusSink: func(snapshot app.StatusSnapshot) {
			switch snapshot.State {
			case app.StateOnline:
				logger.Info("heartbeat accepted", "node_id", snapshot.NodeID)
			case app.StateError:
				logger.Error("send heartbeat", "error", snapshot.Error)
			}
			if *once && (snapshot.State == app.StateOnline || snapshot.State == app.StateError) {
				firstHeartbeatFailed = snapshot.State == app.StateError
				stopWorker()
			}
		},
	})
	if err := worker.Run(workerContext, config, *interval); err != nil {
		return err
	}
	if firstHeartbeatFailed {
		return errors.New("first heartbeat failed")
	}
	logger.Info("agent stopped", "node_id", config.NodeID)
	return nil
}

type foregroundLifecycleController interface {
	Bootstrap(context.Context) error
	Enroll(context.Context, string, string) error
	StartPairing(context.Context, string) error
}

func runForegroundLifecycle(ctx context.Context, serverURL, enrollmentToken string, lifecycle foregroundLifecycleController) error {
	if lifecycle == nil {
		return errors.New("foreground lifecycle controller is required")
	}
	if err := lifecycle.Bootstrap(ctx); err != nil {
		return err
	}
	if serverURL == "" {
		return nil
	}
	if enrollmentToken != "" {
		return lifecycle.Enroll(ctx, serverURL, enrollmentToken)
	}
	return lifecycle.StartPairing(ctx, serverURL)
}

func runForegroundLifecycleForConfig(ctx context.Context, config agentclient.Config, loadErr error, serverURL, enrollmentToken string, lifecycle foregroundLifecycleController) (bool, error) {
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return false, fmt.Errorf("load agent configuration: %w", loadErr)
	}
	if loadErr == nil && !config.IsPendingPairing() {
		return false, nil
	}
	if err := runForegroundLifecycle(ctx, serverURL, enrollmentToken, lifecycle); err != nil {
		return true, err
	}
	return true, nil
}

func defaultAgentConfigPath() string {
	return agentpaths.DefaultConfigPath(runtime.GOOS, os.Getenv("ProgramData"))
}

func networkUsagePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "network-usage.json")
}
