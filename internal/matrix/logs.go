package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
)

// UseLogger sets where the backend reports what it cannot return: failed background
// work (cache writes during sync, receipt resends) and mautrix's own log, bridged
// from zerolog. Set before Resume or Login; nil keeps the silent default.
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

// warnIf logs a failed best-effort step at warn, with op naming it; nil is silent.
// For work nobody waits on — a cache write during sync — whose failure would
// otherwise vanish.
func (b *InProc) warnIf(ctx context.Context, err error, op string, attrs ...any) {
	b.logIf(ctx, slog.LevelWarn, err, op, attrs...)
}

// debugIf is warnIf for steps whose failure is routine (a cache that is only a
// speed-up, a server that lacks an optional feature).
func (b *InProc) debugIf(ctx context.Context, err error, op string, attrs ...any) {
	b.logIf(ctx, slog.LevelDebug, err, op, attrs...)
}

func (b *InProc) logIf(ctx context.Context, level slog.Level, err error, op string, attrs ...any) {
	if err == nil {
		return
	}
	// Our own shutdown cancels everything in flight; that is not a failure.
	if ctx.Err() != nil {
		level = slog.LevelDebug
	}
	b.log().Log(ctx, level, op+" failed", append([]any{"op", op, "err", err}, attrs...)...)
}

// attachLogger points a client's zerolog at the backend's slog logger. mautrix's
// info is chatty crypto bookkeeping, so below debug only its warnings pass; trace
// (sync bodies) never does.
func (b *InProc) attachLogger(client *mautrix.Client) {
	if client == nil || b.logger == nil {
		return
	}
	level := zerolog.WarnLevel
	if b.logger.Enabled(context.Background(), slog.LevelDebug) {
		level = zerolog.DebugLevel
	}
	client.Log = zerolog.New(zerologBridge{log: b.logger.With("component", "mautrix")}).Level(level).With().Timestamp().Logger()
}

// requestOutcomes are mautrix's per-request log lines (see Client.LogRequestDone).
var requestOutcomes = map[string]bool{
	"Request failed": true, "Request canceled": true, "Request parsing failed": true, "Request completed": true,
}

// failingSyncer is the sync loop's syncer with its failures logged: mautrix retries a
// failed /sync every 10s and says nothing. The first failure warns, then every
// doubling of the streak (2, 4, 8 …) so an outage stays visible without a line per
// retry, and recovery is logged at info.
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
		// This response's room events are dropped with the loop: the full initial sync
		// that follows brings rooms' state and history back, and the emptied cache
		// stays empty (the durable sign a rewind is owed). What only this response
		// carries still reaches the listeners: to-device messages, device list
		// changes and key counts, for the crypto machine (an initial sync has no
		// device_lists.changed, so a change dropped here is never fetched).
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

// zerologBridge re-emits zerolog's JSON lines through slog, so mautrix's log shares
// the format, the level filter and the secret scrubbing.
type zerologBridge struct{ log *slog.Logger }

// Write takes one zerolog event. A line that is not JSON is logged as it is.
func (z zerologBridge) Write(p []byte) (int, error) {
	var fields map[string]any
	if err := json.Unmarshal(p, &fields); err != nil {
		z.log.Info(string(p))
		return len(p), nil //nolint:nilerr // a line that is not JSON is still logged, as it is
	}
	level := slog.LevelInfo
	switch fields[zerolog.LevelFieldName] {
	case "trace", "debug":
		level = slog.LevelDebug
	case "warn", "error", "fatal", "panic":
		// mautrix calls every failed request an error, retries included; what breaks
		// a feature is logged by the code that gives up on it.
		level = slog.LevelWarn
	}
	msg, _ := fields[zerolog.MessageFieldName].(string) // absent: an empty message
	if requestOutcomes[msg] {
		// Every failed request also comes back as an error to its caller, which logs
		// it with context (or the sync loop does, rate-limited); a second line per
		// retry would bury the journal while offline.
		level = slog.LevelDebug
	}
	delete(fields, zerolog.LevelFieldName)
	delete(fields, zerolog.MessageFieldName)
	delete(fields, zerolog.TimestampFieldName)
	// A request's body can be a message's text; the log names events, never content.
	delete(fields, "req_body")
	delete(fields, "resp_body")
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := make([]any, 0, 2*len(keys))
	for _, k := range keys {
		attrs = append(attrs, k, fmt.Sprint(fields[k]))
	}
	z.log.Log(context.Background(), level, msg, attrs...)
	return len(p), nil
}
