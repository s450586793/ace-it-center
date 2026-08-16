//go:build windows

package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"golang.org/x/sys/windows"
)

type windowsProcessRunner struct{}

func defaultProcessRunner() ProcessRunner { return windowsProcessRunner{} }

func (windowsProcessRunner) Run(ctx context.Context, executable string, arguments []string, maximumOutput int) ([]byte, error) {
	if maximumOutput <= 0 {
		return nil, errors.New("process output limit must be positive")
	}
	output := &boundedProcessBuffer{maximum: maximumOutput}
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Stdout = output
	command.Stderr = io.Discard
	command.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("updater process failed: %w", err)
	}
	if output.exceeded {
		return nil, fmt.Errorf("updater process output exceeds %d bytes", maximumOutput)
	}
	return append([]byte(nil), output.Bytes()...), nil
}

func (windowsProcessRunner) StartDetached(ctx context.Context, executable string, arguments []string, options DetachedLaunchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var creationFlags uint32
	if options.NewProcessGroup {
		creationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	}
	if options.Detached {
		creationFlags |= windows.DETACHED_PROCESS
	}
	if options.BreakawayFromJob {
		creationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	command := exec.Command(executable, arguments...)
	command.Stdout = nil
	command.Stderr = nil
	command.SysProcAttr = &windows.SysProcAttr{CreationFlags: creationFlags, HideWindow: true}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
