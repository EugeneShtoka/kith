package whatsapp

import (
	"context"
	"fmt"
	"log/slog"

	signalLog "go.mau.fi/libsignal/logger"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// slogLogger routes whatsmeow's log to slog: errors (mostly retried requests) as
// warnings, info as debug.
type slogLogger struct{ log *slog.Logger }

func newLogger(log *slog.Logger, account string) waLog.Logger {
	return slogLogger{log: log.With("component", "whatsmeow", "account", account)}
}

// NewStoreLogger returns whatsmeow's logger for the store.
func NewStoreLogger(log *slog.Logger) waLog.Logger {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return slogLogger{log: log.With("component", "whatsmeow", "network", "whatsapp")}
}

func (l slogLogger) Errorf(msg string, args ...any) { l.at(slog.LevelWarn, msg, args...) }
func (l slogLogger) Warnf(msg string, args ...any)  { l.at(slog.LevelWarn, msg, args...) }
func (l slogLogger) Infof(msg string, args ...any)  { l.at(slog.LevelDebug, msg, args...) }
func (l slogLogger) Debugf(msg string, args ...any) { l.at(slog.LevelDebug, msg, args...) }
func (l slogLogger) Sub(module string) waLog.Logger {
	return slogLogger{log: l.log.With("module", module)}
}

func (l slogLogger) at(level slog.Level, msg string, args ...any) {
	if !l.log.Enabled(context.Background(), level) {
		return
	}
	l.log.Log(context.Background(), level, fmt.Sprintf(msg, args...))
}

// signalLogger replaces libsignal's global logger, whose default prints debug to
// stdout (the journal) and is set lazily without a guard (a race between accounts).
// Set once before any goroutine; keeps warnings and errors only.
type signalLogger struct{}

func init() {
	var quiet signalLog.Loggable = signalLogger{}
	signalLog.Setup(&quiet)
}

func (signalLogger) Debug(string, string) {}
func (signalLogger) Info(string, string)  {}
func (signalLogger) Warning(caller, message string) {
	slog.Warn("libsignal: "+message, "component", "libsignal", "caller", caller)
}

func (signalLogger) Error(caller, message string) {
	slog.Warn("libsignal: "+message, "component", "libsignal", "caller", caller)
}
func (signalLogger) Configure(string) {}
