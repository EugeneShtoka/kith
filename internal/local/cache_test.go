package local

import (
	"context"
	"errors"
	"testing"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// seedRoom registers a room, which every other table references.
func seedRoom(t *testing.T, cache *db.Cache, roomID domain.RoomID) {
	t.Helper()
	if err := cache.SaveRooms(context.Background(), domain.MatrixRooms, []domain.Room{{ID: roomID, Name: "Room"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
}

func TestRecordEmojiFeedsTheRanking(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, "@me:example.org")
	seedRoom(t, b.cache, "!r:x")

	for range 3 {
		if err := b.RecordEmoji(ctx, domain.EmojiComposed, "!r:x", "🎉"); err != nil {
			t.Fatalf("RecordEmoji: %v", err)
		}
	}
	if err := b.RecordEmoji(ctx, domain.EmojiComposed, "!r:x", "👍"); err != nil {
		t.Fatalf("RecordEmoji: %v", err)
	}

	got, err := b.EmojiScores(ctx, domain.EmojiComposed, "!r:x", nil, "room")
	if err != nil {
		t.Fatalf("EmojiScores: %v", err)
	}
	if got["🎉"] <= got["👍"] {
		t.Errorf("scores = %v, want the most-used ranked highest", got)
	}
	// Composed and reaction emoji are ranked separately.
	other, err := b.EmojiScores(ctx, domain.EmojiReaction, "!r:x", nil, "room")
	if err != nil {
		t.Fatalf("EmojiScores(reaction): %v", err)
	}
	if len(other) != 0 {
		t.Errorf("react ranking = %v, want nothing — those were composed", other)
	}
}

// A service without a cache answers every read with nothing rather than failing.
func TestCachelessReadsAnswerNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := New(nil, nil)

	if got, err := b.CachedTimeline(ctx, "!r:x"); err != nil || got != nil {
		t.Errorf("CachedTimeline = (%v, %v), want nothing", got, err)
	}
	if got, err := b.CachedReactions(ctx, "!r:x"); err != nil || got != nil {
		t.Errorf("CachedReactions = (%v, %v), want nothing", got, err)
	}
	if got, err := b.SenderSlots(ctx, "!r:x"); err != nil || got != nil {
		t.Errorf("SenderSlots = (%v, %v), want nothing", got, err)
	}
	if err := b.SaveSenderSlots(ctx, "!r:x", map[string]int{"@a:x": 1}); err != nil {
		t.Errorf("SaveSenderSlots with no cache: %v", err)
	}
	if got, err := b.SearchSenders(ctx, domain.EveryRoom(), 10); err != nil || got != nil {
		t.Errorf("SearchSenders = (%v, %v), want nothing", got, err)
	}
	if got, err := b.EmojiScores(ctx, domain.EmojiReaction, "!r:x", nil, "room"); err != nil || got != nil {
		t.Errorf("EmojiScores = (%v, %v), want nothing", got, err)
	}
	if err := b.RecordEmoji(ctx, domain.EmojiReaction, "!r:x", "👍"); err != nil {
		t.Errorf("RecordEmoji with no cache: %v", err)
	}
}

func TestRecordReactionRefusalFromTheClient(t *testing.T) {
	t.Parallel()

	b := backendWithCache(t, "@eugene:example.org")
	ctx := context.Background()
	if err := b.RecordReactionRefusal(ctx, "RCS", "🤯"); err != nil {
		t.Fatal(err)
	}
	refusals, err := b.ReactionRefusals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refusals) != 1 || refusals[0].Protocol != "RCS" || refusals[0].Emoji != "🤯" {
		t.Errorf("refusals = %+v, want one RCS/🤯", refusals)
	}
}

// Clearing empties the cache and has the network refill it from the start.
func TestClearCacheEmptiesItAndRewinds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	net := &fakeNetwork{account: "@me:x"}
	s := New(testCache(t), net)
	seedRoom(t, s.cache, "!r:x")
	if err := s.ClearCache(ctx); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	if rooms, err := s.cache.Rooms(ctx); err != nil || len(rooms) != 0 {
		t.Errorf("rooms after clearing = (%v, %v), want none", rooms, err)
	}
	if net.rewinds != 1 {
		t.Errorf("rewinds = %d, want 1: an emptied cache nobody refills stays empty", net.rewinds)
	}
}

// Clearing with no cache open says so: success would read as done.
func TestClearingWithNoCacheIsAnError(t *testing.T) {
	t.Parallel()
	if err := New(nil, nil).ClearCache(context.Background()); !errors.Is(err, errNoCache) {
		t.Fatalf("ClearCache() with no cache = %v, want errNoCache", err)
	}
}
