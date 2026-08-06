//go:build windows

package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
)

type windowsRunner struct{}

func NewPlatformRunner() (Runner, bool) {
	return windowsRunner{}, true
}

func (windowsRunner) Run(ctx context.Context, invocation Invocation, output io.Writer) (int, error) {
	command := exec.Command(invocation.Program, invocation.Args...)
	command.Stdout = output
	command.Stderr = output
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | createNoWindow,
		HideWindow:    true,
	}
	if err := command.Start(); err != nil {
		return -1, fmt.Errorf("start command process: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return processExitCode(command), err
	case <-ctx.Done():
		terminateErr := terminateProcessTree(command.Process.Pid)
		waitErr := <-done
		if terminateErr != nil {
			return processExitCode(command), fmt.Errorf("terminate command process tree: %w", terminateErr)
		}
		if waitErr != nil {
			return processExitCode(command), ctx.Err()
		}
		return processExitCode(command), ctx.Err()
	}
}

func terminateProcessTree(processID int) error {
	killContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kill := exec.CommandContext(killContext, "taskkill.exe", "/PID", strconv.Itoa(processID), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	if err := kill.Run(); err == nil {
		return nil
	}
	process, findErr := os.FindProcess(processID)
	if findErr != nil {
		return findErr
	}
	return process.Kill()
}

func processExitCode(command *exec.Cmd) int {
	if command.ProcessState == nil {
		return -1
	}
	return command.ProcessState.ExitCode()
}
