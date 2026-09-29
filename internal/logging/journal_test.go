package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// entry is one journal entry a fakeJournal took.
type entry struct {
	message  string
	priority int
	fields   map[string]string
}

// fakeJournal records what a JournalHandler sends.
type fakeJournal struct {
	mu      sync.Mutex
	entries []entry
	down    bool
}

func (f *fakeJournal) journal() Journal {
	return Journal{
		Available: func() bool { return !f.down },
		Send: func(message string, priority int, fields map[string]string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.down {
				return errors.New("journal down")
			}
			f.entries = append(f.entries, entry{message, priority, fields})
			return nil
		},
	}
}

// last is the latest entry, its MESSAGE and PRIORITY folded into the fields.
func (f *fakeJournal) last(t *testing.T) map[string]string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.entries) == 0 {
		t.Fatal("no journal entry")
	}
	e := f.entries[len(f.entries)-1]
	out := map[string]string{"MESSAGE": e.message, "PRIORITY": fmt.Sprint(e.priority)}
	maps.Copy(out, e.fields)
	return out
}

func (f *fakeJournal) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

func newJournalLogger(level slog.Leveler) (*fakeJournal, *slog.Logger) {
	f := &fakeJournal{}
	return f, slog.New(NewJournalHandler(f.journal(), "kith-test", level))
}

func TestFieldName(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]string{
		"room":        "ROOM",
		"op":          "OP",
		"err":         "ERR",
		"event":       "EVENT",
		"req_hdr":     "REQ_HDR",
		"key.ref-x y": "KEY_REF_X_Y",
		"_private":    "PRIVATE",
		"2fa":         "FA",
		"__9_x":       "X",
		"_":           "",
		"":            "",
		"é":           "",
		"message":     "ATTR_MESSAGE",
		"priority":    "ATTR_PRIORITY",
		"message_id":  "ATTR_MESSAGE_ID",
		"CamelCase":   "CAMELCASE",
	} {
		if got := FieldName(key); got != want {
			t.Errorf("FieldName(%q) = %q, want %q", key, got, want)
		}
	}
	if got := FieldName(strings.Repeat("a", 100)); len(got) != maxFieldName {
		t.Errorf("long name kept %d bytes, want %d", len(got), maxFieldName)
	}
}

func TestPriority(t *testing.T) {
	t.Parallel()
	for level, want := range map[slog.Level]int{
		slog.LevelError: 3, slog.LevelError + 4: 3,
		slog.LevelWarn: 4, slog.LevelWarn + 2: 4,
		slog.LevelInfo: 6, slog.LevelInfo + 1: 6,
		slog.LevelDebug: 7, slog.LevelDebug - 4: 7,
	} {
		if got := Priority(level); got != want {
			t.Errorf("Priority(%v) = %d, want %d", level, got, want)
		}
	}
}

func TestJournalHandlerFields(t *testing.T) {
	t.Parallel()
	f, log := newJournalLogger(slog.LevelDebug)
	log.Warn("mark room read failed", "room", "!abc:example.org", "op", "mark room read",
		"err", errors.New("M_FORBIDDEN\nsecond line"), "count", 3, "took", 1500*time.Millisecond,
		"message", "an attribute, not the MESSAGE")
	got := f.last(t)
	want := map[string]string{
		"MESSAGE": "mark room read failed", "PRIORITY": "4", "SYSLOG_IDENTIFIER": "kith-test",
		"ROOM": "!abc:example.org", "OP": "mark room read", "ERR": "M_FORBIDDEN\nsecond line",
		"COUNT": "3", "TOOK": "1.5s", "ATTR_MESSAGE": "an attribute, not the MESSAGE",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (entry %v)", k, got[k], v, got)
		}
	}
	log.Debug("dbg", "at", time.Date(2026, 9, 25, 1, 2, 3, 0, time.UTC), "_", "dropped")
	if got := f.last(t); got["PRIORITY"] != "7" || got["AT"] != "2026-09-25T01:02:03Z" || len(got) != 4 {
		t.Errorf("debug entry = %v", got)
	}
	// Every name handed to the journal is one it accepts: go-systemd would otherwise
	// complain on stderr, which kith must never write to.
	log.Info("odd keys", "a.b", 1, "-x", 2, "9lives", 3, "ключ", 4)
	for name := range f.last(t) {
		if FieldName(name) != name && name != "MESSAGE" && name != "PRIORITY" && name != "SYSLOG_IDENTIFIER" {
			t.Errorf("field %q is not a sanitized name", name)
		}
	}
}

func TestJournalHandlerLevel(t *testing.T) {
	t.Parallel()
	level := &slog.LevelVar{}
	level.Set(slog.LevelWarn)
	f, log := newJournalLogger(level)
	if log.Enabled(t.Context(), slog.LevelInfo) {
		t.Error("info enabled at warn")
	}
	log.Info("dropped")
	log.Error("kept")
	if got := f.last(t); f.count() != 1 || got["MESSAGE"] != "kept" || got["PRIORITY"] != "3" {
		t.Errorf("entries = %d, last %v; want only the error", f.count(), got)
	}
	level.Set(slog.LevelDebug)
	if !log.Enabled(t.Context(), slog.LevelDebug) {
		t.Error("a LevelVar change was not seen")
	}
	h := NewJournalHandler(Journal{}, "", nil)
	if !h.Enabled(t.Context(), slog.LevelInfo) || h.Enabled(t.Context(), slog.LevelDebug) {
		t.Error("nil level is not info")
	}
}

