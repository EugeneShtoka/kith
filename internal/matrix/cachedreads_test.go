package matrix

import (
	"context"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

const testUser = id.UserID("@me:example.org")

// at is a timestamp n seconds into a fixed day.
func at(n int) time.Time {
	return time.Date(2026, 9, 6, 12, 0, n, 0, time.UTC)
}

// seedRoom registers a room, which every other table references.
func seedRoom(t *testing.T, b *InProc, roomID domain.RoomID) {
	t.Helper()
	if err := b.cache.SaveRooms(context.Background(), []domain.Room{
		{ID: roomID, Name: "Room"},
	}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
}

// Whoever spoke most recently leads the mention list.
func TestMentionCandidatesLeadWithRecentSpeakers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, testUser)
	seedRoom(t, b, "!r:x")
	if err := b.cache.SaveMembers(ctx, "!r:x", []domain.Member{
		{UserID: "@alice:x", DisplayName: "Alice"},
		{UserID: "@zoe:x", DisplayName: "Zoe"},
	}); err != nil {
		t.Fatalf("SaveMembers: %v", err)
	}
	if err := b.cache.SaveMessages(ctx, "!r:x", []domain.Message{
		{ID: "$1", RoomID: "!r:x", Sender: "@zoe:x", Body: "hello", Timestamp: at(9)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}

	got, err := b.MentionCandidates(ctx, "!r:x", 0)
	if err != nil {
		t.Fatalf("MentionCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want both members", len(got))
	}
	if got[0].UserID != "@zoe:x" {
		t.Errorf("the list leads with %s, want the most recent speaker", got[0].UserID)
	}
}

func TestRecordEmojiFeedsTheRanking(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, testUser)
	seedRoom(t, b, "!r:x")

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

// The local unread count replaces the server's when the receipt is cached.
func TestCachedUnreadPrefersTheLocalCount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, testUser)
	seedRoom(t, b, "!r:x")
	if err := b.cache.SaveMessages(ctx, "!r:x", []domain.Message{
		{ID: "$read", RoomID: "!r:x", Sender: "@alice:x", Body: "seen", Timestamp: at(1)},
		{ID: "$1", RoomID: "!r:x", Sender: "@alice:x", Body: "one", Timestamp: at(2)},
		{ID: "$2", RoomID: "!r:x", Sender: "@alice:x", Body: "two", Timestamp: at(3)},
	}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	if err := b.cache.SaveUnread(ctx, domain.Unread{
		RoomID: "!r:x", ReadEvent: "$read", Notifications: 99,
	}, 0); err != nil {
		t.Fatalf("SaveUnread: %v", err)
	}

	got, err := b.CachedUnread(ctx)
	if err != nil {
		t.Fatalf("CachedUnread: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rooms, want the one", len(got))
	}
	if !got[0].Counted {
		t.Error("the row is not marked Counted, so the badge falls back to the server's number")
	}
	if got[0].Messages != 2 {
		t.Errorf("messages = %d, want the two after the receipt rather than the server's 99", got[0].Messages)
	}
}

// A backend without a cache answers every read with nothing rather than failing.
func TestCachelessReadsAnswerNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := New(nil)

	if got, err := b.CachedTimeline(ctx, "!r:x"); err != nil || got != nil {
		t.Errorf("CachedTimeline = (%v, %v), want nothing", got, err)
	}
	if got, err := b.CachedReactions(ctx, "!r:x"); err != nil || got != nil {
		t.Errorf("CachedReactions = (%v, %v), want nothing", got, err)
	}
	if got, err := b.CachedUnread(ctx); err != nil || got != nil {
		t.Errorf("CachedUnread = (%v, %v), want nothing", got, err)
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

// Regression: a deletion whose words were kept went blank once a server page (which
// carries no content for redactions) was fetched.
func TestAKeptDeletionSurvivesAPageFromTheServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := backendWithCache(t, testUser)
	seedRoom(t, b, "!r:x")

	const kept = "what it actually said"
	if err := b.cache.SaveMessages(ctx, "!r:x", []domain.Message{
		{ID: "$1", RoomID: "!r:x", Sender: "@her:x", Body: kept, HTML: "<b>" + kept + "</b>", Timestamp: at(1)},
		{ID: "$2", RoomID: "!r:x", Sender: "@her:x", Body: "erased", Timestamp: at(2)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.cache.MarkRedacted(ctx, "!r:x", "$1", "@her:x", "", time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	if err := b.cache.MarkRedacted(ctx, "!r:x", "$2", "@her:x", "", time.Time{}, false); err != nil {
		t.Fatal(err)
	}

	page := []domain.Message{
		{ID: "$1", RoomID: "!r:x", Sender: "@her:x", Redacted: true},
		{ID: "$2", RoomID: "!r:x", Sender: "@her:x", Redacted: true},
		{ID: "$3", RoomID: "!r:x", Sender: "@her:x", Body: "not deleted at all"},
	}
	b.restoreKept(ctx, "!r:x", page)

	if page[0].Body != kept {
		t.Errorf("kept deletion came back with body %q, want %q", page[0].Body, kept)
	}
	if page[0].HTML == "" {
		t.Error("the kept formatting was not restored with the words")
	}
	if page[1].Body != "" {
		t.Errorf("an erased deletion was given back %q — the erase must stay done", page[1].Body)
	}
	if page[2].Body != "not deleted at all" {
		t.Errorf("an ordinary message was disturbed: %q", page[2].Body)
	}
}

// Message history merges the cache (kept originals) and the server (every m.replace).
func TestMergingRevisionsUnionsBothSourcesOldestFirst(t *testing.T) {
	t.Parallel()

	kept := []domain.Revision{
		{ID: "$m", Body: "the original, which only this cache still has", At: at(10)},
		{ID: "$e1", Body: "kept copy", At: at(20)},
	}
	fetched := []domain.Revision{
		{ID: "$e1", Body: "fetched copy of the same edit", At: at(20)},
		{ID: "$e2", Body: "an edit this cache never saw", At: at(30)},
	}

	got := mergeRevisions(kept, fetched)
	if len(got) != 3 {
		t.Fatalf("%d versions, want 3: %+v", len(got), got)
	}
	for i, want := range []domain.EventID{"$m", "$e1", "$e2"} {
		if got[i].ID != want {
			t.Errorf("version %d = %q, want %q — oldest first", i+1, got[i].ID, want)
		}
	}
	// The cache wins a tie.
	if got[1].Body != "kept copy" {
		t.Errorf("the fetched copy overwrote the kept one: %q", got[1].Body)
	}
	if only := mergeRevisions(nil, fetched); len(only) != 2 {
		t.Errorf("%d versions from the server alone, want 2", len(only))
	}
	if only := mergeRevisions(kept, nil); len(only) != 2 {
		t.Errorf("%d versions from the cache alone, want 2", len(only))
	}
}
