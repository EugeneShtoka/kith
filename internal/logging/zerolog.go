package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/rs/zerolog"
)

// Zerolog is a zerolog logger writing through log, for libraries that log with zerolog
// (mautrix, Meta's messagix): the same format, level filter and secret scrubbing.
// level is the least zerolog level passed on.
func Zerolog(log *slog.Logger, level zerolog.Level) zerolog.Logger {
	return zerolog.New(zerologBridge{log: log}).Level(level).With().Timestamp().Logger()
}

// requestOutcomes are mautrix's per-request log lines (see Client.LogRequestDone).
var requestOutcomes = map[string]bool{
	"Request failed": true, "Request canceled": true, "Request parsing failed": true, "Request completed": true,
}

// zerologBridge re-emits zerolog's JSON lines through slog (same format, level filter
// and secret scrubbing).
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
		// mautrix calls every failed request an error, retries included.
		level = slog.LevelWarn
	}
	msg, _ := fields[zerolog.MessageFieldName].(string) // absent: an empty message
	if requestOutcomes[msg] {
		// The caller logs failures with context; a line per retry would flood offline.
		level = slog.LevelDebug
	}
	delete(fields, zerolog.LevelFieldName)
	delete(fields, zerolog.MessageFieldName)
	delete(fields, zerolog.TimestampFieldName)
	// Bodies can hold message text; the log never carries content.
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
