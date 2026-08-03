package core

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxCommandBytes       = 32 << 10
	MaxCommandOutputBytes = 256 << 10
	MinCommandTimeout     = 10
	MaxCommandTimeout     = 1800
)

type CommandShell string

const (
	CommandShellPowerShell CommandShell = "powershell"
	CommandShellCMD        CommandShell = "cmd"
)

type CommandStatus string

const (
	CommandQueued    CommandStatus = "queued"
	CommandLeased    CommandStatus = "leased"
	CommandRunning   CommandStatus = "running"
	CommandSucceeded CommandStatus = "succeeded"
	CommandFailed    CommandStatus = "failed"
	CommandTimedOut  CommandStatus = "timed_out"
)

type CommandStatusCounts struct {
	Queued    int `json:"queued"`
	Leased    int `json:"leased"`
	Running   int `json:"running"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	TimedOut  int `json:"timed_out"`
}

type CommandTask struct {
	ID             string              `json:"id"`
	Shell          CommandShell        `json:"shell"`
	Command        string              `json:"command"`
	TimeoutSeconds int                 `json:"timeout_seconds"`
	CreatedBy      string              `json:"created_by"`
	RetriedFromID  *string             `json:"retried_from_id,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
	TargetCount    int                 `json:"target_count"`
	Counts         CommandStatusCounts `json:"counts"`
}

type CommandExecution struct {
	ID              string        `json:"id"`
	TaskID          string        `json:"task_id"`
	NodeID          string        `json:"node_id"`
	NodeName        string        `json:"node_name"`
	Status          CommandStatus `json:"status"`
	Attempt         int           `json:"attempt"`
	StartedAt       *time.Time    `json:"started_at"`
	FinishedAt      *time.Time    `json:"finished_at"`
	ExitCode        *int          `json:"exit_code"`
	Output          string        `json:"output"`
	OutputTruncated bool          `json:"output_truncated"`
	ErrorMessage    string        `json:"error_message"`
	DurationMS      *int64        `json:"duration_ms"`
}

type CommandTaskDetail struct {
	Task       CommandTask        `json:"task"`
	Executions []CommandExecution `json:"executions"`
}

type CommandClaim struct {
	ExecutionID    string       `json:"execution_id"`
	TaskID         string       `json:"task_id"`
	Shell          CommandShell `json:"shell"`
	Command        string       `json:"command"`
	TimeoutSeconds int          `json:"timeout_seconds"`
	LeaseToken     string       `json:"lease_token"`
	LeaseExpiresAt time.Time    `json:"lease_expires_at"`
}

type CommandCompletion struct {
	ExecutionID     string        `json:"-"`
	LeaseToken      string        `json:"lease_token"`
	Status          CommandStatus `json:"status"`
	ExitCode        *int          `json:"exit_code"`
	Output          string        `json:"output"`
	OutputTruncated bool          `json:"output_truncated"`
	ErrorMessage    string        `json:"error_message"`
	DurationMS      int64         `json:"duration_ms"`
}

func ValidateCommand(shell CommandShell, command string, timeoutSeconds int) error {
	switch shell {
	case CommandShellPowerShell, CommandShellCMD:
	default:
		return fmt.Errorf("unsupported command shell")
	}
	if !utf8.ValidString(command) {
		return fmt.Errorf("command must be valid UTF-8")
	}
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("command is required")
	}
	if len(command) > MaxCommandBytes {
		return fmt.Errorf("command exceeds %d bytes", MaxCommandBytes)
	}
	if timeoutSeconds < MinCommandTimeout || timeoutSeconds > MaxCommandTimeout {
		return fmt.Errorf("timeout_seconds must be between %d and %d", MinCommandTimeout, MaxCommandTimeout)
	}
	return nil
}

func (status CommandStatus) Terminal() bool {
	switch status {
	case CommandSucceeded, CommandFailed, CommandTimedOut:
		return true
	default:
		return false
	}
}