func TestJournalWithAttrsAndGroups(t *testing.T) {
	t.Parallel()
	f, base := newJournalLogger(slog.LevelInfo)
	log := base.With("user", "@a:x").WithGroup("req").With("op", "sync").WithGroup("")
	log.Info("m", "hdr", "h", slog.Group("inner", "k", "v"), slog.Group("", "flat", 1), slog.Group("empty"), slog.Attr{}) //nolint:sloglint // mixed on purpose: the handler must flatten both forms
	got := f.last(t)
	want := map[string]string{
		"USER": "@a:x", "REQ_OP": "sync", "REQ_HDR": "h", "REQ_INNER_K": "v", "REQ_FLAT": "1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (entry %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want)+3 {
		t.Errorf("unexpected fields in %v", got)
	}
	// The base logger is untouched by its derivatives.
	base.Info("plain", "x", 1)
	if got := f.last(t); got["USER"] != "" || got["X"] != "1" || got["REQ_X"] != "" {
		t.Errorf("base entry picked up derived attrs: %v", got)
	}
	// A group with nothing in it leaves no trace.
	base.WithGroup("g").Info("nothing")
	if got := f.last(t); len(got) != 3 {
		t.Errorf("empty group added fields: %v", got)
	}
	// A record's attribute overrides a WithAttrs one of the same name.
	base.With("room", "old").Info("m", "room", "new")
	if got := f.last(t); got["ROOM"] != "new" {
		t.Errorf("ROOM = %q, want the record's", got["ROOM"])
	}
	h := NewJournalHandler(Journal{}, "", nil)
	if h.WithAttrs(nil) != h || h.WithGroup("") != h {
		t.Error("empty WithAttrs/WithGroup should return the handler itself")
	}
}

// The journal gets exactly what the text format would: no secrets.
func TestJournalScrubs(t *testing.T) {
	t.Parallel()
	f, log := newJournalLogger(slog.LevelDebug)
	token := "syt_" + strings.Repeat("y", 20)
	log.With("auth", "Bearer "+token).WithGroup("g").Warn("login with password=hunter2 "+token,
		"err", fmt.Errorf("GET ?access_token=%s failed", token),
		"detail", stringer("api_key="+token), "raw", []byte("token: "+token),
		slog.Group("req", "hdr", "Authorization: Bearer "+token))
	got := f.last(t)
	for k, v := range got {
		for _, secret := range []string{token, "hunter2", "yyyyyyyyyy"} {
			if strings.Contains(v, secret) {
				t.Errorf("%s leaked %q: %q", k, secret, v)
			}
		}
	}
	for _, k := range []string{"MESSAGE", "AUTH", "G_ERR", "G_DETAIL", "G_RAW", "G_REQ_HDR"} {
		if !strings.Contains(got[k], Redacted) {
			t.Errorf("%s = %q, want it redacted", k, got[k])
		}
	}
}

func TestJournalSendError(t *testing.T) {
	t.Parallel()
	f := &fakeJournal{down: true}
	h := NewJournalHandler(f.journal(), "x", nil)
	if err := h.Handle(t.Context(), slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0)); err == nil {
		t.Error("a failed send was not reported")
	}
}

func TestJournalConcurrent(t *testing.T) {
	t.Parallel()
	f, log := newJournalLogger(slog.LevelInfo)
	const writers, each = 8, 50
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			l := log.With("writer", w)
			for i := range each {
				l.WithGroup("g").Info("line", "i", i)
			}
		})
	}
	wg.Wait()
	if f.count() != writers*each {
		t.Fatalf("%d entries, want %d", f.count(), writers*each)
	}
	for _, e := range f.entries {
		if e.message != "line" || e.fields["WRITER"] == "" || e.fields["G_I"] == "" {
			t.Fatalf("garbled entry %+v", e)
		}
	}
}

// Live: KITH_LIVE_JOURNAL=1 go test -run TestLiveJournal ./internal/logging writes a
// uniquely tagged entry to this machine's journal and reads it back.
func TestLiveJournal(t *testing.T) {
	if os.Getenv("KITH_LIVE_JOURNAL") != "1" {
		t.Skip("set KITH_LIVE_JOURNAL=1 to write to the real journal")
	}
	tag := fmt.Sprintf("kith-live-%d", time.Now().UnixNano())
	if !SystemJournal.Available() {
		t.Fatal("no journal on this machine")
	}
	token := "syt_" + strings.Repeat("z", 20)
	slog.New(NewJournalHandler(SystemJournal, tag, nil)).Warn("live smoke test "+token, "room", "!live:example.org", "err", "two\nlines")

	var entry map[string]any
	for range 50 {
		out, err := exec.CommandContext(t.Context(), "journalctl", "--user", "-t", tag, "-o", "json", "--no-pager").Output()
		if err == nil && len(bytes.TrimSpace(out)) > 0 {
			if err := json.Unmarshal(bytes.SplitN(bytes.TrimSpace(out), []byte("\n"), 2)[0], &entry); err != nil {
				t.Fatalf("parse %q: %v", out, err)
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if entry == nil {
		t.Fatalf("entry tagged %s never showed up in journalctl --user", tag)
	}
	t.Logf("journal entry: MESSAGE=%v PRIORITY=%v SYSLOG_IDENTIFIER=%v ROOM=%v ERR=%v",
		entry["MESSAGE"], entry["PRIORITY"], entry["SYSLOG_IDENTIFIER"], entry["ROOM"], entry["ERR"])
	if entry["PRIORITY"] != "4" || entry["ROOM"] != "!live:example.org" ||
		!strings.Contains(fmt.Sprint(entry["MESSAGE"]), Redacted) {
		t.Errorf("entry = %v", entry)
	}
}
