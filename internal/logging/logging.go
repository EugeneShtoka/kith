// Package logging builds the log format shared by all binaries: slog text, level from
// flag/env/config, no timestamps under journald, secrets scrubbed.
//
// kith (owns the terminal) logs to the journal, else a size-capped file; kithd logs to
// stderr under systemd, else the journal or --log-file; kith-mcp to stderr (stdout is
// the protocol).
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/coreos/go-systemd/v22/journal"
)

// EnvLevel is the environment variable that overrides the config's level.
const EnvLevel = "KITH_LOG_LEVEL"

// Levels lists the level names ParseLevel accepts, for help text.
const Levels = "debug, info, warn, error"

// ParseLevel reads a level name. Empty means info.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("logging: unknown level %q (want one of %s)", name, Levels)
	}
}

// Resolve picks the level: flag, else $KITH_LOG_LEVEL, else config, else info. The
// chosen one is validated, so a typo is reported.
func Resolve(flag, configured string) (slog.Level, error) {
	for _, name := range []string{flag, os.Getenv(EnvLevel), configured} {
		if strings.TrimSpace(name) != "" {
			return ParseLevel(name)
		}
	}
	return slog.LevelInfo, nil
}

// Options shapes a logger.
type Options struct {
	// Level is the minimum level written; nil means info.
	Level slog.Leveler
	// NoTime drops the time attribute: journald stamps each line itself.
	NoTime bool
}

// New returns a text logger writing to w, scrubbing secrets from every value.
func New(w io.Writer, opts Options) *slog.Logger {
	level := opts.Level
	if level == nil {
		level = slog.LevelInfo
	}
	noTime := opts.NoTime
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if noTime && len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return scrubAttr(a)
		},
	}))
}

// UnderJournald reports whether stderr is the stream JOURNAL_STREAM names (an
// inherited variable with a different stderr does not count).
func UnderJournald() bool {
	under, err := journal.StderrIsJournalStream()
	return err == nil && under
}

// Discard returns a logger that writes nothing.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// OrDiscard returns l, or Discard when l is nil.
func OrDiscard(l *slog.Logger) *slog.Logger {
	if l == nil {
		return Discard()
	}
	return l
}

// ── Secrets ──

// secretPatterns keep group 1 (key and separator) and redact the rest. Bare Matrix
// tokens (syt_…, mct_…) are caught too, since errors may quote one out of context.
var secretPatterns = []*regexp.Regexp{
	// Authorization: Bearer xyz / Bearer xyz
	regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|basic)\s+|bearer\s+)[^\s"',;]+`),
	// access_token=xyz, "access_token":"xyz", password: xyz, and friends.
	regexp.MustCompile(`(?i)((?:access_token|refresh_token|token|password|passphrase|recovery_key|api_key|apikey|pickle_key)` +
		`"?\s*[:=]\s*"?)[^\s"&',;}]+`),
	// Bare Synapse/tuwunel-style tokens.
	regexp.MustCompile(`()\b(?:syt|syr|mct|mat)_[A-Za-z0-9_-]{8,}`),
}

// Redacted replaces every scrubbed secret.
const Redacted = "[REDACTED]"

// Scrub removes credentials from s.
func Scrub(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "${1}"+Redacted)
	}
	return s
}

// secretKeys are attribute keys whose values are always redacted, since the patterns
// can't recognize e.g. "abc". Case-insensitive, whole match; "-" counts as "_".
var secretKeys = map[string]bool{
	"access_token": true, "refresh_token": true, "token": true, "password": true,
	"passphrase": true, "recovery_key": true, "api_key": true, "apikey": true,
	"pickle_key": true, "secret": true, "authorization": true,
}

// isSecretKey reports an attribute key named in secretKeys.
func isSecretKey(key string) bool {
	return secretKeys[strings.ReplaceAll(strings.ToLower(key), "-", "_")]
}

// scrubAttr scrubs a value of any kind, rendering errors and Stringers first; a
// secretKeys value is redacted whole.
func scrubAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() != slog.KindGroup && isSecretKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Scrub(v.String()))
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, Scrub(x.Error()))
		case fmt.Stringer:
			return slog.String(a.Key, Scrub(x.String()))
		case []byte:
			return slog.String(a.Key, Scrub(string(x)))
		default:
			return slog.String(a.Key, Scrub(fmt.Sprint(x)))
		}
	case slog.KindGroup:
		attrs := v.Group()
		scrubbed := make([]any, 0, len(attrs))
		for _, inner := range attrs {
			scrubbed = append(scrubbed, scrubAttr(inner))
		}
		return slog.Group(a.Key, scrubbed...)
	case slog.KindBool, slog.KindDuration, slog.KindFloat64, slog.KindInt64,
		slog.KindTime, slog.KindUint64, slog.KindLogValuer:
		return slog.Attr{Key: a.Key, Value: v}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
