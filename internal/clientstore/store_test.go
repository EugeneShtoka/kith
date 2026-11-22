package clientstore_test

import (
	"crypto/rand"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/driver"

	"github.com/EugeneShtoka/kith/internal/clientstore"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, sealeddb.KeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func open(t *testing.T, path string, key []byte) *clientstore.Store {
	t.Helper()
	s, err := clientstore.Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func msg(event string, at int, body string) clientstore.Message {
	return clientstore.Message{Event: event, At: time.UnixMilli(int64(at)), Message: []byte(body)}
}

func bodies(t *testing.T, s *clientstore.Store, room string) []string {
	t.Helper()
	rows, err := s.Timeline(t.Context(), room, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(rows))
	for i, b := range rows {
		out[i] = string(b)
	}
	return out
}

// A room's messages come oldest first, the newest limit of them; a message written
// again is one row; the stream's position is kept with what it wrote.
func TestTheStreamWritesRowsAndItsPosition(t *testing.T) {
	t.Parallel()
	s := open(t, filepath.Join(t.TempDir(), "proxy.db"), newKey(t))
	ctx := t.Context()
	if err := s.PutStream(ctx, "!r", []clientstore.Message{msg("$2", 2, "two"), msg("$1", 1, "one")}, "messages", "e", 7); err != nil {
		t.Fatal(err)
	}
	if err := s.PutStream(ctx, "!r", []clientstore.Message{msg("$1", 1, "one, edited")}, "messages", "e", 8); err != nil {
		t.Fatal(err)
	}
	if got := bodies(t, s, "!r"); !slices.Equal(got, []string{"one, edited", "two"}) {
		t.Errorf("timeline = %q", got)
	}
	rows, err := s.Timeline(ctx, "!r", 1)
	if err != nil || len(rows) != 1 || string(rows[0]) != "two" {
		t.Errorf("the newest one = %q, %v", rows, err)
	}
	if epoch, change, ferr := s.Position(ctx, "messages"); ferr != nil || epoch != "e" || change != 8 {
		t.Errorf("position = %q %d %v, want e 8", epoch, change, ferr)
	}
	// A row written again is renumbered: it is a change again, and its old number none.
	changes, err := s.ChangesAfter(ctx, 0, 10)
	if err != nil || len(changes) != 2 || string(changes[0].Message) != "two" ||
		string(changes[1].Message) != "one, edited" || changes[1].Seq != 3 || changes[1].Room != "!r" {
		t.Errorf("changes after 0 = %+v, %v", changes, err)
	}
}

// A page asked before a change keeps the row that change wrote; a page asked before
// the rooms were marked to fill again leaves its room to fill.
func TestAPageNeverOverwritesNewerWord(t *testing.T) {
	t.Parallel()
	s := open(t, filepath.Join(t.TempDir(), "proxy.db"), newKey(t))
	ctx := t.Context()
	if err := s.SetRooms(ctx, "Home", []string{"!r"}); err != nil {
		t.Fatal(err)
	}
	asked, err := s.Ask(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ferr := s.PutStream(ctx, "!r", []clientstore.Message{msg("$1", 1, "edited")}, "messages", "e", 1); ferr != nil {
		t.Fatal(ferr)
	}
	if ferr := s.PutPage(ctx, "!r", []clientstore.Message{msg("$1", 1, "as the page had it"), msg("$0", 0, "older")}, asked); ferr != nil {
		t.Fatal(ferr)
	}
	if got := bodies(t, s, "!r"); !slices.Equal(got, []string{"older", "edited"}) {
		t.Errorf("timeline = %q, want the page's older message and the stream's edit", got)
	}
	if left, ferr := s.Unfilled(ctx); ferr != nil || len(left) != 0 {
		t.Errorf("unfilled after the page = %v, %v", left, ferr)
	}

	asked, err = s.Ask(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Unfill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPage(ctx, "!r", nil, asked); err != nil {
		t.Fatal(err)
	}
	if left, err := s.Unfilled(ctx); err != nil || !slices.Equal(left, []string{"!r"}) {
		t.Errorf("unfilled after a page asked before Unfill = %v, %v; want !r", left, err)
	}
}

// Each profile keeps its set; a room no set holds goes, with everything kept of it,
// and the change numbers it had are never given again.
func TestARoomNoProfileHoldsIsDeleted(t *testing.T) {
	t.Parallel()
	s := open(t, filepath.Join(t.TempDir(), "proxy.db"), newKey(t))
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetRooms(ctx, "Home", []string{"!a", "!b"}))
	must(s.SetRooms(ctx, "Work", []string{"!b"}))
	must(s.PutStream(ctx, "!a", []clientstore.Message{msg("$1", 1, "a")}, "messages", "e", 1))
	must(s.PutRoomAnswer(ctx, "CachedReactions", "!a", []byte("r")))
	seq, err := s.Seq(ctx)
	must(err)
	for _, tc := range []struct {
		profile, room string
		want          bool
	}{{"Home", "!a", true}, {"Work", "!a", false}, {"Work", "!b", true}, {"Nobody", "!b", false}} {
		if got, err := s.Holds(ctx, tc.profile, tc.room); err != nil || got != tc.want {
			t.Errorf("%s holds %s = %v, %v", tc.profile, tc.room, got, err)
		}
	}

	must(s.SetRooms(ctx, "Home", []string{"!b"}))
	if got := bodies(t, s, "!a"); len(got) != 0 {
		t.Errorf("!a's messages after no profile holds it = %q", got)
	}
	if _, ok, err := s.RoomAnswer(ctx, "CachedReactions", "!a"); ok || err != nil {
		t.Errorf("!a's reactions after no profile holds it: %v, %v", ok, err)
	}
	must(s.PutStream(ctx, "!b", []clientstore.Message{msg("$2", 2, "b")}, "messages", "e", 2))
	if now, err := s.Seq(ctx); err != nil || now != seq+1 {
		t.Errorf("the next change = %d, %v; want %d", now, err, seq+1)
	}
}

// Answers are kept by method, profile and request.
func TestAnswersAreKeptByProfileAndRequest(t *testing.T) {
	t.Parallel()
	s := open(t, filepath.Join(t.TempDir(), "proxy.db"), newKey(t))
	ctx := t.Context()
	if err := s.PutAnswer(ctx, "Rooms", "Home", "", []byte("home")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAnswer(ctx, "Rooms", "Home", "", []byte("home again")); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.Answer(ctx, "Rooms", "Home", ""); err != nil || !ok || string(got) != "home again" {
		t.Errorf("Home's Rooms = %q %v %v", got, ok, err)
	}
	for _, miss := range [][3]string{{"Rooms", "Work", ""}, {"Rail", "Home", ""}, {"Rooms", "Home", "0a01"}} {
		if _, ok, err := s.Answer(ctx, miss[0], miss[1], miss[2]); ok || err != nil {
			t.Errorf("%v kept: %v %v", miss, ok, err)
		}
	}
	if err := s.PutMeta(ctx, "profiles", `["Home"]`); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Meta(ctx, "profiles"); err != nil || v != `["Home"]` {
		t.Errorf("meta = %q %v", v, err)
	}
	if v, err := s.Meta(ctx, "none"); err != nil || v != "" {
		t.Errorf("meta none = %q %v", v, err)
	}
}

// A cache is rebuilt on doubt: reopened under its key it keeps everything and its
// epoch; under another key, or of another layout, it opens empty, numbering anew.
func TestAStoreInDoubtIsRebuilt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "proxy.db")
	key := newKey(t)
	s, err := clientstore.Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	epoch := s.Epoch()
	if ferr := s.PutAnswer(t.Context(), "Rooms", "Home", "", []byte("x")); ferr != nil {
		t.Fatal(ferr)
	}
	if ferr := s.Close(); ferr != nil {
		t.Fatal(ferr)
	}

	again := open(t, path, key)
	if again.Epoch() != epoch {
		t.Errorf("epoch reopened = %s, want %s", again.Epoch(), epoch)
	}
	if _, ok, ferr := again.Answer(t.Context(), "Rooms", "Home", ""); !ok || ferr != nil {
		t.Errorf("an answer reopened: %v %v", ok, ferr)
	}
	if ferr := again.Close(); ferr != nil {
		t.Fatal(ferr)
	}

	other := open(t, path, newKey(t))
	if other.Epoch() == epoch {
		t.Error("a store opened under another key kept its epoch")
	}
	if _, ok, _ := other.Answer(t.Context(), "Rooms", "Home", ""); ok {
		t.Error("a store opened under another key kept an answer")
	}
	if ferr := other.Close(); ferr != nil {
		t.Fatal(ferr)
	}

	newer := filepath.Join(t.TempDir(), "newer.db")
	k := newKey(t)
	db, err := driver.Open(sealeddb.Target(newer, k))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rebuilt := open(t, newer, k)
	if v, err := rebuilt.Meta(t.Context(), "epoch"); err != nil || v != rebuilt.Epoch() {
		t.Errorf("a store of another layout = %q %v, want it made anew", v, err)
	}
}
