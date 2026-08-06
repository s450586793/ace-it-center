package maintenance

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type fakePairingPruner struct {
	called chan time.Time
	err    error
}

func (p *fakePairingPruner) PrunePairingRequests(_ context.Context, before time.Time) (int64, error) {
	if p.called != nil {
		p.called <- before
	}
	return 0, p.err
}

func TestPairingRetentionPrunesImmediately(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	pruner := &fakePairingPruner{called: make(chan time.Time, 1)}
	runner := NewPairingRetention(pruner, discardLogger(), func() time.Time { return now }, time.Hour, 30*24*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)
	select {
	case cutoff := <-pruner.called:
		if want := now.Add(-30 * 24 * time.Hour); !cutoff.Equal(want) {
			t.Fatalf("cutoff = %v, want %v", cutoff, want)
		}
	case <-time.After(time.Second):
		t.Fatal("retention prune was not called immediately")
	}
}

func TestPairingRetentionDoesNotLogPrunerError(t *testing.T) {
	var logs bytes.Buffer
	pruner := &fakePairingPruner{
		called: make(chan time.Time, 1),
		err:    errors.New("credential=secret-pairing-token"),
	}
	runner := NewPairingRetention(pruner, slog.New(slog.NewTextHandler(&logs, nil)), time.Now, time.Hour, 30*24*time.Hour)
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
	if output := logs.String(); strings.Contains(output, "secret-pairing-token") {
		t.Fatalf("retention logs exposed pruner error: %q", output)
	}
}
