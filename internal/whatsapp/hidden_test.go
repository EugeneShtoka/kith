package whatsapp

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
	a, cache, _ := offline(t)
	const room domain.RoomID = "whatsapp:15550000001/120363@g.us"
	start := time.Unix(1_700_000_000, 0)
	if err := cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "whatsapp:15550000001/120363@g.us/1", RoomID: room, Sender: "whatsapp:15550000002@s.whatsapp.net", Body: "hi", Timestamp: start.Add(time.Minute)},
		{ID: "whatsapp:15550000001/120363@g.us/2", RoomID: room, Sender: "whatsapp:15550000003@s.whatsapp.net", Body: "buy", Timestamp: start.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: room}, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	cache.OnHiddenChange(a.RecountUnread)
	if err := cache.UseHidden(ctx, []string{"whatsapp:15550000003@s.whatsapp.net"}, nil, nil); err != nil {
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
	a, cache, _ := offline(t)
	if err := a.MarkPerson(ctx, domain.PersonVerdict{Person: "whatsapp:15550000003@s.whatsapp.net", Tag: "Spam", Rule: domain.MoveAlways, Filter: "crypto"}); err != nil {
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
