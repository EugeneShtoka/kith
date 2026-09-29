package matrix

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix/id"
)

// The identity that lists the account is this person; another person's identity is
// not. Taking it (at start, or on a reload) recounts the rooms already counted, so a
// badge that counted the puppet's messages as someone else's comes down at once.
func TestTheIdentityListingTheAccountIsYou(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := backendWithCache(t, id.UserID("@me:x"))
	base := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	if err := b.cache.SaveMessages(ctx, "!a:x", []domain.Message{
		{ID: "$read", Sender: "@dana:x", Timestamp: base},
		{ID: "$before", Sender: "@dana:x", Timestamp: base.Add(time.Minute)},
		{ID: "$slack", Sender: "@slack_me:x", Timestamp: base.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", ReadEvent: "$read"}, base.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	b.unread.seed([]domain.Unread{b.countLocal(ctx, domain.Unread{RoomID: "!a:x"})}, nil)
	if got := b.unread.get("!a:x").Messages; got != 2 {
		t.Fatalf("before the identities: %d unread, want 2 (the puppet counted as someone else)", got)
	}

	b.UseIdentities(ctx, [][]string{{"@dana:x", "@whatsapp_dana:x"}, {"@slack_me:x", "@me:x"}})
	if got, want := b.me(), []string{"@me:x", "@slack_me:x"}; !slices.Equal(got, want) {
		t.Errorf("me = %v, want %v", got, want)
	}
	if got := b.unread.get("!a:x").Messages; got != 0 {
		t.Errorf("after the identities: %d unread, want 0 (the puppet's message floors the room)", got)
	}
}
