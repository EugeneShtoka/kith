package matrix

import (
	"context"
	"path/filepath"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// backendWithCache is an InProc on a fresh cache, with a client that knows who we are.
func backendWithCache(t *testing.T, me id.UserID) *InProc {
	t.Helper()
	cache := testCache(t)
	client, err := mautrix.NewClient("https://example.org", me, "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	b := New(cache)
	b.client = client
	return b
}

// A refusal is learned only from a reaction of ours, redacted by someone else, who is a
// bridge; each condition is checked by removing it.
func TestRefusalLearnedOnlyFromABridgeTakingOursAway(t *testing.T) {
	t.Parallel()

	const me = id.UserID("@eugene:example.org")
	ctx := context.Background()

	cases := map[string]struct {
		reactionSender string
		redactedBy     string
		want           bool
	}{
		"a bridge removes ours":           {string(me), "@telegrambot:example.org", true},
		"we remove our own":               {string(me), string(me), false},
		"a bridge removes someone else's": {"@telegram_999:example.org", "@telegrambot:example.org", false},
		"a person removes ours":           {string(me), "@moderator:example.org", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := backendWithCache(t, me)
			b.noteRefusedReaction(ctx,
				domain.Reaction{RoomID: "!a:x", Sender: tc.reactionSender, Key: "🫶"},
				&event.Event{Sender: id.UserID(tc.redactedBy)})

			refusals, err := b.cache.ReactionRefusals(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(refusals) > 0; got != tc.want {
				t.Errorf("learned=%t, want %t (refusals=%+v)", got, tc.want, refusals)
			}
			if tc.want && refusals[0].Emoji != "🫶" {
				t.Errorf("recorded %q, want the emoji that was refused", refusals[0].Emoji)
			}
		})
	}
}

// testCache is a fresh cache, closed when the test ends.
func testCache(t *testing.T) *db.Cache {
	t.Helper()
	cache, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}
