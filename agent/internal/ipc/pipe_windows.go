//go:build windows

package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	winio "github.com/Microsoft/go-winio"
)

const (
	WindowsPipeName            = `\\.\pipe\AceITCenterAgent`
	WindowsTrayUpdateEventName = `Global\AceITCenterAgentTray.Update.v1`
	pipeSecurity               = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"
)

type Client interface {
	Call(context.Context, Request) (Response, error)
	Close() error
}

func ListenWindows(ctx context.Context, router *Router) error {
	return ListenWindowsReady(ctx, router, nil)
}

// ListenWindowsReady sends the bind result before accepting named-pipe clients.
func ListenWindowsReady(ctx context.Context, router *Router, ready chan<- error) error {
	if ctx == nil || router == nil {
		err := fmt.Errorf("IPC context and router are required")
		signalReady(ready, err)
		return err
	}
	listenContext, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := winio.ListenPipe(WindowsPipeName, &winio.PipeConfig{
		SecurityDescriptor: pipeSecurity,
		MessageMode:        true,
		InputBufferSize:    MaxMessageBytes,
		OutputBufferSize:   MaxMessageBytes,
	})
	if err != nil {
		err = fmt.Errorf("listen named pipe: %w", err)
		signalReady(ready, err)
		return err
	}
	transport := &pipeTransport{listener: listener}
	stopCloser := make(chan struct{})
	defer close(stopCloser)
	go func() {
		select {
		case <-listenContext.Done():
			_ = transport.Close()
		case <-stopCloser:
		}
	}()
	signalReady(ready, nil)
	return Serve(listenContext, transport, router)
}

func signalReady(ready chan<- error, err error) {
	if ready != nil {
		ready <- err
	}
}

type pipeTransport struct {
	listener net.Listener
	once     sync.Once
}

func (t *pipeTransport) Accept(ctx context.Context) (Connection, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	connection, err := t.listener.Accept()
	if err != nil {
		return nil, err
	}
	return connection, nil
}

func (t *pipeTransport) Close() error {
	var err error
	t.once.Do(func() { err = t.listener.Close() })
	return err
}

type pipeClient struct {
	connection net.Conn
	once       sync.Once
	mu         sync.Mutex
	used       bool
}

func DialWindows(ctx context.Context) (Client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("IPC context is required")
	}
	connection, err := winio.DialPipeContext(ctx, WindowsPipeName)
	if err != nil {
		return nil, fmt.Errorf("dial named pipe: %w", err)
	}
	return &pipeClient{connection: connection}, nil
}

func (c *pipeClient) Call(ctx context.Context, request Request) (Response, error) {
	if ctx == nil {
		return Response{}, fmt.Errorf("IPC context is required")
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	c.mu.Lock()
	if c.used {
		c.mu.Unlock()
		return Response{}, fmt.Errorf("IPC client accepts one request")
	}
	c.used = true
	c.mu.Unlock()
	stopCancellation := make(chan struct{})
	defer close(stopCancellation)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-stopCancellation:
		}
	}()
	deadline := time.Now().Add(defaultRequestTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := c.connection.SetWriteDeadline(deadline); err != nil {
		return Response{}, fmt.Errorf("set named pipe write deadline: %w", err)
	}
	if err := encodeRequest(c.connection, request); err != nil {
		return Response{}, err
	}
	closeWriter, ok := c.connection.(interface{ CloseWrite() error })
	if !ok {
		return Response{}, fmt.Errorf("named pipe does not support closing writes")
	}
	if err := closeWriter.CloseWrite(); err != nil {
		return Response{}, fmt.Errorf("close named pipe writes: %w", err)
	}
	if err := c.connection.SetReadDeadline(deadline); err != nil {
		return Response{}, fmt.Errorf("set named pipe read deadline: %w", err)
	}
	response, err := decodeResponse(c.connection)
	if err != nil {
		return Response{}, err
	}
	return response, nil
}

func (c *pipeClient) Close() error {
	var err error
	c.once.Do(func() { err = c.connection.Close() })
	return err
}

func decodeResponse(reader net.Conn) (Response, error) {
	contents := make([]byte, 0, MaxMessageBytes)
	buffer := make([]byte, 4096)
	for {
		count, err := reader.Read(buffer)
		if len(contents)+count > MaxMessageBytes {
			return Response{}, ErrMessageTooLarge
		}
		contents = append(contents, buffer[:count]...)
		if err != nil {
			if len(contents) == 0 {
				return Response{}, fmt.Errorf("read IPC response: %w", err)
			}
			break
		}
	}
	var response Response
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decodeExactlyOne(decoder, &response); err != nil {
		return Response{}, err
	}
	return response, nil
}

func encodeRequest(writer io.Writer, request Request) error {
	contents, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode IPC request: %w", err)
	}
	if len(contents) > MaxMessageBytes {
		return ErrMessageTooLarge
	}
	count, err := writer.Write(contents)
	if err != nil {
		return fmt.Errorf("write IPC request: %w", err)
	}
	if count != len(contents) {
		return io.ErrShortWrite
	}
	return nil
}
