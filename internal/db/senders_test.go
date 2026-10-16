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
		"!a:x": {{ID: "$a1", Sender: "@spam:x", Body: "one", Timestamp: at(1)}, {ID: "$a2", Sender: "@ann:x", Body: "hi", Timestamp: at(2)}},
		"!b:x": {{ID: "$b1", Sender: "@spam:x", Body: "two", Timestamp: at(3)}, {ID: "$b2", Sender: "@spam:x", Body: "gone", Timestamp: at(4)}},
		"!c:x": {{ID: "$c1", Sender: "@spam:x", Body: "three", Timestamp: at(5)}},
	} {
		for i := range msgs {
			msgs[i].RoomID = room
		}
		if err := c.SaveMessages(ctx, room, msgs); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.MarkRedacted(ctx, "!b:x", "$b2", "@spam:x", "", at(6), false); err != nil {
		t.Fatal(err)
	}
	ids := func(since time.Time, limit int) []domain.EventID {
		t.Helper()
		got, err := c.SenderMessages(ctx, "@spam:x", since, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []domain.EventID
		for _, m := range got {
			if m.RoomID == "" || m.Sender != "@spam:x" {
				t.Errorf("message %+v lacks its room or sender", m)
			}
			out = append(out, m.ID)
		}
		return out
	}
	if got := ids(time.Time{}, 10); !slices.Equal(got, []domain.EventID{"$a1", "$b1", "$c1"}) {
		t.Errorf("everything: %v", got)
	}
	if got := ids(at(3), 10); !slices.Equal(got, []domain.EventID{"$b1", "$c1"}) {
		t.Errorf("since the third minute: %v", got)
	}
	if got := ids(time.Time{}, 2); !slices.Equal(got, []domain.EventID{"$a1", "$b1"}) {
		t.Errorf("the first two: %v", got)
	}
}

// You have written to the others in a DM you wrote in, and to whomever you replied to;
// not to someone in a group you only wrote in, nor in a DM you never answered.
func TestWrittenToIsDMsYouAnsweredAndReplies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c := openTemp(t)
	if err := c.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{
		{ID: "!dm:x", IsDirect: true}, {ID: "!quiet:x", IsDirect: true}, {ID: "!group:x"},
	}); err != nil {
		t.Fatal(err)
	}
	at := func(i int) time.Time { return time.Unix(1_700_000_000+int64(i)*60, 0) }
	me := []string{"@me:x", "@whatsapp_me:x"}
	for room, msgs := range map[domain.RoomID][]domain.Message{
		"!dm:x":    {{ID: "$d1", Sender: "@ann:x", Body: "hi", Timestamp: at(1)}, {ID: "$d2", Sender: "@whatsapp_me:x", Body: "hey", Timestamp: at(2)}},
		"!quiet:x": {{ID: "$q1", Sender: "@spam:x", Body: "buy", Timestamp: at(1)}},
		"!group:x": {
			{ID: "$g1", Sender: "@bob:x", Body: "news", Timestamp: at(1)},
			{ID: "$g2", Sender: "@cat:x", Body: "lunch?", Timestamp: at(2)},
			{ID: "$g3", Sender: "@me:x", Body: "yes", ReplyTo: "$g2", Timestamp: at(3)},
			{ID: "$g4", Sender: "@me:x", Body: "me too", ReplyTo: "$g3", Timestamp: at(4)},
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
	if want := []string{"@ann:x", "@cat:x"}; !slices.Equal(got, want) {
		t.Errorf("written to %v, want %v", got, want)
	}
}
