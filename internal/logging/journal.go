package logging

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/journal"
)

// Journal is where journal entries go. SystemJournal is the real one; tests pass
// a fake.
type Journal struct {
	// Available reports whether the journal can take entries.
	Available func() bool
	// Send writes one entry: its MESSAGE, PRIORITY (a syslog priority, see
	// Priority) and other fields.
	Send func(message string, priority int, fields map[string]string) error
}

// SystemJournal is systemd-journald through go-systemd's native-protocol client:
// one datagram per entry on /run/systemd/journal/socket, an entry too big for one
// handed over as a file descriptor. Available probes the socket (a datagram
// connect, which fails with no socket or nobody bound to it); it is false on
// Windows, and on macOS and FreeBSD, which have no journald.
var SystemJournal = Journal{
	Available: journal.Enabled,
	Send: func(message string, priority int, fields map[string]string) error {
		if err := journal.Send(message, journal.Priority(priority), fields); err != nil {
			return fmt.Errorf("logging: write journal: %w", err)
		}
		return nil
	},
}

// maxFieldName is journald's limit on a field name.
const maxFieldName = 64

// reservedFields are journal fields with a meaning of their own; an attribute
// whose name comes out as one is renamed ATTR_<name> rather than overriding it.
var reservedFields = map[string]bool{
	"MESSAGE": true, "MESSAGE_ID": true, "PRIORITY": true, "SYSLOG_IDENTIFIER": true,
	"SYSLOG_FACILITY": true, "SYSLOG_PID": true, "SYSLOG_TIMESTAMP": true, "SYSLOG_RAW": true,
	"CODE_FILE": true, "CODE_LINE": true, "CODE_FUNC": true, "ERRNO": true, "TID": true,
	"INVOCATION_ID": true, "USER_INVOCATION_ID": true, "DOCUMENTATION": true,
}

// FieldName turns an attribute key (groups joined with _) into a journal field
// name: upper case, every character outside [A-Z0-9_] replaced with _, with no
// leading _ (journald keeps those for fields it sets itself) or digit, and at most
// 64 bytes. It returns "" when nothing usable is left, and the attribute is dropped.
func FieldName(key string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(key) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.TrimLeft(b.String(), "_0123456789")
	if reservedFields[name] {
		name = "ATTR_" + name
	}
	if len(name) > maxFieldName {
		name = name[:maxFieldName]
	}
	return name
}

// Priority maps a slog level to a syslog priority: error and above 3, warn 4,
// info 6, debug 7. A level between two takes the lower one's.
func Priority(level slog.Level) int {
	switch {
	case level >= slog.LevelError:
		return int(journal.PriErr)
	case level >= slog.LevelWarn:
		return int(journal.PriWarning)
	case level >= slog.LevelInfo:
		return int(journal.PriInfo)
	default:
		return int(journal.PriDebug)
	}
}

// JournalHandler is an slog.Handler writing each record to the journal: MESSAGE,
// PRIORITY, SYSLOG_IDENTIFIER, and one field per attribute (see FieldName), every
// value scrubbed of secrets as the text format is. It is immutable, so safe for
// concurrent use (Send is one datagram per entry).
type JournalHandler struct {
	journal    Journal
	identifier string
	level      slog.Leveler
	// fixed holds the fields of WithAttrs, already named and scrubbed.
	fixed map[string]string
	// prefix is the open groups of WithGroup, each followed by _.
	prefix string
}

// NewJournalHandler returns a handler writing to j, tagging entries
// SYSLOG_IDENTIFIER=identifier, at level (nil: info) and above.
func NewJournalHandler(j Journal, identifier string, level slog.Leveler) *JournalHandler {
	if level == nil {
		level = slog.LevelInfo
	}
	return &JournalHandler{journal: j, identifier: identifier, level: level}
}

// Enabled reports whether level is written.
func (h *JournalHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

// Handle writes one record as one journal entry.
func (h *JournalHandler) Handle(_ context.Context, r slog.Record) error {
	fields := make(map[string]string, 1+len(h.fixed)+r.NumAttrs())
	maps.Copy(fields, h.fixed)
	r.Attrs(func(a slog.Attr) bool {
		addAttr(fields, h.prefix, a)
		return true
	})
	if h.identifier != "" {
		fields["SYSLOG_IDENTIFIER"] = h.identifier
	}
	return h.journal.Send(Scrub(r.Message), Priority(r.Level), fields)
}

// WithAttrs returns a handler adding attrs, under the open groups, to every entry.
func (h *JournalHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := *h
	next.fixed = maps.Clone(h.fixed)
	if next.fixed == nil {
		next.fixed = make(map[string]string, len(attrs))
	}
	for _, a := range attrs {
		addAttr(next.fixed, h.prefix, a)
	}
	return &next
}

// WithGroup returns a handler naming later attributes GROUP_KEY.
func (h *JournalHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.prefix = h.prefix + name + "_"
	return &next
}

// addAttr adds a's field (or its group's fields) to fields, following slog's
// handler rules: an empty attribute is dropped, an empty group too, and a group
// with no key is inlined.
func addAttr(fields map[string]string, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		inner := prefix
		if a.Key != "" {
			inner = prefix + a.Key + "_"
		}
		for _, ga := range a.Value.Group() {
			addAttr(fields, inner, ga)
		}
		return
	}
	if name := FieldName(prefix + a.Key); name != "" {
		fields[name] = render(scrubAttr(a).Value)
	}
}

// render formats a scrubbed value as the text handler would, times as RFC 3339.
func render(v slog.Value) string {
	if v.Kind() == slog.KindTime {
		return v.Time().Format(time.RFC3339Nano)
	}
	return v.String()
}
