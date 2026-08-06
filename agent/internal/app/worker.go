package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	agentclient "aceitcenter.local/platform/agent/internal/agent"
	"aceitcenter.local/platform/internal/core"
)

type State string

const (
	StateStarting State = "starting"
	StateOnline   State = "online"
	StateError    State = "error"
	StateStopped  State = "stopped"
)

type StatusSnapshot struct {
	State         State
	NodeID        string
	ServerURL     string
	Version       string
	LastHeartbeat time.Time
	Error         string
}

type StatusSink func(StatusSnapshot)

type HeartbeatClient interface {
	Heartbeat(ctx context.Context, credential string, heartbeat core.Heartbeat) error
}

type Collector func(version string) (core.EnrollRequest, core.Heartbeat, error)

type LogUploader func(context.Context, agentclient.Config) error

type LogErrorSink func(string)

type CommandLoop func(context.Context, agentclient.Config) error

const logUploadInterval = time.Hour

type Dependencies struct {
	Client           HeartbeatClient
	Collect          Collector
	Version          string
	StatusSink       StatusSink
	LogUploader      LogUploader
	LogErrorSink     LogErrorSink
	CommandLoop      CommandLoop
	CommandErrorSink LogErrorSink
}

type Worker struct {
	dependencies Dependencies
}

type ConfigurationError struct {
	Message string
}

func (e *ConfigurationError) Error() string {
	return e.Message
}

func NewWorker(dependencies Dependencies) *Worker {
	return &Worker{dependencies: dependencies}
}

// Run sends an immediate heartbeat and continues retrying until ctx is cancelled.
// Heartbeat and collection failures are reported through StatusSink rather than
// ending the worker, so transient network failures do not stop the Agent.
func (w *Worker) Run(ctx context.Context, config agentclient.Config, interval time.Duration) error {
	if err := w.validate(config, interval); err != nil {
		return err
	}
	var commandDone <-chan struct{}
	if w.dependencies.CommandLoop != nil {
		done := make(chan struct{})
		commandDone = done
		go func() {
			defer close(done)
			if err := w.dependencies.CommandLoop(ctx, config); err != nil && ctx.Err() == nil && w.dependencies.CommandErrorSink != nil {
				w.dependencies.CommandErrorSink(sanitizeError(err, config.Credential))
			}
		}()
	}

	lastHeartbeat := time.Time{}
	lastLogUpload := time.Time{}
	publish := func(state State, err error) {
		if w.dependencies.StatusSink == nil {
			return
		}
		w.dependencies.StatusSink(StatusSnapshot{
			State:         state,
			NodeID:        config.NodeID,
			ServerURL:     config.ServerURL,
			Version:       w.dependencies.Version,
			LastHeartbeat: lastHeartbeat,
			Error:         sanitizeError(err, config.Credential),
		})
	}
	sendHeartbeat := func() {
		_, heartbeat, err := w.dependencies.Collect(w.dependencies.Version)
		if err == nil {
			err = w.dependencies.Client.Heartbeat(ctx, config.Credential, heartbeat)
		}
		if err != nil {
			publish(StateError, err)
			return
		}
		now := time.Now().UTC()
		lastHeartbeat = now
		publish(StateOnline, nil)
		if w.dependencies.LogUploader != nil && (lastLogUpload.IsZero() || !now.Before(lastLogUpload.Add(logUploadInterval))) {
			lastLogUpload = now
			if err := w.dependencies.LogUploader(ctx, config); err != nil && w.dependencies.LogErrorSink != nil {
				w.dependencies.LogErrorSink(sanitizeError(err, config.Credential))
			}
		}
	}

	publish(StateStarting, nil)
	sendHeartbeat()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			publish(StateStopped, nil)
			if commandDone != nil {
				<-commandDone
			}
			return nil
		case <-ticker.C:
			sendHeartbeat()
		}
	}
}

func (w *Worker) validate(config agentclient.Config, interval time.Duration) error {
	switch {
	case w.dependencies.Client == nil:
		return &ConfigurationError{Message: "worker heartbeat client is required"}
	case w.dependencies.Collect == nil:
		return &ConfigurationError{Message: "worker collector is required"}
	case w.dependencies.Version == "":
		return &ConfigurationError{Message: "worker version is required"}
	case config.ServerURL == "":
		return &ConfigurationError{Message: "agent server URL is required"}
	case config.NodeID == "":
		return &ConfigurationError{Message: "agent node ID is required"}
	case config.Credential == "":
		return &ConfigurationError{Message: "agent credential is required"}
	case interval <= 0:
		return &ConfigurationError{Message: "heartbeat interval must be positive"}
	default:
		return nil
	}
}

func sanitizeError(err error, credential string) string {
	if err == nil {
		return ""
	}

	message := strings.Join(strings.Fields(err.Error()), " ")
	if credential != "" {
		message = strings.ReplaceAll(message, credential, "[redacted]")
	}
	const maxLength = 512
	if len(message) > maxLength {
		message = fmt.Sprintf("%s...", message[:maxLength])
	}
	return message
}
