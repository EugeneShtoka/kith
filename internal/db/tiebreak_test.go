package db

import (
	"context"
	"math/rand/v2"
	"slices"
	"strconv"
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

// A network that numbers a room's messages (Telegram) gives them whole seconds, so a
// busy room has many in one second, and IDs that are the room's then the number. By
// the ID's text, message 100 came before 99. Every read orders them by number:
// a page, a window around each one, a thread, the latest, search and the merge, with
// numbers crossing every digit count a room reaches and saved in no order.
func TestNumberedMessagesInOneSecondKeepTheirOrder(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("telegram:1/2")
	numbers := []int{7, 8, 9, 10, 11, 98, 99, 100, 101, 998, 999, 1000, 1001, 9999, 10000, 10001}
	second := time.UnixMilli(1_700_000_000_000)
	want := make([]domain.Message, 0, len(numbers)+3)
	root := domain.Message{ID: domain.EventID(room + "/1"), Sender: "@a:x", Body: "root", Timestamp: second.Add(-time.Minute)}
	want = append(want, root)
	for _, n := range numbers {
		want = append(want, domain.Message{
			ID: domain.EventID(string(room) + "/" + strconv.Itoa(n)), Sender: "@a:x",
			Body: "burst " + strconv.Itoa(n), Timestamp: second, ThreadRoot: root.ID,
		})
	}
	// One each side, so the burst sits between other seconds.
	want = slices.Insert(want, 1, domain.Message{ID: domain.EventID(room + "/5"), Sender: "@a:x", Body: "before", Timestamp: second.Add(-time.Second)})
	want = append(want, domain.Message{ID: domain.EventID(room + "/3"), Sender: "@a:x", Body: "after", Timestamp: second.Add(time.Second)})

	shuffled := slices.Clone(want)
	rand.New(rand.NewPCG(1, 2)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	for _, m := range shuffled {
		mustSave(t, cache, room, m)
	}
	idsOf := func(msgs []domain.Message) []domain.EventID {
		out := make([]domain.EventID, len(msgs))
		for i := range msgs {
			out[i] = msgs[i].ID
		}
		return out
	}
	wantIDs := idsOf(want)

	if got := idsOf(domain.MergeMessages(nil, shuffled)); !slices.Equal(got, wantIDs) {
		t.Errorf("MergeMessages = %v,\nwant %v", got, wantIDs)
	}
	for n := 1; n <= len(want); n++ {
		page, err := cache.Messages(ctx, room, n)
		if err != nil {
			t.Fatal(err)
		}
		if got := idsOf(page); !slices.Equal(got, wantIDs[len(want)-n:]) {
			t.Errorf("Messages(%d) = %v, want %v", n, got, wantIDs[len(want)-n:])
		}
	}
	for i, target := range wantIDs {
		for _, span := range []int{1, 3} {
			got, err := cache.MessagesAround(ctx, room, target, span, span)
			if err != nil {
				t.Fatal(err)
			}
			lo, hi := max(i-span, 0), min(i+span+1, len(want))
			if !slices.Equal(idsOf(got), wantIDs[lo:hi]) {
				t.Errorf("MessagesAround(%s, %d, %d) = %v, want %v", target, span, span, idsOf(got), wantIDs[lo:hi])
			}
		}
	}
	thread, err := cache.ThreadMessages(ctx, room, root.ID, len(numbers))
	if err != nil {
		t.Fatal(err)
	}
	if got, w := idsOf(thread), append([]domain.EventID{root.ID}, wantIDs[2:2+len(numbers)]...); !slices.Equal(got, w) {
		t.Errorf("ThreadMessages = %v,\nwant %v", got, w)
	}
	last := wantIDs[1+len(numbers)]
	if got, lerr := cache.LatestInThread(ctx, room, root.ID); lerr != nil || got != last {
		t.Errorf("LatestInThread = %s (%v), want %s", got, lerr, last)
	}
	if got, lerr := cache.LatestEvents(ctx, []domain.RoomID{room}); lerr != nil || got[room] != wantIDs[len(want)-1] {
		t.Errorf("LatestEvents = %v (%v), want %s", got, lerr, wantIDs[len(want)-1])
	}
	hits, err := cache.SearchMessages(ctx, domain.SearchRequest{
		Filter: domain.SearchFilter{Terms: "burst"}, Rooms: domain.TheseRooms([]domain.RoomID{room}), Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	newestFirst := slices.Clone(wantIDs[2 : 2+len(numbers)])
	slices.Reverse(newestFirst)
	got := make([]domain.EventID, len(hits))
	for i := range hits {
		got[i] = hits[i].EventID
	}
	if !slices.Equal(got, newestFirst) {
		t.Errorf("search = %v,\nwant %v", got, newestFirst)
	}
}
