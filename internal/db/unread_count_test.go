package db

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

const me = "@me:x"

// mine is every MXID that is this person.
var mine = []string{me}

// The count is what is unread, not what would have notified (the server's number);
// our own message floors what came before it.
func TestCountUnreadCountsWhatHasNotBeenRead(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	base := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }

	for _, msg := range []domain.Message{
		{ID: "$read", Sender: "@alice:x", Body: "before", Timestamp: at(0)},
		{ID: "$one", Sender: "@alice:x", Body: "after", Timestamp: at(1)},
		{ID: "$mine", Sender: me, Body: "sent from my phone", Timestamp: at(2)},
		{ID: "$gone", Sender: "@alice:x", Body: "deleted", Timestamp: at(3), Redacted: true},
		{ID: "$ping", Sender: "@alice:x", Body: "hey you", Timestamp: at(4), Mentioned: true},
	} {
		mustSave(t, cache, "!a:x", msg)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", ReadEvent: "$read"}, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}

	messages, mentions, counted, err := cache.CountUnread(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("CountUnread() error = %v", err)
	}
	if !counted {
		t.Fatal("counted = false for a room whose read event is cached")
	}
	if messages != 1 || mentions != 1 {
		t.Errorf("CountUnread() = %d messages, %d mentions; want 1, 1 — our own message floors what came before it", messages, mentions)
	}
}

// The floor is the later of the receipt and our own newest message.
func TestCountUnreadKeepsTheLaterFloor(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	base := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }

	for _, msg := range []domain.Message{
		{ID: "$mine", Sender: me, Body: "asked something", Timestamp: at(0)},
		{ID: "$answer", Sender: "@alice:x", Body: "answered it", Timestamp: at(1)},
		{ID: "$read", Sender: "@alice:x", Body: "and read this far", Timestamp: at(2)},
		{ID: "$after", Sender: "@alice:x", Body: "after the receipt", Timestamp: at(3)},
	} {
		mustSave(t, cache, "!a:x", msg)
	}
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", ReadEvent: "$read"}, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}

	messages, _, counted, err := cache.CountUnread(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("CountUnread() error = %v", err)
	}
	// Only $after: the receipt is newer than our message.
	if !counted || messages != 1 {
		t.Errorf("CountUnread() = %d (counted %v), want 1 — the receipt is the later floor", messages, counted)
	}
}

// A room whose read event is not cached comes back uncounted, so the caller falls
// back to the server's number.
func TestCountUnreadReportsWhenItCannotAnswer(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", Notifications: 12, ReadEvent: "$long-gone"}, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}

	for _, room := range []domain.RoomID{"!a:x", "!never-heard-of:x"} {
		messages, _, counted, err := cache.CountUnread(ctx, mine, room)
		if err != nil {
			t.Fatalf("CountUnread(%s) error = %v", room, err)
		}
		if counted || messages != 0 {
			t.Errorf("CountUnread(%s) = %d, counted=%v; want 0, false", room, messages, counted)
		}
	}
}

// A message sharing the receipt's millisecond counts as read, whatever its event ID.
func TestAMessageSharingTheReceiptsMillisecondCountsAsRead(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	same := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

	mustSave(t, cache, "!a:x", domain.Message{ID: "$aaa", Sender: "@alice:x", Body: "first", Timestamp: same})
	mustSave(t, cache, "!a:x", domain.Message{ID: "$bbb", Sender: "@alice:x", Body: "second", Timestamp: same})

	// Either way round, including the receipt on the lower ID.
	for _, read := range []domain.EventID{"$aaa", "$bbb"} {
		if err := cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", ReadEvent: read}, 0); err != nil {
			t.Fatalf("SaveUnread() error = %v", err)
		}
		messages, _, counted, err := cache.CountUnread(ctx, mine, "!a:x")
		if err != nil {
			t.Fatalf("CountUnread() error = %v", err)
		}
		if !counted {
			t.Fatalf("with the receipt on %s the room could not be counted", read)
		}
		if messages != 0 {
			t.Errorf("with the receipt on %s: CountUnread() = %d, want 0 — nothing is known to be after it", read, messages)
		}
	}

	// A later message is still counted.
	later := same.Add(time.Millisecond)
	mustSave(t, cache, "!a:x", domain.Message{ID: "$ccc", Sender: "@alice:x", Body: "after", Timestamp: later})
	messages, _, _, err := cache.CountUnread(ctx, mine, "!a:x")
	if err != nil {
		t.Fatalf("CountUnread() error = %v", err)
	}
	if messages != 1 {
		t.Errorf("CountUnread() = %d, want 1 — a message after the receipt's millisecond", messages)
	}
}

