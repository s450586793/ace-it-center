package maintenance

import (
	"context"
	"log/slog"
	"time"
)

type PairingRequestPruner interface {
	PrunePairingRequests(context.Context, time.Time) (int64, error)
}

type PairingRetention struct {
	pruner    PairingRequestPruner
	logger    *slog.Logger
	now       func() time.Time
	interval  time.Duration
	retention time.Duration
}

func NewPairingRetention(
	pruner PairingRequestPruner,
	logger *slog.Logger,
	now func() time.Time,
	interval, retention time.Duration,
) *PairingRetention {
	return &PairingRetention{
		pruner:    pruner,
		logger:    logger,
		now:       now,
		interval:  interval,
		retention: retention,
	}
}

func (r *PairingRetention) Run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	r.prune(ctx)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			r.prune(ctx)
		}
	}
}

func (r *PairingRetention) prune(ctx context.Context) {
	cutoff := r.now().Add(-r.retention)
	count, err := r.pruner.PrunePairingRequests(ctx, cutoff)
	if err != nil {
		r.logger.Error("pairing request retention prune failed")
		return
	}
	r.logger.Info("pairing request retention pruned", "cutoff", cutoff, "count", count)
}
