package ipc

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

const defaultRequestTimeout = 10 * time.Second

// Connection 是一个本地 IPC 对端。Task 4 将提供这个平台无关抽象的 Windows named-pipe 实现。
type Connection interface {
	io.Reader
	io.Writer
	io.Closer
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// Transport 接受本地 IPC 连接。
type Transport interface {
	Accept(context.Context) (Connection, error)
	Close() error
}

// Serve 接受并处理连接，直至 ctx 取消或 Transport 返回错误。每个连接只携带一个请求和
// 一个响应，为 pipe transport 保持明确的消息边界。
func Serve(ctx context.Context, transport Transport, router *Router) error {
	return serveWithTimeout(ctx, transport, router, defaultRequestTimeout)
}

func serveWithTimeout(ctx context.Context, transport Transport, router *Router, timeout time.Duration) error {
	if transport == nil || router == nil {
		return errors.New("IPC transport and router are required")
	}
	if timeout <= 0 {
		return errors.New("IPC request timeout must be positive")
	}
	connections := newConnectionGroup()
	defer func() {
		_ = transport.Close()
		connections.closeAll()
		connections.wait()
	}()
	for {
		connection, err := transport.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		connections.add(connection)
		go func() {
			defer connections.done(connection)
			serveConnectionWithTimeout(ctx, connection, router, timeout)
		}()
	}
}

func serveConnectionWithTimeout(ctx context.Context, connection Connection, router *Router, timeout time.Duration) {
	defer connection.Close()
	deadline := time.Now().Add(timeout)
	if err := connection.SetReadDeadline(deadline); err != nil {
		return
	}
	if err := connection.SetWriteDeadline(deadline); err != nil {
		return
	}
	requestContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	request, err := Decode(connection, MaxMessageBytes)
	if err != nil {
		_ = Encode(connection, failure("", "invalid_request", "request is invalid or too large"))
		return
	}
	response := router.Handle(requestContext, request)
	if errors.Is(requestContext.Err(), context.DeadlineExceeded) {
		response = failure(request.ID, "request_timeout", "request timed out")
	}
	_ = Encode(connection, response)
}

type connectionGroup struct {
	mu          sync.Mutex
	connections map[Connection]struct{}
	waitGroup   sync.WaitGroup
}

func newConnectionGroup() *connectionGroup {
	return &connectionGroup{connections: make(map[Connection]struct{})}
}

func (g *connectionGroup) add(connection Connection) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.connections[connection] = struct{}{}
	g.waitGroup.Add(1)
}

func (g *connectionGroup) done(connection Connection) {
	g.mu.Lock()
	delete(g.connections, connection)
	g.mu.Unlock()
	g.waitGroup.Done()
}

func (g *connectionGroup) closeAll() {
	g.mu.Lock()
	connections := make([]Connection, 0, len(g.connections))
	for connection := range g.connections {
		connections = append(connections, connection)
	}
	g.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (g *connectionGroup) wait() {
	g.waitGroup.Wait()
}
