package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
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
	case app.ModeUpdateHelper:
		if err := runUpdateHelper(logger, args); err != nil {
			logger.Error("run update helper", "error", err)
			os.Exit(1)
		}
	default:
		logger.Error("agent mode is not implemented", "mode", mode)
	}
}

func shouldAttachConsole(goos string, args []string) bool {
	if goos != "windows" || len(args) == 0 || args[0] == "tray" || args[0] == "update-helper" {
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
			checker := update.Checker{
				Origin:         config.ServerURL,
				CurrentVersion: buildinfo.Version,
				CurrentOS:      currentOS,
				PublicKey:      buildinfo.UpdatePublicKey,
				StagingDir:     stagingDirectory,
				Timeout:        30 * time.Second,
			}
			return executeServiceUpdate(ctx, serviceUpdateRuntime{
				checker:        checker,
				executablePath: executable,
				stagingDir:     stagingDirectory,
				launch:         update.LaunchHelper,
				authorize:      authorize,
			})
		},
		LogBootstrapFailure: func() {
			logger.Error("service configuration is unavailable")
		},
	})
	return runtimeController
}

type serviceUpdateChecker interface {
	Check(context.Context) (update.Candidate, error)
	Stage(context.Context, update.Candidate) (update.StagedUpdate, error)
}

type serviceUpdateRuntime struct {
	checker        serviceUpdateChecker
	executablePath string
	stagingDir     string
	launch         func(context.Context, update.LaunchOptions) error
	authorize      controller.UpdateLaunchAuthorizer
}

func executeServiceUpdate(ctx context.Context, runtime serviceUpdateRuntime) (controller.UpdateStatus, error) {
	if runtime.checker == nil || runtime.launch == nil || runtime.authorize == nil || runtime.executablePath == "" || runtime.stagingDir == "" {
		return controller.UpdateStatus{}, errors.New("update runtime is incomplete")
	}
	candidate, err := runtime.checker.Check(ctx)
	if err != nil {
		return controller.UpdateStatus{}, err
	}
	staged, err := runtime.checker.Stage(ctx, candidate)
	if err != nil {
		return controller.UpdateStatus{}, err
	}
	launchOptions := update.LaunchOptions{
		ExecutablePath: runtime.executablePath,
		InstallerPath:  staged.InstallerPath,
		BackupPath:     filepath.Join(runtime.stagingDir, "AceAgent.lkg.exe"),
		StagingDir:     runtime.stagingDir,
		Version:        staged.Version,
	}
	finish, err := runtime.authorize()
	if err != nil {
		_ = os.Remove(staged.InstallerPath)
		return controller.UpdateStatus{}, fmt.Errorf("authorize update helper launch: %w", err)
	}
	status := controller.UpdateStatus{Available: true, Version: staged.Version, URL: candidate.InstallerURL}
	if err := runtime.launch(ctx, launchOptions); err != nil {
		finish(controller.UpdateStatus{}, false)
		_ = os.Remove(staged.InstallerPath)
		return controller.UpdateStatus{}, fmt.Errorf("launch update helper: %w", err)
	}
	finish(status, true)
	return status, nil
}

func runUpdateHelper(_ *slog.Logger, args []string) error {
	logger, closer, err := logging.New(logging.Options{
		Path:  agentpaths.UpdateLogPath(defaultAgentConfigPath()),
		Level: slog.LevelInfo,
	})
	if err != nil {
		return fmt.Errorf("configure update logging: %w", err)
	}
	defer closer.Close()
	return runUpdateHelperWithRunner(context.Background(), args, defaultAgentConfigPath(), logger, update.RunHelper)
}

func runUpdateHelperWithRunner(ctx context.Context, args []string, configPath string, logger *slog.Logger, runner func(context.Context, update.HelperOptions) error) error {
	if runner == nil {
		return errors.New("update helper runner is required")
	}
	options, err := parseUpdateHelperOptions(args)
	if err != nil {
		return err
	}
	options = configureUpdateHelperOptions(options, configPath, func(error) {
		if logger != nil {
			logger.Warn("update helper cleanup deferred", "version", options.Version)
		}
	})
	if logger != nil {
		logger.Info("update helper started", "version", options.Version)
	}
	if err := runner(ctx, options); err != nil {
		if logger != nil {
			attributes := []any{"version", options.Version, "stage", updateFailureStage(err)}
			if recoveryStage := updateRecoveryFailureStage(err); recoveryStage != "" {
				attributes = append(attributes, "recovery_stage", recoveryStage)
			}
			logger.Error("update helper failed", attributes...)
		}
		return err
	}
	if logger != nil {
		logger.Info("update helper completed", "version", options.Version)
	}
	return nil
}

func updateFailureStage(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "validate running update helper"):
		return "helper_validation"
	case strings.Contains(message, "acquire update helper execution lock"):
		return "helper_lock"
	case strings.Contains(message, "stop Agent Service"):
		return "stop_service"
	case strings.Contains(message, "stop Agent tray"):
		return "stop_tray"
	case strings.Contains(message, "run silent installer"):
		return "installer"
	case strings.Contains(message, "configure updated Agent Service"):
		return "service_config"
	case strings.Contains(message, "start updated Agent Service"):
		return "start_service"
	case strings.Contains(message, "validate updated Agent health"):
		return "health_check"
	case strings.Contains(message, "store last-known-good Agent"):
		return "backup"
	default:
		return "unknown"
	}
}

func updateRecoveryFailureStage(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "stop failed updated Agent Service"):
		return "stop_service"
	case strings.Contains(message, "restore last-known-good Agent"):
		return "restore"
	case strings.Contains(message, "reapply last-known-good Agent Service configuration"):
		return "service_config"
	case strings.Contains(message, "restart last-known-good Agent Service"):
		return "start_service"
	case strings.Contains(message, "validate last-known-good Agent health"):
		return "health_check"
	default:
		return ""
	}
}

func configureUpdateHelperOptions(options update.HelperOptions, configPath string, warning func(error)) update.HelperOptions {
	options.StagingDir = agentpaths.StagingDirectory(configPath)
	options.CleanupWarning = warning
	return options
}

func parseUpdateHelperOptions(args []string) (update.HelperOptions, error) {
	flags := flag.NewFlagSet("update-helper", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options update.HelperOptions
	flags.StringVar(&options.InstallerPath, "installer", "", "staged installer path")
	flags.StringVar(&options.ExecutablePath, "executable", "", "installed Agent path")
	flags.StringVar(&options.BackupPath, "backup", "", "last-known-good Agent path")
	flags.StringVar(&options.Version, "version", "", "candidate version")
	if err := flags.Parse(args); err != nil {
		return update.HelperOptions{}, fmt.Errorf("parse update helper arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return update.HelperOptions{}, errors.New("update helper does not accept positional arguments")
	}
	if options.InstallerPath == "" || options.ExecutablePath == "" || options.BackupPath == "" || options.Version == "" {
		return update.HelperOptions{}, errors.New("update helper requires installer, executable, backup, and version")
	}
	return options, nil
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
	logger     *slog.Logger
	configPath string
	statusSink app.StatusSink
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
	return worker.Run(ctx, config, interval)
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
