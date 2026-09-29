// Package logging builds the one log format all three binaries share: log/slog's
// text handler, with a level from flag, env or config, no timestamps under journald
// (it stamps every line itself), and secrets scrubbed from every value on the way
// out. It is a leaf: callers hand it a writer and it hands back a *slog.Logger.
//
// What goes where (see Open and Target): kith, which owns the terminal, logs to the
// journal over its native protocol (JournalHandler) when there is one, else to a
// size-capped file (OpenFile); kithd logs to stderr under systemd (journald captures
// it), and when detached to the journal or its --log-file; kith-mcp to stderr (stdout is
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

// Resolve picks the level: the flag if set, else $KITH_LOG_LEVEL if set, else the
// config's, else info. Each is validated, so a typo is reported rather than ignored.
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

// UnderJournald reports whether stderr is connected to the journal: systemd sets
// JOURNAL_STREAM to its device and inode for a unit whose output goes there, and a
// child that inherited the variable with some other stderr does not count.
func UnderJournald() bool {
	under, err := journal.StderrIsJournalStream()
	return err == nil && under
}

// Discard is a logger that writes nothing: the default for a component nobody wired.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// OrDiscard returns l, or Discard when l is nil.
func OrDiscard(l *slog.Logger) *slog.Logger {
	if l == nil {
		return Discard()
	}
	return l
}

// ── Secrets ──

// secretPatterns match a credential and keep only its name: the replacement keeps
// group 1 (the key and separator) and redacts the rest. Matrix access tokens
// (syt_…, mct_…) are caught bare too, since an error may quote one out of context.
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

// secretKeys are attribute keys whose value is a secret whatever it looks like: the
// value patterns only catch a credential they can recognize, and "token"="abc" is not
// one. Matched whole and case-insensitively; "-" counts as "_".
var secretKeys = map[string]bool{
	"access_token": true, "refresh_token": true, "token": true, "password": true,
	"passphrase": true, "recovery_key": true, "api_key": true, "apikey": true,
	"pickle_key": true, "secret": true, "authorization": true,
}

// isSecretKey reports an attribute key named in secretKeys.
func isSecretKey(key string) bool {
	return secretKeys[strings.ReplaceAll(strings.ToLower(key), "-", "_")]
}

// scrubAttr scrubs one attribute's value, whatever its kind: an error or a Stringer
// is rendered first, so what it says is scrubbed too. A value under a secret key
// (secretKeys) is redacted whole.
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
