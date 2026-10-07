package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A poll is kept with its message, rewritten as its results change, found by the ID
// its network names it by, and erased with a deleted message's content.
func TestAPollIsKeptWithItsMessage(t *testing.T) {
	t.Parallel()
	c, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("telegram:42/-11")
	poll := domain.Poll{ID: "777", Question: "Hike on Saturday?", Options: []domain.PollOption{
		{ID: "a", Text: "Yes", Votes: 2}, {ID: "b", Text: "No", Votes: 1},
	}}
	if err := c.SaveMessages(ctx, room, []domain.Message{
		{ID: "telegram:42/-11/5", RoomID: room, Body: poll.Summary(), Poll: &poll, Timestamp: time.Unix(1000, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.MessageByID(ctx, room, "telegram:42/-11/5")
	if err != nil || !ok || got.Poll == nil || got.Poll.Question != "Hike on Saturday?" || len(got.Poll.Options) != 2 {
		t.Fatalf("read back %+v, %v, %v", got.Poll, ok, err)
	}

	poll.Options[1].Votes, poll.Options[1].Mine = 2, true
	if err := c.SetPoll(ctx, room, "telegram:42/-11/5", poll); err != nil {
		t.Fatal(err)
	}
	found, err := c.PollsByID(ctx, domain.AccountRooms(domain.ProtocolTelegram, "42"), "777")
	if err != nil || len(found) != 1 || found[0].ID != "telegram:42/-11/5" || !found[0].Poll.Options[1].Mine {
		t.Fatalf("by id = %+v, %v", found, err)
	}
	if other, _ := c.PollsByID(ctx, domain.AccountRooms(domain.ProtocolTelegram, "43"), "777"); len(other) != 0 {
		t.Errorf("another account's rooms found %+v", other)
	}

	if err := c.MarkRedacted(ctx, room, "telegram:42/-11/5", "", "", time.Unix(2000, 0), false); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := c.MessageByID(ctx, room, "telegram:42/-11/5"); got.Poll != nil {
		t.Errorf("a deleted message kept its poll: %+v", got.Poll)
	}
	if err := c.SetPoll(ctx, room, "telegram:42/-11/5", poll); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := c.MessageByID(ctx, room, "telegram:42/-11/5"); got.Poll != nil {
		t.Error("a results update brought a deleted message's poll back")
	}
}
