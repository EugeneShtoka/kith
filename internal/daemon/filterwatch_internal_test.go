package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// filterStoreStub is a filterStore whose verdicts the test replaces, as syncs from other
// machines would.
type filterStoreStub struct {
	mu       sync.Mutex
	verdicts []domain.TagVerdict
}

func (s *filterStoreStub) MarkTag(context.Context, domain.TagVerdict) error { return nil }

func (s *filterStoreStub) TagVerdicts(context.Context) ([]domain.TagVerdict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.TagVerdict(nil), s.verdicts...), nil
}

func (s *filterStoreStub) CachedTimeline(context.Context, domain.RoomID) ([]domain.Message, error) {
	return nil, nil
}

func (s *filterStoreStub) set(verdicts ...domain.TagVerdict) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verdicts = verdicts
}

// A release made on another machine reaches a daemon nobody reloads, once the memo is
// stale; until then the memo answers without a read.
func TestTheMoveMemoIsReReadFromTheCache(t *testing.T) {
	t.Parallel()

	store := &filterStoreStub{}
	store.set(domain.TagVerdict{Room: "!a:x", Tag: "spam", Rule: domain.MoveFirstMessage, Filter: "crypto"})
	clock := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	w := newFilterWatch(store)
	w.now = func() time.Time { return clock }
	w.reload(domain.Filters{{Name: "any", Tag: "Spam", Words: domain.Tracked{Words: []string{"morning"}}}}, domain.TagSet{})

	ctx := context.Background()
	message := domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", Body: "morning"}
	w.watch(ctx, message, domain.RoomFacts{ID: "!a:x"})
	if got := w.caught("!a:x"); len(got) != 1 || got[0] != "spam" {
		t.Fatalf("caught = %v, want the stored move", got)
	}
	store.set(domain.TagVerdict{Room: "!a:x", Tag: "Spam", Released: true})
	w.watch(ctx, message, domain.RoomFacts{ID: "!a:x"})
	if len(w.caught("!a:x")) != 1 {
		t.Fatal("the memo was re-read on every message; it is meant to be a map lookup")
	}
	clock = clock.Add(filterReseed + time.Second)
	w.watch(ctx, message, domain.RoomFacts{ID: "!a:x"})
	if got := w.caught("!a:x"); len(got) != 0 {
		t.Errorf("caught = %v after the memo went stale, want the release here too", got)
	}
}

// failingFilterStore refuses every read and write, like a cache on a full disk.
type failingFilterStore struct{ filterStoreStub }

func (*failingFilterStore) MarkTag(context.Context, domain.TagVerdict) error {
	return errors.New("database is locked")
}

func (*failingFilterStore) TagVerdicts(context.Context) ([]domain.TagVerdict, error) {
	return nil, errors.New("database is locked")
}

// An unwritten move holds in memory, and the journal hears it will not survive a
// restart; a failed reseed is logged too.
func TestFilterStoreFailuresAreLogged(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	w := newFilterWatch(&failingFilterStore{})
	w.log = slog.New(slog.NewTextHandler(&out, nil))

	w.seed(context.Background())
	if !w.record(context.Background(), domain.TagVerdict{Room: "!r:x", Tag: "Spam", Rule: domain.MoveFirstMessage}) {
		t.Error("a move whose write failed must still hold for this process")
	}
	if got := w.caught("!r:x"); len(got) != 1 || got[0] != "spam" {
		t.Errorf("caught = %v, want the unwritten move", got)
	}
	got := out.String()
	for _, want := range []string{"reread the moves failed", "record the move failed", "room=!r:x", "database is locked", "level=WARN"} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
}
