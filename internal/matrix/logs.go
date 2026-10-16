package matrix

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"

	"github.com/EugeneShtoka/kith/internal/logging"
)

// UseLogger sets the logger for failed background work and mautrix's bridged log.
// Set before Resume or Login; nil keeps the silent default.
func (b *InProc) UseLogger(log *slog.Logger) {
	if log == nil {
		return
	}
	b.logger = log
}

// log is the backend's logger, never nil.
func (b *InProc) log() *slog.Logger {
	if b.logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return b.logger
}

// warnIf logs a failed best-effort step at warn; nil is silent.
func (b *InProc) warnIf(ctx context.Context, err error, op string, attrs ...any) {
	b.logIf(ctx, slog.LevelWarn, err, op, attrs...)
}

// debugIf is warnIf for steps whose failure is routine.
func (b *InProc) debugIf(ctx context.Context, err error, op string, attrs ...any) {
	b.logIf(ctx, slog.LevelDebug, err, op, attrs...)
}

func (b *InProc) logIf(ctx context.Context, level slog.Level, err error, op string, attrs ...any) {
	if err == nil {
		return
	}
	// Shutdown cancels everything in flight; not a failure.
	if ctx.Err() != nil {
		level = slog.LevelDebug
	}
	b.log().Log(ctx, level, op+" failed", append([]any{"op", op, "err", err}, attrs...)...)
}

// attachLogger points a client's zerolog at the backend's slog logger. mautrix's info
// is chatty, so below debug only warnings pass; trace (sync bodies) never does.
func (b *InProc) attachLogger(client *mautrix.Client) {
	if client == nil || b.logger == nil {
		return
	}
	level := zerolog.WarnLevel
	if b.logger.Enabled(context.Background(), slog.LevelDebug) {
		level = zerolog.DebugLevel
	}
	client.Log = logging.Zerolog(b.logger.With("component", "mautrix"), level)
}

// failingSyncer logs sync failures, which mautrix retries silently every 10s: warn on
// each power-of-two streak, info on recovery.
type failingSyncer struct {
	*mautrix.DefaultSyncer
	b      *InProc
	mu     sync.Mutex
	streak int
}

func (f *failingSyncer) OnFailedSync(res *mautrix.RespSync, err error) (time.Duration, error) {
	wait, stop := f.DefaultSyncer.OnFailedSync(res, err)
	f.mu.Lock()
	f.streak++
	streak := f.streak
	f.mu.Unlock()
	level := slog.LevelDebug
	if streak&(streak-1) == 0 { // a power of two
		level = slog.LevelWarn
	}
	if stop != nil {
		level = slog.LevelError
	}
	f.b.log().Log(context.Background(), level, "sync failed", "op", "sync", "failures", streak, "retry_in", wait, "err", err)
	return wait, stop //nolint:wrapcheck // mautrix's own contract: its error ends the loop as is
}

func (f *failingSyncer) ProcessResponse(ctx context.Context, res *mautrix.RespSync, since string) error {
	if f.b.rewind.take() {
		// Drop the room events (the following initial sync restores them and the
		// cache stays empty), but still deliver to-device messages, device list
		// changes and key counts: an initial sync has no device_lists.changed.
		bare := *res
		bare.Rooms = mautrix.RespSync{}.Rooms
		if err := f.DefaultSyncer.ProcessResponse(ctx, &bare, since); err != nil {
			return err //nolint:wrapcheck // handlers' errors, passed through to mautrix
		}
		return errRewind
	}
	f.mu.Lock()
	streak := f.streak
	f.streak = 0
	f.mu.Unlock()
	if streak > 0 {
		f.b.log().Info("sync recovered", "op", "sync", "failures", streak)
	}
	return f.DefaultSyncer.ProcessResponse(ctx, res, since) //nolint:wrapcheck // handlers' errors, passed through to mautrix
}
