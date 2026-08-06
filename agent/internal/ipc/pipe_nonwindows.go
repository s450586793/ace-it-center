//go:build !windows

package ipc

import (
	"context"
	"fmt"
)

const (
	WindowsPipeName            = `\\.\pipe\AceITCenterAgent`
	WindowsTrayUpdateEventName = `Global\AceITCenterAgentTray.Update.v1`
)

type Client interface {
	Call(context.Context, Request) (Response, error)
	Close() error
}

func ListenWindows(context.Context, *Router) error {
	return fmt.Errorf("Windows named pipes are only supported on Windows")
}

func ListenWindowsReady(context.Context, *Router, chan<- error) error {
	return fmt.Errorf("Windows named pipes are only supported on Windows")
}

func DialWindows(context.Context) (Client, error) {
	return nil, fmt.Errorf("Windows named pipes are only supported on Windows")
}
