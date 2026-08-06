package maintenance

import (
	"context"
	"log/slog"
	"time"
)

type NetworkSamplePruner interface {
	PruneNetworkSamples(ctx context.Context, before time.Time) (int64, error)
}

type NetworkRetention struct {
	pruner    NetworkSamplePruner
	logger    *slog.Logger
	now       func() time.Time
	interval  time.Duration
	retention time.Duration
}

func NewNetworkRetention(
	pruner NetworkSamplePruner,
	logger *slog.Logger,
	now func() time.Time,
	interval time.Duration,
	retention time.Duration,
) *NetworkRetention {
	return &NetworkRetention{
		pruner:    pruner,
		logger:    logger,
		now:       now,
		interval:  interval,
		retention: retention,
	}
}

func (r *NetworkRetention) Run(ctx context.Context) {
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

func (r *NetworkRetention) prune(ctx context.Context) {
	cutoff := r.now().Add(-r.retention)
	count, err := r.pruner.PruneNetworkSamples(ctx, cutoff)
	if err != nil {
		r.logger.Error("network sample retention prune failed")
		return
	}
	r.logger.Info("network sample retention pruned", "cutoff", cutoff, "count", count)
}
