package ipc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"aceitcenter.local/platform/agent/internal/controller"
)

type testTransport struct {
	connections chan Connection
	closed      chan struct{}
	once        sync.Once
}

func newTestTransport(connections ...Connection) *testTransport {
	transport := &testTransport{connections: make(chan Connection, len(connections)), closed: make(chan struct{})}
	for _, connection := range connections {
		transport.connections <- connection
	}
	return transport
}

func (t *testTransport) Accept(ctx context.Context) (Connection, error) {
	select {
	case connection := <-t.connections:
		return connection, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *testTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

type testConnection struct {
	reader        io.Reader
	writer        bytes.Buffer
	readDeadline  time.Time
	writeDeadline time.Time
	written       chan struct{}
	closed        chan struct{}
	mu            sync.Mutex
	closeOnce     sync.Once
}

func newTestConnection(request string) *testConnection {
	return &testConnection{reader: bytes.NewBufferString(request), written: make(chan struct{}), closed: make(chan struct{})}
}

func (c *testConnection) Read(contents []byte) (int, error) { return c.reader.Read(contents) }

func (c *testConnection) Write(contents []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	count, err := c.writer.Write(contents)
	select {
	case <-c.written:
	default:
		close(c.written)
	}
	return count, err
}

func (c *testConnection) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *testConnection) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = deadline
	return nil
}

func (c *testConnection) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = deadline
	return nil
}

type slowConnection struct {
	*testConnection
	deadline chan time.Time
}

func newSlowConnection() *slowConnection {
	return &slowConnection{testConnection: newTestConnection(""), deadline: make(chan time.Time, 1)}
}

func (c *slowConnection) Read([]byte) (int, error) {
	select {
	case deadline := <-c.deadline:
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		select {
		case <-timer.C:
			return 0, errors.New("read deadline exceeded")
		case <-c.closed:
			return 0, io.ErrClosedPipe
		}
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}

func (c *slowConnection) SetReadDeadline(deadline time.Time) error {
	c.testConnection.SetReadDeadline(deadline)
	c.deadline <- deadline
	return nil
}

func TestServeTimesOutSlowReadAndSetsDeadlines(t *testing.T) {
	connection := newSlowConnection()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := newTestTransport(connection)
	done := make(chan error, 1)
	go func() { done <- serveWithTimeout(ctx, transport, NewRouter(&fakeController{}), 20*time.Millisecond) }()

	select {
	case <-connection.written:
	case <-time.After(time.Second):
		t.Fatal("slow read did not time out")
	}
	connection.mu.Lock()
	readDeadline := connection.readDeadline
	writeDeadline := connection.writeDeadline
	connection.mu.Unlock()
	if readDeadline.IsZero() || writeDeadline.IsZero() {
		t.Fatalf("deadlines = %v, %v", readDeadline, writeDeadline)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestServeAcceptsFastConnectionWhileSlowConnectionIsPending(t *testing.T) {
	slow := newSlowConnection()
	fast := newTestConnection(`{"id":"1","method":"status.get"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := newTestTransport(slow, fast)
	done := make(chan error, 1)
	go func() { done <- serveWithTimeout(ctx, transport, NewRouter(&fakeController{}), time.Second) }()

	select {
	case <-fast.written:
	case <-time.After(time.Second):
		t.Fatal("fast connection was blocked by slow connection")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestServeCancelsHandlerAtRequestDeadline(t *testing.T) {
	connection := newTestConnection(`{"id":"1","method":"update.check"}`)
	started := make(chan struct{})
	fake := &fakeController{}
	fake.updateErr = nil
	controller := blockingController{fakeController: fake, started: started}

	done := make(chan struct{})
	go func() {
		serveConnectionWithTimeout(context.Background(), connection, NewRouter(&controller), 20*time.Millisecond)
		close(done)
	}()
	<-started
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not observe request deadline")
	}
}

type blockingController struct {
	*fakeController
	started chan struct{}
}

func (c *blockingController) CheckUpdate(ctx context.Context) (controller.UpdateStatus, error) {
	close(c.started)
	<-ctx.Done()
	return controller.UpdateStatus{}, ctx.Err()
}
