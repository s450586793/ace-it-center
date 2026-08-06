package windowsservice

import (
	"context"

	"aceitcenter.local/platform/agent/internal/ipc"
)

// RuntimeController 是 Windows Service 运行所需的 Controller 生命周期接口。
type RuntimeController interface {
	ipc.Controller
	Bootstrap(context.Context) error
	Shutdown(context.Context) error
}
