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

// Bridged bursts share one time; reads break ties by event ID, as domain.MergeMessages
// does, so windows and pages are stable.
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

// Numbered IDs within one second (Telegram: "<room>/<n>") order by number, not text
// ("100" after "99"), in every read and the merge.
func TestNumberedMessagesInOneSecondKeepTheirOrder(t *testing.T) {
	t.Parallel()

	cache := openTemp(t)
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

	assertTimelineOrder(t, cache, room, root, want, len(numbers), "burst")
}

// assertTimelineOrder saves want shuffled and checks every read returns want's order:
// merge, pages, windows, the thread under root (want[2:2+threaded]), latest, search.
func assertTimelineOrder(t *testing.T, cache *Cache, room domain.RoomID, root domain.Message, want []domain.Message, threaded int, term string) {
	t.Helper()
	ctx := context.Background()
	shuffled := slices.Clone(want)
	rand.New(rand.NewPCG(1, 2)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	for i := range shuffled {
		mustSave(t, cache, room, shuffled[i])
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
	// The client re-sorts what it reads, so what is read must carry the order.
	read, rerr := cache.Messages(ctx, room, len(want))
	if rerr != nil {
		t.Fatal(rerr)
	}
	rand.New(rand.NewPCG(5, 6)).Shuffle(len(read), func(i, j int) { read[i], read[j] = read[j], read[i] })
	if got := idsOf(domain.MergeMessages(nil, read)); !slices.Equal(got, wantIDs) {
		t.Errorf("MergeMessages of what was read = %v,\nwant %v", got, wantIDs)
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
	thread, err := cache.ThreadMessages(ctx, room, root.ID, threaded)
	if err != nil {
		t.Fatal(err)
	}
	if got, w := idsOf(thread), append([]domain.EventID{root.ID}, wantIDs[2:2+threaded]...); !slices.Equal(got, w) {
		t.Errorf("ThreadMessages = %v,\nwant %v", got, w)
	}
	last := wantIDs[1+threaded]
	if got, lerr := cache.LatestInThread(ctx, room, root.ID); lerr != nil || got != last {
		t.Errorf("LatestInThread = %s (%v), want %s", got, lerr, last)
	}
	if got, lerr := cache.LatestEvents(ctx, []domain.RoomID{room}); lerr != nil || got[room] != wantIDs[len(want)-1] {
		t.Errorf("LatestEvents = %v (%v), want %s", got, lerr, wantIDs[len(want)-1])
	}
	hits, err := cache.SearchMessages(ctx, domain.SearchRequest{
		Filter: domain.SearchFilter{Terms: term}, Rooms: domain.TheseRooms([]domain.RoomID{room}), Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	newestFirst := slices.Clone(wantIDs[2 : 2+threaded])
	slices.Reverse(newestFirst)
	got := make([]domain.EventID, len(hits))
	for i := range hits {
		got[i] = hits[i].EventID
	}
	if !slices.Equal(got, newestFirst) {
		t.Errorf("search = %v,\nwant %v", got, newestFirst)
	}
}

// With unordered IDs in one second (WhatsApp), Seq orders every read; a message seen
// again keeps its first Seq, and the Seq row goes with the message.
func TestGivenPlacesOrderMessagesInOneSecond(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("whatsapp:1/2")
	second := time.UnixMilli(1_700_000_000_000)
	rng := rand.New(rand.NewPCG(3, 4))
	// Random mixed-length IDs, so neither length nor text gives the order.
	randomID := func() domain.EventID {
		const hex = "0123456789ABCDEF"
		b := make([]byte, 16+rng.IntN(8))
		for i := range b {
			b[i] = hex[rng.IntN(len(hex))]
		}
		return domain.EventID(string(room) + "/" + string(b))
	}
	root := domain.Message{ID: randomID(), Sender: "@a:x", Body: "root", Timestamp: second.Add(-time.Minute), Seq: 1}
	want := []domain.Message{root, {ID: randomID(), Sender: "@a:x", Body: "before", Timestamp: second.Add(-time.Second)}}
	const burst = 12
	for i := range burst {
		want = append(want, domain.Message{
			ID: randomID(), Sender: "@a:x", Body: "burst " + strconv.Itoa(i), Timestamp: second,
			ThreadRoot: root.ID, Seq: int64(1000 + i*7),
		})
	}
	want = append(want, domain.Message{ID: randomID(), Sender: "@a:x", Body: "after", Timestamp: second.Add(time.Second)})
	assertTimelineOrder(t, cache, room, root, want, burst, "burst")

	// Seen again with another place (history after live): it does not move.
	again := want[2]
	again.Seq = 1_000_000
	mustSave(t, cache, room, again)
	got, err := cache.MessagesAround(ctx, room, again.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != want[2].Seq || got[1].ID != want[3].ID {
		t.Errorf("a message seen again moved: %+v", got)
	}

	// Trims and forgotten rooms delete rows; the place goes with its message.
	if _, err := cache.db.ExecContext(ctx, "DELETE FROM messages WHERE room_id = ? AND event_id = ?",
		string(room), string(want[3].ID)); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := cache.db.QueryRowContext(ctx, "SELECT count(*) FROM message_order WHERE room_id = ? AND event_id = ?",
		string(room), string(want[3].ID)).Scan(&left); err != nil || left != 0 {
		t.Errorf("a deleted message's place stayed (%d rows, %v)", left, err)
	}
}
