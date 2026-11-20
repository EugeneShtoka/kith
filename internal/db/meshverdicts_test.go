package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A verdict another bridge made is kept only where it wins over the cache's own, checked
// as it is written: a release made here while the follower read the mesh stays; a later
// clearing removes it; a room the cache does not hold is left out, and the rest of the
// batch is kept beside it.
func TestKeepVerdictsKeepsOnlyWhatWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := openTemp(t)
	const room, other, unheld = domain.RoomID("tg:-1001"), domain.RoomID("tg:-1002"), domain.RoomID("tg:-1003")
	if err := c.KeepRoomNetworks(ctx, map[domain.RoomID]domain.Protocol{room: domain.ProtocolTelegram, other: domain.ProtocolTelegram}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Millisecond)
	if err := c.MarkTag(ctx, domain.TagVerdict{Room: room, Tag: "Spam", Released: true, At: at}); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkPerson(ctx, domain.PersonVerdict{Person: "tg:555", Tag: "Spam", Released: true, At: at}); err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Minute)
	changed, err := c.KeepVerdicts(ctx, []domain.TagVerdict{
		{Room: room, Tag: "spam", Rule: domain.MoveAlways, At: later},
		{Room: unheld, Tag: "spam", Rule: domain.MoveAlways, At: later},
		{Room: other, Tag: "spam", Rule: domain.MoveAlways, At: later},
	}, []domain.PersonVerdict{{Person: "tg:555", Tag: "spam", Rule: domain.MoveSenderFirst, At: later}})
	if err != nil || !changed {
		t.Fatalf("KeepVerdicts = %v, %v; want the other room's move kept", changed, err)
	}
	tags, err := c.TagVerdicts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || !tags[0].In() && !tags[1].In() || tags[0].Room == unheld || tags[1].Room == unheld {
		t.Errorf("TagVerdicts = %+v, want the release and the other room's move", tags)
	}
	for _, v := range tags {
		if v.Room == room && !v.Released {
			t.Errorf("a later move undid the release: %+v", v)
		}
	}
	if people, _ := c.KeptPersonVerdicts(ctx); len(people) != 1 || !people[0].Released {
		t.Errorf("KeptPersonVerdicts = %+v, want the release", people)
	}
	if _, err := c.KeepVerdicts(ctx, []domain.TagVerdict{{Room: room, Tag: "spam", At: later}}, nil); err != nil {
		t.Fatal(err)
	}
	if tags, _ := c.RoomVerdicts(ctx, room); len(tags) != 0 {
		t.Errorf("a later clearing left %+v", tags)
	}
}
