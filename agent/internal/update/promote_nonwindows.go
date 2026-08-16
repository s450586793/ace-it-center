//go:build !windows

package update

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type nonWindowsPromotionOperations struct{}

func defaultPromotionOperations() PromotionOperations { return nonWindowsPromotionOperations{} }

func (nonWindowsPromotionOperations) Lstat(path string) (os.FileInfo, error) { return os.Lstat(path) }

func (nonWindowsPromotionOperations) RunVersion(ctx context.Context, executable string, maximumOutput int) (string, error) {
	output := &boundedProcessBuffer{maximum: maximumOutput}
	command := exec.CommandContext(ctx, executable, "version")
	command.Stdout = output
	command.Stderr = io.Discard
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

func (nonWindowsPromotionOperations) Replace(source, destination string) error {
	return os.Rename(source, destination)
}

func (nonWindowsPromotionOperations) IsRetryable(error) bool { return false }
