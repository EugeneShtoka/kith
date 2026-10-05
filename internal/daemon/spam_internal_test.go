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

// spamStoreStub is a spamStore whose recorded verdicts the test replaces, as syncs from
// the account's other machines would.
type spamStoreStub struct {
	mu       sync.Mutex
	verdicts []domain.SpamVerdict
}

func (s *spamStoreStub) MarkSpam(context.Context, domain.SpamVerdict) error { return nil }

func (s *spamStoreStub) SpamRooms(context.Context) ([]domain.SpamVerdict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.SpamVerdict(nil), s.verdicts...), nil
}

func (s *spamStoreStub) CachedTimeline(context.Context, domain.RoomID) ([]domain.Message, error) {
	return nil, nil
}

func (s *spamStoreStub) set(verdicts ...domain.SpamVerdict) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verdicts = verdicts
}

// A release made on another machine reaches a daemon nobody reloads.
func TestTheCaughtMemoIsReReadFromTheCache(t *testing.T) {
	t.Parallel()

	store := &spamStoreStub{}
	store.set(domain.SpamVerdict{Room: "!a:x", Rule: domain.SpamFirstMessage, Filter: "crypto"})
	clock := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	w := newSpamWatch(store)
	w.mine = func(sender string) bool { return sender == "@me:x" }
	w.now = func() time.Time { return clock }

	ctx := context.Background()
	message := domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", Body: "morning"}
	if !w.spam(ctx, message, domain.RoomFacts{ID: "!a:x"}) {
		t.Fatal("a caught room is not in Spam")
	}

	store.set(domain.SpamVerdict{Room: "!a:x", Released: true})
	if !w.spam(ctx, message, domain.RoomFacts{ID: "!a:x"}) {
		t.Fatal("the memo was re-read on every message; it is meant to be a map lookup")
	}
	clock = clock.Add(spamReseed + time.Second)
	if w.spam(ctx, message, domain.RoomFacts{ID: "!a:x"}) {
		t.Error("a room released elsewhere is still in Spam here after the memo went stale")
	}
}

// failingSpamStore refuses every write and read, the way a cache on a full disk would.
type failingSpamStore struct{ spamStoreStub }

func (*failingSpamStore) MarkSpam(context.Context, domain.SpamVerdict) error {
	return errors.New("database is locked")
}

func (*failingSpamStore) SpamRooms(context.Context) ([]domain.SpamVerdict, error) {
	return nil, errors.New("database is locked")
}

// A verdict that could not be written still holds in memory, but the journal hears
// that it will not survive a restart; a failed reseed is logged too.
func TestSpamStoreFailuresAreLogged(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	w := newSpamWatch(&failingSpamStore{})
	w.log = slog.New(slog.NewTextHandler(&out, nil))

	w.seed(context.Background())
	if !w.record(context.Background(), domain.SpamVerdict{Room: "!r:x", Rule: domain.SpamFirstMessage}) {
		t.Error("a verdict whose write failed must still hold for this process")
	}
	got := out.String()
	for _, want := range []string{"reread the caught rooms failed", "record the verdict failed", "room=!r:x", "database is locked", "level=WARN"} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
}
