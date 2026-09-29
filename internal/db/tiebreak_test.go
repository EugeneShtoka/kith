package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Bridged backfill often stamps a burst of messages with one time. Reads order ties
// by event ID, as domain.MergeMessages does, so a window around one message holds it
// and its true neighbors, and a page is the same on every read.
func TestMessagesSharingATimeKeepTheirOrder(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!r:x")
	same := time.UnixMilli(5000)
	// Saved out of order, so insertion order cannot pass for the right one.
	ids := []domain.EventID{"$c", "$a", "$e", "$b", "$d"}
	for _, id := range ids {
		mustSave(t, cache, room, domain.Message{ID: id, Sender: "@a:x", Body: string(id), Timestamp: same})
	}
	mustSave(t, cache, room, domain.Message{ID: "$later", Sender: "@a:x", Body: "later", Timestamp: same.Add(time.Second)})

	idsOf := func(msgs []domain.Message) []domain.EventID {
		out := make([]domain.EventID, len(msgs))
		for i := range msgs {
			out[i] = msgs[i].ID
		}
		return out
	}
	for _, tc := range []struct {
		target        domain.EventID
		before, after int
		want          []domain.EventID
	}{
		{"$c", 1, 1, []domain.EventID{"$b", "$c", "$d"}},
		{"$e", 2, 1, []domain.EventID{"$c", "$d", "$e", "$later"}},
		{"$a", 3, 0, []domain.EventID{"$a"}},
	} {
		got, err := cache.MessagesAround(ctx, room, tc.target, tc.before, tc.after)
		if err != nil {
			t.Fatalf("MessagesAround(%s): %v", tc.target, err)
		}
		if !slices.Equal(idsOf(got), tc.want) {
			t.Errorf("MessagesAround(%s, %d, %d) = %v, want %v", tc.target, tc.before, tc.after, idsOf(got), tc.want)
		}
	}

	page, err := cache.Messages(ctx, room, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []domain.EventID{"$d", "$e", "$later"}; !slices.Equal(idsOf(page), want) {
		t.Errorf("Messages(3) = %v, want %v", idsOf(page), want)
	}
}
