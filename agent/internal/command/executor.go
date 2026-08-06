package command

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"aceitcenter.local/platform/internal/core"
)

type Invocation struct {
	Program string
	Args    []string
}

type Runner interface {
	Run(context.Context, Invocation, io.Writer) (int, error)
}

type Executor struct {
	runner  Runner
	timeout func(int) time.Duration
}

func NewExecutor(runner Runner) *Executor {
	return &Executor{
		runner: runner,
		timeout: func(seconds int) time.Duration {
			return time.Duration(seconds) * time.Second
		},
	}
}

func (e *Executor) Execute(ctx context.Context, claim core.CommandClaim) core.CommandCompletion {
	startedAt := time.Now()
	result := core.CommandCompletion{Status: core.CommandFailed}
	if err := core.ValidateCommand(claim.Shell, claim.Command, claim.TimeoutSeconds); err != nil {
		result.ErrorMessage = boundedUTF8(err.Error(), 512)
		return result
	}
	if e == nil || e.runner == nil || e.timeout == nil {
		result.ErrorMessage = "command executor is unavailable"
		return result
	}
	invocation, err := commandInvocation(claim.Shell, claim.Command)
	if err != nil {
		result.ErrorMessage = boundedUTF8(err.Error(), 512)
		return result
	}

	commandContext, cancel := context.WithTimeout(ctx, e.timeout(claim.TimeoutSeconds))
	defer cancel()
	output := newBoundedOutputWriter(core.MaxCommandOutputBytes)
	exitCode, runErr := e.runner.Run(commandContext, invocation, output)
	result.DurationMS = time.Since(startedAt).Milliseconds()
	result.Output, result.OutputTruncated = normalizedOutput(output)
	if exitCode >= 0 {
		result.ExitCode = &exitCode
	}

	switch {
	case commandContext.Err() == context.DeadlineExceeded:
		result.Status = core.CommandTimedOut
		result.ErrorMessage = fmt.Sprintf("command timed out after %d seconds", claim.TimeoutSeconds)
	case commandContext.Err() == context.Canceled:
		result.Status = core.CommandFailed
		result.ErrorMessage = "command execution cancelled"
	case runErr != nil || exitCode != 0:
		result.Status = core.CommandFailed
		if exitCode >= 0 {
			result.ErrorMessage = fmt.Sprintf("process exited with code %d", exitCode)
		} else if runErr != nil {
			result.ErrorMessage = boundedUTF8(runErr.Error(), 512)
		} else {
			result.ErrorMessage = "command process failed"
		}
	default:
		result.Status = core.CommandSucceeded
	}
	return result
}

func commandInvocation(shell core.CommandShell, command string) (Invocation, error) {
	switch shell {
	case core.CommandShellPowerShell:
		return Invocation{
			Program: "powershell.exe",
			Args: []string{
				"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command,
			},
		}, nil
	case core.CommandShellCMD:
		return Invocation{Program: "cmd.exe", Args: []string{"/D", "/S", "/C", command}}, nil
	default:
		return Invocation{}, fmt.Errorf("unsupported command shell")
	}
}

type boundedOutputWriter struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func newBoundedOutputWriter(limit int) *boundedOutputWriter {
	return &boundedOutputWriter{limit: limit}
}

func (w *boundedOutputWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	originalLength := len(value)
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		if originalLength > 0 {
			w.truncated = true
		}
		return originalLength, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		w.truncated = true
	}
	_, _ = w.buffer.Write(value)
	return originalLength, nil
}

func (w *boundedOutputWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}

func (w *boundedOutputWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

func normalizedOutput(writer *boundedOutputWriter) (string, bool) {
	value := strings.ToValidUTF8(string(writer.Bytes()), "\uFFFD")
	truncated := writer.Truncated()
	if len(value) > core.MaxCommandOutputBytes {
		value = boundedUTF8(value, core.MaxCommandOutputBytes)
		truncated = true
	}
	return value, truncated
}

func boundedUTF8(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
