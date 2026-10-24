package matrix

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix/id"
)

// The identity listing the account is this person; taking it recounts counted rooms,
// so a badge that counted the puppet's messages comes down at once.
func TestTheIdentityListingTheAccountIsYou(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := backendWithCache(t, id.UserID("@me:x"))
	base := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	if err := b.cache.SaveMessages(ctx, mx+"!a:x", []domain.Message{
		{ID: mx + "$read", Sender: "matrix:@dana:x", Timestamp: base},
		{ID: mx + "$before", Sender: "matrix:@dana:x", Timestamp: base.Add(time.Minute)},
		{ID: mx + "$slack", Sender: "matrix:@slack_me:x", Timestamp: base.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.cache.SaveUnread(ctx, domain.Unread{RoomID: mx + "!a:x", ReadEvent: mx + "$read"}, base.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	b.unread.seed([]domain.Unread{b.countLocal(ctx, domain.Unread{RoomID: mx + "!a:x"})}, nil)
	if got := b.unread.get(mx + "!a:x").Messages; got != 2 {
		t.Fatalf("before the identities: %d unread, want 2 (the puppet counted as someone else)", got)
	}

	b.UseIdentities(ctx, [][]string{{"matrix:@dana:x", "matrix:@whatsapp_dana:x"}, {"matrix:@slack_me:x", "matrix:@me:x"}})
	if got, want := b.me(), []string{"matrix:@me:x", "matrix:@slack_me:x"}; !slices.Equal(got, want) {
		t.Errorf("me = %v, want %v", got, want)
	}
	if got := b.unread.get(mx + "!a:x").Messages; got != 0 {
		t.Errorf("after the identities: %d unread, want 0 (the puppet's message floors the room)", got)
	}
}
