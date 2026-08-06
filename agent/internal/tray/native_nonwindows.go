//go:build !windows

package tray

import (
	"context"
	"fmt"
	"time"

	"aceitcenter.local/platform/agent/internal/ipc"
)

// Options 配置原生托盘运行时。
type Options struct {
	Dial            func(context.Context) (ipc.Client, error)
	LogDirectory    string
	RefreshInterval time.Duration
	ShowOnStart     bool
}

func Run(context.Context, ipc.Client, Options) error {
	return fmt.Errorf("native tray is only supported on Windows")
}

// AttachParentConsole 在非 Windows 平台无需执行任何操作。
func AttachParentConsole() bool {
	return false
}