// CountUnreadAll omits rooms it cannot count.
func TestCountUnreadAllOmitsRoomsItCannotAnswerFor(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	base := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

	mustSave(t, cache, "!a:x", domain.Message{ID: "$read", Sender: "@alice:x", Body: "x", Timestamp: base})
	mustSave(t, cache, "!a:x", domain.Message{ID: "$new", Sender: "@alice:x", Body: "y", Timestamp: base.Add(time.Minute)})
	for _, u := range []domain.Unread{
		{RoomID: "!a:x", ReadEvent: "$read"},
		{RoomID: "!cold:x", Notifications: 7, ReadEvent: "$unknown"},
	} {
		if err := cache.SaveUnread(ctx, u, 0); err != nil {
			t.Fatalf("SaveUnread() error = %v", err)
		}
	}

	got, err := cache.CountUnreadAll(ctx, mine)
	if err != nil {
		t.Fatalf("CountUnreadAll() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("CountUnreadAll() returned %d rooms, want 1", len(got))
	}
	if a := got["!a:x"]; a.Messages != 1 || !a.Counted {
		t.Errorf("counted room = %+v, want 1 message and Counted", a)
	}
	if _, ok := got["!cold:x"]; ok {
		t.Error("a room with an unresolvable read position was reported as counted")
	}
}

// The read position only moves forward by time; counts and the mark are always replaced.
func TestSaveUnreadKeepsTheNewerPosition(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", ReadEvent: "$new"}, 200); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	older := domain.Unread{RoomID: "!a:x", Notifications: 4, ReadEvent: "$old", Marked: true}
	if err := cache.SaveUnread(ctx, older, 100); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	// An unknown time does not move a known position either.
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", Notifications: 4, ReadEvent: "$what", Marked: true}, 0); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	got, err := cache.Unread(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("Unread() = %v, %v", got, err)
	}
	want := domain.Unread{RoomID: "!a:x", Notifications: 4, ReadEvent: "$new", Marked: true}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("Unread() = %+v, want %+v", got[0], want)
	}
	positions, err := cache.ReadPositions(ctx)
	if err != nil || positions["!a:x"] != 200 {
		t.Errorf("ReadPositions() = %v, %v; want !a:x at 200", positions, err)
	}
}

// A read event the cache does not hold still counts once its time is stored.
func TestCountUnreadUsesTheStoredReadTime(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	base := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	mustSave(t, cache, "!a:x", domain.Message{ID: "$before", Sender: "@alice:x", Body: "read", Timestamp: base})
	mustSave(t, cache, "!a:x", domain.Message{ID: "$after", Sender: "@alice:x", Body: "unread", Timestamp: base.Add(2 * time.Minute)})
	if err := cache.SaveUnread(ctx, domain.Unread{RoomID: "!a:x", ReadEvent: "$uncached"}, base.Add(time.Minute).UnixMilli()); err != nil {
		t.Fatalf("SaveUnread() error = %v", err)
	}
	messages, _, counted, err := cache.CountUnread(ctx, mine, "!a:x")
	if err != nil || !counted || messages != 1 {
		t.Errorf("CountUnread() = %d, counted %v, %v; want 1, counted", messages, counted, err)
	}
	all, err := cache.CountUnreadAll(ctx, mine)
	if err != nil || !all["!a:x"].Counted || all["!a:x"].Messages != 1 {
		t.Errorf("CountUnreadAll() = %+v, %v; want !a:x counted at 1", all, err)
	}
}
