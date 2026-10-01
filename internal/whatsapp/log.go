package whatsapp

import (
	"context"
	"fmt"
	"log/slog"

	signalLog "go.mau.fi/libsignal/logger"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// slogLogger is whatsmeow's log through kith's: its errors are mostly retried
// requests, so they are warnings; its info is bookkeeping, so it is debug.
type slogLogger struct{ log *slog.Logger }

// newLogger is whatsmeow's logger for one account.
func newLogger(log *slog.Logger, account string) waLog.Logger {
	return slogLogger{log: log.With("component", "whatsmeow", "account", account)}
}

// NewStoreLogger is whatsmeow's logger for the store itself.
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

// signalLogger is libsignal's package-wide logger. Its default prints every debug line
// to stdout, which in kithd is the journal; it is also set lazily and unguarded on
// first use, a race between the first two accounts to sign or decrypt. Set once
// here, before any goroutine, it keeps libsignal's warnings and errors and drops the
// rest.
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
