package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"runtime"

	"aceitcenter.local/platform/agent/internal/agentpaths"
	"aceitcenter.local/platform/agent/internal/buildinfo"
	"aceitcenter.local/platform/agent/internal/logging"
	"aceitcenter.local/platform/agent/internal/updaterapp"
)

func main() {
	configPath := agentpaths.DefaultConfigPath(runtime.GOOS, os.Getenv("ProgramData"))
	logger, closer, err := logging.New(logging.Options{
		Path:  agentpaths.UpdateLogPath(configPath),
		Level: slog.LevelInfo,
	})
	if err != nil {
		os.Exit(1)
	}
	defer closer.Close()

	exitCode := runUpdater(context.Background(), os.Args[1:], os.Stdout, updaterapp.Dependencies{
		UpdatePublicKey: buildinfo.UpdatePublicKey,
		Version:         buildinfo.Version,
	})
	logUpdaterResult(logger, os.Args[1:], exitCode)
	os.Exit(exitCode)
}

func runUpdater(ctx context.Context, args []string, stdout io.Writer, dependencies updaterapp.Dependencies) int {
	if err := updaterapp.Run(ctx, args, stdout, dependencies); err != nil {
		return 1
	}
	return 0
}

func safeCommandName(args []string) string {
	if len(args) == 0 {
		return "missing"
	}
	switch args[0] {
	case "check", "apply", "version":
		return args[0]
	default:
		return "unknown"
	}
}

func logUpdaterResult(logger *slog.Logger, args []string, exitCode int) {
	if logger == nil {
		return
	}
	command := safeCommandName(args)
	if exitCode == 0 {
		logger.Info("updater command completed", "command", command)
		return
	}
	logger.Error("updater command failed", "command", command)
}
