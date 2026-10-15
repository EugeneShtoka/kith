package slack

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Hiding a person recounts the rooms they wrote in, and streams the new count.
func TestHidingAPersonRecountsTheirRooms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	a, cache := cached(t)
	const room domain.RoomID = "slack:T1/C1"
	start := time.Unix(1_700_000_000, 0)
	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "slack:T1/C1/1", RoomID: room, Sender: "slack:T1/U1", Body: "hi", Timestamp: start.Add(time.Minute)},
		{ID: "slack:T1/C1/2", RoomID: room, Sender: "slack:T1/U2", Body: "buy", Timestamp: start.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: room}, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	cache.OnHiddenChange(a.RecountUnread)
	if err := cache.UseHidden(ctx, []string{"slack:T1/U2"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-a.Unread():
		if u.RoomID != room || !u.Counted || u.Messages != 1 {
			t.Errorf("streamed %+v, want the room counted at 1 without the hidden person", u)
		}
	default:
		t.Fatal("hiding a person streamed no recount")
	}
}

// A person's mark is kept in the cache, which is all this network has, and follows a
// renamed tag.
func TestAPersonMarkIsKeptAndRetagged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	a, cache := cached(t)
	if err := a.MarkPerson(ctx, domain.PersonVerdict{Person: "slack:U2", Tag: "Spam", Rule: domain.MoveAlways, Filter: "crypto"}); err != nil {
		t.Fatal(err)
	}
	if err := a.RetagVerdicts(ctx, "Spam", "Junk"); err != nil {
		t.Fatal(err)
	}
	got, err := cache.PersonVerdicts(ctx)
	if err != nil || len(got) != 1 || got[0].Tag != "junk" || got[0].Filter != "crypto" {
		t.Errorf("kept %+v (%v), want the mark under Junk", got, err)
	}
}
