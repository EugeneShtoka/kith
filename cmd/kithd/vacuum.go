package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
)

// The cache is vacuumed when it is worth it (db.Cache.Vacuum decides): first a while
// after start, past the networks' catch-up, then daily. One put off (no room for the
// rewrite, a backup reading the cache) is tried again in an hour.
const (
	vacuumFirst = 10 * time.Minute
	vacuumEvery = 24 * time.Hour
	vacuumRetry = time.Hour
)

// vacuumCache runs the cache's vacuums until ctx ends, which interrupts one running.
// It blocks.
func vacuumCache(ctx context.Context, log *slog.Logger, cache *db.Cache) {
	if cache == nil {
		return
	}
	// putOff is the last reason one was put off, so a lasting one is logged once.
	var putOff string
	wait := vacuumFirst
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = vacuumEvery
		v, err := cache.Vacuum(ctx, sqliteTempDir())
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, db.ErrVacuumPutOff):
			if err.Error() != putOff {
				putOff = err.Error()
				log.Info(putOff, "op", "cache vacuum")
			}
			wait = vacuumRetry
			continue
		case err != nil:
			log.Warn("cache vacuum failed", "op", "cache vacuum", "err", err)
		case v.Ran:
			log.Info("cache vacuumed", "op", "cache vacuum",
				"before_mb", v.Before>>20, "after_mb", v.After>>20, "took", v.Took.Round(time.Millisecond))
		default:
			log.Debug("cache vacuum not needed", "op", "cache vacuum", "free_mb", v.Free>>20, "size_mb", v.Before>>20)
		}
		putOff = ""
	}
}
