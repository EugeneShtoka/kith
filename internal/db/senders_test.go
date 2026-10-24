package db

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A sender's messages come from every room, oldest first, from since on and up to the
// limit; deleted ones are left out.
func TestSenderMessagesSpanRooms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := openTemp(t)
	at := func(i int) time.Time { return time.Unix(1_700_000_000+int64(i)*60, 0) }
	for room, msgs := range map[domain.RoomID][]domain.Message{
		mx + "!a:x": {{ID: mx + "$a1", Sender: "matrix:@spam:x", Body: "one", Timestamp: at(1)}, {ID: mx + "$a2", Sender: "matrix:@ann:x", Body: "hi", Timestamp: at(2)}},
		mx + "!b:x": {{ID: mx + "$b1", Sender: "matrix:@spam:x", Body: "two", Timestamp: at(3)}, {ID: mx + "$b2", Sender: "matrix:@spam:x", Body: "gone", Timestamp: at(4)}},
		mx + "!c:x": {{ID: mx + "$c1", Sender: "matrix:@spam:x", Body: "three", Timestamp: at(5)}},
	} {
		for i := range msgs {
			msgs[i].RoomID = room
		}
		if err := c.SaveMessages(ctx, room, msgs); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.MarkRedacted(ctx, mx+"!b:x", mx+"$b2", "matrix:@spam:x", "", at(6), false); err != nil {
		t.Fatal(err)
	}
	ids := func(since time.Time, limit int) []domain.EventID {
		t.Helper()
		got, err := c.SenderMessages(ctx, "matrix:@spam:x", since, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []domain.EventID
		for _, m := range got {
			if m.RoomID == "" || m.Sender != "matrix:@spam:x" {
				t.Errorf("message %+v lacks its room or sender", m)
			}
			out = append(out, m.ID)
		}
		return out
	}
	if got := ids(time.Time{}, 10); !slices.Equal(got, []domain.EventID{mx + "$a1", mx + "$b1", mx + "$c1"}) {
		t.Errorf("everything: %v", got)
	}
	if got := ids(at(3), 10); !slices.Equal(got, []domain.EventID{mx + "$b1", mx + "$c1"}) {
		t.Errorf("since the third minute: %v", got)
	}
	if got := ids(time.Time{}, 2); !slices.Equal(got, []domain.EventID{mx + "$a1", mx + "$b1"}) {
		t.Errorf("the first two: %v", got)
	}
}

// You have written to the others in a DM you wrote in, and to whomever you replied to;
// not to someone in a group you only wrote in, nor in a DM you never answered.
func TestWrittenToIsDMsYouAnsweredAndReplies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := openTemp(t)
	if err := c.SaveRooms(ctx, mxRooms, []domain.Room{
		{ID: mx + "!dm:x", IsDirect: true}, {ID: mx + "!quiet:x", IsDirect: true}, {ID: mx + "!group:x"},
	}); err != nil {
		t.Fatal(err)
	}
	at := func(i int) time.Time { return time.Unix(1_700_000_000+int64(i)*60, 0) }
	me := []string{"matrix:@me:x", "matrix:@whatsapp_me:x"}
	for room, msgs := range map[domain.RoomID][]domain.Message{
		mx + "!dm:x":    {{ID: mx + "$d1", Sender: "matrix:@ann:x", Body: "hi", Timestamp: at(1)}, {ID: mx + "$d2", Sender: "matrix:@whatsapp_me:x", Body: "hey", Timestamp: at(2)}},
		mx + "!quiet:x": {{ID: mx + "$q1", Sender: "matrix:@spam:x", Body: "buy", Timestamp: at(1)}},
		mx + "!group:x": {
			{ID: mx + "$g1", Sender: "matrix:@bob:x", Body: "news", Timestamp: at(1)},
			{ID: mx + "$g2", Sender: "matrix:@cat:x", Body: "lunch?", Timestamp: at(2)},
			{ID: mx + "$g3", Sender: "matrix:@me:x", Body: "yes", ReplyTo: mx + "$g2", Timestamp: at(3)},
			{ID: mx + "$g4", Sender: "matrix:@me:x", Body: "me too", ReplyTo: mx + "$g3", Timestamp: at(4)},
		},
	} {
		for i := range msgs {
			msgs[i].RoomID = room
		}
		if err := c.SaveMessages(ctx, room, msgs); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.WrittenTo(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{"matrix:@ann:x", "matrix:@cat:x"}; !slices.Equal(got, want) {
		t.Errorf("written to %v, want %v", got, want)
	}
}
