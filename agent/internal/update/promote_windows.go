//go:build windows

package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

type windowsPromotionOperations struct{}

func defaultPromotionOperations() PromotionOperations { return windowsPromotionOperations{} }

func (windowsPromotionOperations) Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }

func (windowsPromotionOperations) RunVersion(ctx context.Context, executable string, maximumOutput int) (string, error) {
	output := &boundedProcessBuffer{maximum: maximumOutput}
	command := exec.CommandContext(ctx, executable, "version")
	command.Stdout = output
	command.Stderr = io.Discard
	command.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("updater version process failed: %w", err)
	}
	if output.exceeded {
		return "", fmt.Errorf("updater version output exceeds %d bytes", maximumOutput)
	}
	return output.String(), nil
}

func (windowsPromotionOperations) Replace(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func (windowsPromotionOperations) IsRetryable(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
