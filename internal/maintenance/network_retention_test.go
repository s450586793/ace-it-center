package maintenance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePruner struct {
	mu     sync.Mutex
	calls  []time.Time
	errors []error
	called chan time.Time
}

func (p *fakePruner) PruneNetworkSamples(_ context.Context, before time.Time) (int64, error) {
	p.mu.Lock()
	p.calls = append(p.calls, before)
	call := len(p.calls)
	var err error
	if call <= len(p.errors) {
		err = p.errors[call-1]
	}
	p.mu.Unlock()

	if p.called != nil {
		select {
		case p.called <- before:
		default:
		}
	}
	return 0, err
}

func (p *fakePruner) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNetworkRetentionPrunesImmediately(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	pruner := &fakePruner{called: make(chan time.Time, 1)}
	runner := NewNetworkRetention(pruner, discardLogger(), func() time.Time { return now },
		time.Hour, 90*24*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)

	select {
	case cutoff := <-pruner.called:
		if want := now.Add(-90 * 24 * time.Hour); !cutoff.Equal(want) {
			t.Fatalf("cutoff = %v, want %v", cutoff, want)
		}
	case <-time.After(time.Second):
		t.Fatal("retention prune was not called immediately")
	}
}

func TestNetworkRetentionRetriesAfterError(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	pruner := &fakePruner{
		errors: []error{errors.New("database password=secret")},
		called: make(chan time.Time, 2),
	}
	runner := NewNetworkRetention(pruner, discardLogger(), func() time.Time { return now },
		10*time.Millisecond, 90*24*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)

	for i := 0; i < 2; i++ {
		select {
		case <-pruner.called:
		case <-time.After(time.Second):
			t.Fatal("retention prune did not retry after error")
		}
	}
}

func TestNetworkRetentionDoesNotLogPrunerError(t *testing.T) {
	var logs bytes.Buffer
	pruner := &fakePruner{
		errors: []error{errors.New("credential=secret-network-token")},
		called: make(chan time.Time, 1),
	}
	runner := NewNetworkRetention(
		pruner,
		slog.New(slog.NewTextHandler(&logs, nil)),
		time.Now,
		time.Hour,
		90*24*time.Hour,
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()

	select {
	case <-pruner.called:
	case <-time.After(time.Second):
		t.Fatal("retention prune was not called immediately")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention runner did not stop after context cancellation")
	}

	if output := logs.String(); strings.Contains(output, "secret-network-token") {
		t.Fatalf("retention logs exposed pruner error: %q", output)
	}
}

func TestNetworkRetentionStopsAfterContextCancellation(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	pruner := &fakePruner{called: make(chan time.Time, 2)}
	runner := NewNetworkRetention(pruner, discardLogger(), func() time.Time { return now },
		10*time.Millisecond, 90*24*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()

	select {
	case <-pruner.called:
	case <-time.After(time.Second):
		t.Fatal("retention prune was not called immediately")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention runner did not stop after context cancellation")
	}

	if calls := pruner.callCount(); calls != 1 {
		t.Fatalf("prune call count = %d, want 1 after cancellation", calls)
	}
}
