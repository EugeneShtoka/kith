package telegram

import (
	"context"
	"log/slog"

	gotdlog "github.com/gotd/log"
)

// gotdLog routes gotd's log into the adapter's, demoting its chatty info to debug.
type gotdLog struct{ log *slog.Logger }

var _ gotdlog.Logger = gotdLog{}

func (g gotdLog) level(l gotdlog.Level) slog.Level {
	switch {
	case l >= gotdlog.LevelError:
		return slog.LevelError
	case l >= gotdlog.LevelWarn:
		return slog.LevelWarn
	default:
		return slog.LevelDebug
	}
}

func (g gotdLog) Enabled(ctx context.Context, l gotdlog.Level) bool {
	return g.log.Enabled(ctx, g.level(l))
}

func (g gotdLog) Log(ctx context.Context, l gotdlog.Level, msg string, attrs ...gotdlog.Attr) {
	g.log.LogAttrs(ctx, g.level(l), msg, slogAttrs(attrs)...)
}

// slogAttrs converts gotd's attributes to slog's.
func slogAttrs(attrs []gotdlog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if a.Value.Kind() == gotdlog.KindGroup {
			out = append(out, slog.Attr{Key: a.Key, Value: slog.GroupValue(slogAttrs(a.Value.Group())...)})
			continue
		}
		out = append(out, slog.Any(a.Key, a.Value.Any()))
	}
	return out
}
