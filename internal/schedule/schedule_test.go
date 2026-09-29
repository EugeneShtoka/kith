package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "scheduled.toml"))
}

// A queue survives the round trip whole: the body, where it goes, the thread it belongs
// to, and the instant it is due.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	s := tempStore(t)
	at := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	want := []domain.ScheduledMessage{{
		ID: "a1", RoomID: "!a:x", Body: "morning", ThreadRoot: "$root", Emote: true,
		At: at, Written: at.Add(-time.Hour),
	}}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d entries, want 1", len(got))
	}
	g := got[0]
	if g.ID != "a1" || g.RoomID != "!a:x" || g.Body != "morning" || g.ThreadRoot != "$root" || !g.Emote {
		t.Errorf("entry came back as %+v", g)
	}
	if !g.At.Equal(at) {
		t.Errorf("At = %v, want %v", g.At, at)
	}
	if !g.Written.Equal(at.Add(-time.Hour)) {
		t.Errorf("Written = %v, want an hour before the send time", g.Written)
	}
}

// Every account that has never scheduled anything has no file.
func TestAMissingFileIsAnEmptyQueue(t *testing.T) {
	t.Parallel()

	got, err := tempStore(t).Load()
	if err != nil {
		t.Errorf("Load on a missing file: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("loaded %d entries from nothing", len(got))
	}
}

// The file is meant to be editable by hand, so one bad timestamp must cost that entry
// and not the queue — the entries are the one thing here that cannot be fetched again.
func TestABadEntryIsReportedAndTheRestSurvive(t *testing.T) {
	t.Parallel()

	s := tempStore(t)
	body := `
[[message]]
id = "good"
room = "!a:x"
body = "this one is fine"
at = "2026-09-07T09:00:00Z"
written = "2026-09-06T09:00:00Z"

[[message]]
id = "broken"
room = "!a:x"
body = "tomorrow at half past fish"
at = "half past fish"
written = "2026-09-06T09:00:00Z"
`
	if err := os.WriteFile(s.Path(), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := s.Load()
	if err == nil {
		t.Error("a bad entry was accepted silently")
	} else if !strings.Contains(err.Error(), "broken") {
		t.Errorf("the error does not name the bad entry: %v", err)
	}
	if len(got) != 1 || got[0].ID != "good" {
		t.Errorf("loaded %+v, want the good entry to survive", got)
	}
}

// The queue holds message bodies nobody has sent yet, so the file is owner-only — and
// stays that way when it is replaced, since Save writes a new file and renames.
func TestTheFileIsOwnerOnly(t *testing.T) {
	t.Parallel()

	s := tempStore(t)
	at := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	for range 2 { // twice: the mode has to survive a rewrite, not just creation
		if err := s.Save([]domain.ScheduledMessage{{ID: "a", RoomID: "!a:x", Body: "hi", At: at}}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		info, err := os.Stat(s.Path())
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("mode = %o, want 600", got)
		}
	}
}

// Save leaves nothing behind.
func TestSaveLeavesNoTempFiles(t *testing.T) {
	t.Parallel()

	s := tempStore(t)
	at := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	for i := range 3 {
		if err := s.Save([]domain.ScheduledMessage{{ID: string(rune('a' + i)), RoomID: "!a:x", At: at}}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".scheduled-") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}
}

// An empty queue is written as an empty file rather than removed, so "nothing scheduled"
// and "never scheduled anything" both load the same way.
func TestSavingAnEmptyQueue(t *testing.T) {
	t.Parallel()

	s := tempStore(t)
	if err := s.Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("loaded %d entries from an empty queue", len(got))
	}
}

// A queued message has to come back off disk as the message it was: the reply it answers
// and the pills written into it, not just the words.
func TestReplyAndMentionsSurviveTheFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "scheduled.toml")
	store := New(path)
	at := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	want := domain.ScheduledMessage{
		ID: "q1", RoomID: "!a:x", Body: "yes, agreed — thanks Dana",
		ReplyTo:  "$their-message",
		Mentions: []domain.Mention{{UserID: "@dana:x", Name: "Dana"}},
		At:       at, Written: at.Add(-time.Hour),
	}
	if err := store.Save([]domain.ScheduledMessage{want}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d entries, want 1", len(got))
	}
	if got[0].ReplyTo != want.ReplyTo {
		t.Errorf("reply_to = %q, want %q", got[0].ReplyTo, want.ReplyTo)
	}
	if len(got[0].Mentions) != 1 || got[0].Mentions[0].UserID != "@dana:x" ||
		got[0].Mentions[0].Name != "Dana" {
		t.Errorf("mentions = %+v, want Dana's pill", got[0].Mentions)
	}
	// It is a queue somebody can read and edit, so the keys have to be legible.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`reply_to = "$their-message"`, "[[message.mention]]", `user_id = "@dana:x"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the file does not contain %s:\n%s", want, body)
		}
	}
}

// A hand-written entry without the optional keys loads with no reply and no pills.
func TestAMinimalEntryLoads(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "scheduled.toml")
	if err := os.WriteFile(path, []byte(`
[[message]]
id = "old"
room = "!a:x"
body = "good morning"
at = "2026-09-08T09:00:00Z"
written = "2026-09-08T08:00:00Z"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(path).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].Body != "good morning" {
		t.Fatalf("loaded %+v, want the one entry", got)
	}
	if got[0].ReplyTo != "" || got[0].Mentions != nil {
		t.Errorf("entry = %+v, want no reply and no pills", got[0])
	}
}
