//go:build !windows

package windowsservice

import (
	"context"
	"fmt"
)

func Run(context.Context, RuntimeController) error {
	return fmt.Errorf("Windows Service is only supported on Windows")
}
