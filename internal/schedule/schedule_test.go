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

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	s := tempStore(t)
	at := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	want := []domain.ScheduledMessage{{
		ID: "a1", RoomID: mx + "!a:x", Body: "morning", ThreadRoot: mx + "$root", Emote: true,
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
	if g.ID != "a1" || g.RoomID != mx+"!a:x" || g.Body != "morning" || g.ThreadRoot != mx+"$root" || !g.Emote {
		t.Errorf("entry came back as %+v", g)
	}
	if !g.At.Equal(at) {
		t.Errorf("At = %v, want %v", g.At, at)
	}
	if !g.Written.Equal(at.Add(-time.Hour)) {
		t.Errorf("Written = %v, want an hour before the send time", g.Written)
	}
}

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

// The file stays owner-only across rewrites.
func TestTheFileIsOwnerOnly(t *testing.T) {
	t.Parallel()

	s := tempStore(t)
	at := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	for range 2 { // the mode must survive a rewrite
		if err := s.Save([]domain.ScheduledMessage{{ID: "a", RoomID: mx + "!a:x", Body: "hi", At: at}}); err != nil {
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

func TestSaveLeavesNoTempFiles(t *testing.T) {
	t.Parallel()

	s := tempStore(t)
	at := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	for i := range 3 {
		if err := s.Save([]domain.ScheduledMessage{{ID: string(rune('a' + i)), RoomID: mx + "!a:x", At: at}}); err != nil {
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

// An empty queue is written as an empty file, not removed.
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

func TestReplyAndMentionsSurviveTheFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "scheduled.toml")
	store := New(path)
	at := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	want := domain.ScheduledMessage{
		ID: "q1", RoomID: mx + "!a:x", Body: "yes, agreed — thanks Dana",
		ReplyTo:  mx + "$their-message",
		Mentions: []domain.Mention{{UserID: "matrix:@dana:x", Name: "Dana"}},
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
	if len(got[0].Mentions) != 1 || got[0].Mentions[0].UserID != "matrix:@dana:x" ||
		got[0].Mentions[0].Name != "Dana" {
		t.Errorf("mentions = %+v, want Dana's pill", got[0].Mentions)
	}
	// The file is hand-editable, so keys must be legible.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`reply_to = "` + mx + `$their-message"`, "[[message.mention]]", `user_id = "matrix:@dana:x"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the file does not contain %s:\n%s", want, body)
		}
	}
}

// An entry without the optional keys loads with no reply and no pills.
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
