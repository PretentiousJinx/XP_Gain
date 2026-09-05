package store

import (
	"context"
	"log/slog"
	"time"
)

// Sweeper drives SweepOnce on a ticker.
type Sweeper struct {
	DB       *DB
	Push     Pusher
	Interval time.Duration

	// MaxBackoff caps the slowdown applied after repeated failures.
	MaxBackoff time.Duration
}

// Run sweeps until ctx is cancelled.
//
// A failed sweep backs off rather than retrying at the normal interval: if
// Firestore is down or the credential is wrong, hammering it every few seconds
// converts one outage into a second one of our own making. Rows stay dirty
// throughout, so nothing is lost by waiting -- the next successful pass ships
// the whole backlog.
func (s *Sweeper) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	maxBackoff := s.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 10 * time.Minute
	}

	delay := interval
	timer := time.NewTimer(delay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("sync sweeper stopping")
			return
		case <-timer.C:
		}

		n, err := SweepOnce(ctx, s.DB, s.Push)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			delay *= 2
			if delay > maxBackoff {
				delay = maxBackoff
			}
			slog.Error("sync sweep failed", "err", err, "retry_in", delay.String())
		default:
			delay = interval
			if n > 0 {
				slog.Info("sync sweep shipped rows", "rows", n)
			}
		}
		timer.Reset(delay)
	}
}

// FlushOnShutdown makes a final best-effort sweep so work completed in the last
// interval is not left sitting dirty until the process next starts.
func (s *Sweeper) FlushOnShutdown(ctx context.Context) {
	n, err := SweepOnce(ctx, s.DB, s.Push)
	if err != nil {
		slog.Warn("final sync sweep failed; rows remain dirty for the next start", "err", err)
		return
	}
	if n > 0 {
		slog.Info("final sync sweep shipped rows", "rows", n)
	}
}
