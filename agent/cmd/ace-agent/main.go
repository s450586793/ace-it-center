package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	agentclient "aceitcenter.local/platform/agent/internal/agent"
)

const version = "0.1.0"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	serverURL := flag.String("server", "", "Ace IT Center server URL")
	enrollmentToken := flag.String("enrollment", "", "one-time enrollment token")
	configPath := flag.String("config", defaultConfigPath(), "agent configuration path")
	once := flag.Bool("once", false, "send one heartbeat and exit")
	interval := flag.Duration("interval", 30*time.Second, "heartbeat interval")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	config, err := agentclient.LoadConfig(*configPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Error("load agent configuration", "error", err)
			os.Exit(1)
		}
		if *serverURL == "" || *enrollmentToken == "" {
			logger.Error("first run requires -server and -enrollment")
			os.Exit(1)
		}
		client, err := agentclient.NewClient(*serverURL, &http.Client{Timeout: 20 * time.Second})
		if err != nil {
			logger.Error("configure agent client", "error", err)
			os.Exit(1)
		}
		request, _, err := agentclient.Collect(version)
		if err != nil {
			logger.Error("collect enrollment inventory", "error", err)
			os.Exit(1)
		}
		request.Token = *enrollmentToken
		result, err := client.Enroll(ctx, request)
		if err != nil {
			logger.Error("enroll agent", "error", err)
			os.Exit(1)
		}
		config = agentclient.Config{ServerURL: *serverURL, NodeID: result.Node.ID, Credential: result.Credential}
		if err := agentclient.SaveConfig(*configPath, config); err != nil {
			logger.Error("save agent configuration", "error", err)
			os.Exit(1)
		}
		logger.Info("agent enrolled", "node_id", config.NodeID)
	}

	if *serverURL != "" {
		config.ServerURL = *serverURL
	}
	client, err := agentclient.NewClient(config.ServerURL, &http.Client{Timeout: 20 * time.Second})
	if err != nil {
		logger.Error("configure agent client", "error", err)
		os.Exit(1)
	}

	sendHeartbeat := func() bool {
		_, heartbeat, err := agentclient.Collect(version)
		if err != nil {
			logger.Error("collect heartbeat", "error", err)
			return false
		}
		if err := client.Heartbeat(ctx, config.Credential, heartbeat); err != nil {
			logger.Error("send heartbeat", "error", err)
			return false
		}
		logger.Info("heartbeat accepted", "node_id", config.NodeID)
		return true
	}

	if !sendHeartbeat() && *once {
		os.Exit(1)
	}
	if *once {
		return
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("agent stopped", "node_id", config.NodeID)
			return
		case <-ticker.C:
			sendHeartbeat()
		}
	}
}

func defaultConfigPath() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "AceITCenter", "agent.json")
	}
	return "/etc/ace-it-center/agent.json"
}
