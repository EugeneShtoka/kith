package matrix

import (
	"context"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The live case (a Google Messages SMS bridge room), in the server's stream order:
// two SMS, then three bridge state events. The last SMS carries a later
// origin_server_ts (the phone's clock) than the state event the receipt points at.
const smsRoom = domain.RoomID("!sms:x")

var smsStream = []srvEvent{
	{ID: "$modS", TS: 1781004013475},
	{ID: "$yqjem", TS: 1781004151425},
	{ID: "$lSkc", Type: "m.room.name", TS: 1781004150040},
	{ID: "$RcJu", Type: "m.bridge", TS: 1781004150043},
	{ID: "$7cBV", Type: "uk.half-shot.bridge", TS: 1781004150047},
}

// smsBackend caches the room's two SMS (plus extra) and talks to srv.
func smsBackend(t *testing.T, srv *eventServer, extra ...domain.Message) *InProc {
	t.Helper()
	msgs := append([]domain.Message{
		{ID: "$modS", RoomID: smsRoom, Sender: "@sms:x", Body: "a", Timestamp: time.UnixMilli(1781004013475)},
		{ID: "$yqjem", RoomID: smsRoom, Sender: "@sms:x", Body: "b", Timestamp: time.UnixMilli(1781004151425)},
	}, extra...)
	return backendWith(t, srv.Server, map[domain.RoomID][]domain.Message{smsRoom: msgs})
}

// smsUnread is the room's cached unread state after a start.
func smsUnread(t *testing.T, b *InProc) domain.Unread {
	t.Helper()
	got, err := b.CachedUnread(context.Background())
	if err != nil {
		t.Fatalf("CachedUnread: %v", err)
	}
	for _, u := range got {
		if u.RoomID == smsRoom {
			return u
		}
	}
	t.Fatalf("no unread state for %s in %+v", smsRoom, got)
	return domain.Unread{}
}

// Regression: a receipt on a bridge's state event was placed by that event's own
// time, behind an SMS the server orders before it, so the room showed 1 unread
// forever. The position is the stream order's: the room is read through.
func TestAReceiptOnABridgeStateEventReadsByStreamOrder(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		// readTS is the stored position's time: 0 is a fresh receipt, the state
		// event's own time is one an earlier build placed.
		readTS int64
	}{
		"a fresh receipt":                 {readTS: 0},
		"a position placed by its own ts": {readTS: 1781004150047},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newEventServer(t, smsStream)
			b := smsBackend(t, srv)
			ctx := context.Background()
			if err := b.cache.SaveUnread(ctx, domain.Unread{RoomID: smsRoom, Notifications: 0, ReadEvent: "$7cBV"}, tt.readTS); err != nil {
				t.Fatalf("SaveUnread: %v", err)
			}
			b.seedUnread(ctx)
			b.fetches.wg.Wait()

			u := smsUnread(t, b)
			if !u.Counted || u.Messages != 0 {
				t.Errorf("unread = %+v, want counted with nothing unread", u)
			}
			if u.ReadEvent != "$yqjem" {
				t.Errorf("read event = %q, want the newest message, $yqjem", u.ReadEvent)
			}
			if n := srv.times("$7cBV"); n != 1 {
				t.Errorf("$7cBV asked %d times, want 1", n)
			}
		})
	}
}

// The same receipt arriving by sync: held (not a cached message), then placed by
// stream order a sync later.
func TestASyncedReceiptOnAStateEventReadsByStreamOrder(t *testing.T) {
	t.Parallel()

	b := smsBackend(t, newEventServer(t, smsStream))
	ctx := context.Background()
	resp := &mautrix.RespSync{}
	resp.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{
		id.RoomID(smsRoom): {Ephemeral: mautrix.SyncEventsList{Events: []*event.Event{
			receiptEvent(`{"$7cBV":{"m.read":{"@me:x":{"ts":1}}}}`),
		}}},
	}
	b.onSync(ctx, resp, "")
	b.onSync(ctx, &mautrix.RespSync{}, "")
	b.fetches.wg.Wait()
	if u := smsUnread(t, b); !u.Counted || u.Messages != 0 {
		t.Errorf("unread = %+v, want counted with nothing unread", u)
	}
}

// A message after the receipt in stream order stays unread, whatever its time.
func TestAMessageAfterAStateReceiptStaysUnread(t *testing.T) {
	t.Parallel()

	stream := append(append([]srvEvent(nil), smsStream...), srvEvent{ID: "$later", TS: 1781004150100})
	later := domain.Message{ID: "$later", RoomID: smsRoom, Sender: "@sms:x", Body: "c",
		Timestamp: time.UnixMilli(1781004160000)}
	b := smsBackend(t, newEventServer(t, stream), later)
	ctx := context.Background()
	if err := b.cache.SaveUnread(ctx, domain.Unread{RoomID: smsRoom, ReadEvent: "$7cBV"}, 0); err != nil {
		t.Fatalf("SaveUnread: %v", err)
	}
	b.seedUnread(ctx)
	b.fetches.wg.Wait()
	u := smsUnread(t, b)
	if !u.Counted || u.Messages != 1 || u.ReadEvent != "$yqjem" {
		t.Errorf("unread = %+v, want counted, 1 unread, read at $yqjem", u)
	}
}

// Regression: marking a room read without opening it receipted the newest message by
// time, which the server (holding a later position in its order) ignored without an
// echo, so nothing changed. The position now moves locally once the receipt lands.
func TestMarkRoomsReadMovesThePositionWithoutAnEcho(t *testing.T) {
	t.Parallel()

	srv := newReceiptServer(t, nil) // accepts and never echoes
	b := backendWith(t, srv.Server, map[domain.RoomID][]domain.Message{readRoom: {
		{ID: "$old", RoomID: readRoom, Sender: "@them:x", Body: "old", Timestamp: readAt(0)},
		{ID: "$new", RoomID: readRoom, Sender: "@them:x", Body: "new", Timestamp: readAt(10)},
	}})
	ctx := context.Background()
	if err := b.cache.SaveUnread(ctx, domain.Unread{RoomID: readRoom, ReadEvent: "$old"}, 0); err != nil {
		t.Fatalf("SaveUnread: %v", err)
	}
	b.seedUnread(ctx)
	if got := unreadOf(t, b); !got.Counted || got.Messages != 1 {
		t.Fatalf("before: unread = %+v, want 1 counted", got)
	}

	if _, err := b.MarkRoomsRead(ctx, []domain.RoomID{readRoom}, false); err != nil {
		t.Fatalf("MarkRoomsRead: %v", err)
	}
	if got := unreadOf(t, b); !got.Counted || got.Messages != 0 {
		t.Errorf("after: unread = %+v, want nothing unread", got)
	}
	wantRead(t, b, "$new")
}

// Opening a room (MarkRead) moves the position locally too, forward only.
func TestMarkReadMovesThePositionForwardOnly(t *testing.T) {
	t.Parallel()

	b := readBackend(t, newReceiptServer(t, nil).Server)
	ctx := context.Background()
	if err := b.cache.SaveUnread(ctx, domain.Unread{RoomID: readRoom, ReadEvent: "$old"}, 0); err != nil {
		t.Fatalf("SaveUnread: %v", err)
	}
	b.seedUnread(ctx)
	if err := b.MarkRead(ctx, readRoom, "$new", false); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	wantRead(t, b, "$new")
	if got := unreadOf(t, b); !got.Counted || got.Messages != 0 {
		t.Errorf("unread = %+v, want nothing unread", got)
	}
	if err := b.MarkRead(ctx, readRoom, "$old", false); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if got := unreadOf(t, b); !got.Counted || got.Messages != 0 {
		t.Errorf("after an older receipt: unread = %+v, want nothing unread", got)
	}
	// The read time as the cache keeps it, written with every change.
	placed, err := b.cache.ReadPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ts, want := placed[readRoom], readAt(10).UnixMilli(); ts != want {
		t.Errorf("read time = %d after an older receipt, want %d kept", ts, want)
	}
}

// The live room, placed by an earlier build and with /context failing: pressing m
// (or opening it) receipts the server's newest event (the bridge's state event,
// which it accepts) and reads the room through locally, without an echo.
func TestMarkingTheBridgeRoomReadReceiptsTheStreamsNewest(t *testing.T) {
	t.Parallel()

	marks := map[string]func(b *InProc) error{
		"m": func(b *InProc) error {
			_, err := b.MarkRoomsRead(context.Background(), []domain.RoomID{smsRoom}, false)
			return err
		},
		"open": func(b *InProc) error { return b.MarkRead(context.Background(), smsRoom, "$yqjem", false) },
	}
	for name, mark := range marks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newEventServer(t, smsStream, eventServerOpts{noContext: true})
			b := smsBackend(t, srv)
			ctx := context.Background()
			if err := b.cache.SaveUnread(ctx, domain.Unread{RoomID: smsRoom, ReadEvent: "$7cBV"}, 1781004150047); err != nil {
				t.Fatalf("SaveUnread: %v", err)
			}
			b.seedUnread(ctx)
			b.fetches.wg.Wait()
			if u := smsUnread(t, b); !u.Counted || u.Messages != 1 {
				t.Fatalf("before: unread = %+v, want the stuck 1", u)
			}

			if err := mark(b); err != nil {
				t.Fatalf("mark: %v", err)
			}
			if u := smsUnread(t, b); !u.Counted || u.Messages != 0 {
				t.Errorf("after: unread = %+v, want nothing unread", u)
			}
			if got := srv.posted(); len(got) != 1 || got[0] != "$7cBV" {
				t.Errorf("receipts = %v, want one at the stream's newest, $7cBV", got)
			}
		})
	}
}

// A main-timeline receipt does not go to a thread reply, even the stream's newest.
func TestMarkReadSkipsAThreadedNewestEvent(t *testing.T) {
	t.Parallel()

	srv := newEventServer(t, []srvEvent{
		{ID: "$old", TS: readAt(0).UnixMilli()},
		{ID: "$new", TS: readAt(10).UnixMilli()},
		{ID: "$reply", TS: readAt(11).UnixMilli(), Thread: "$old"},
	})
	b := readBackend(t, srv.Server)
	if err := b.MarkRead(context.Background(), readRoom, "$new", false); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if got := srv.posted(); len(got) != 1 || got[0] != "$new" {
		t.Errorf("receipts = %v, want one at $new", got)
	}
	wantRead(t, b, "$new")
}

// unreadOf is readRoom's in-memory unread state.
func unreadOf(t *testing.T, b *InProc) domain.Unread {
	t.Helper()
	b.recount(context.Background(), readRoom)
	return b.unread.get(readRoom)
}

// streamPosition's edges.
func TestStreamPosition(t *testing.T) {
	t.Parallel()

	msg := func(eventID string, ts int64) *event.Event {
		return &event.Event{ID: id.EventID(eventID), Type: event.EventMessage, Timestamp: ts}
	}
	state := func(eventID string, ts int64) *event.Event {
		return &event.Event{ID: id.EventID(eventID), Type: event.StateRoomName, Timestamp: ts}
	}
	tests := map[string]struct {
		receipt       *event.Event
		before, after []*event.Event
		newest        readPos
		want          readPos
		wantOK        bool
	}{
		"a message with one after it": {
			receipt: msg("$m", 50), after: []*event.Event{msg("$n", 40)},
			want: readPos{Event: "$m", TS: 50}, wantOK: true,
		},
		"a state event between messages": {
			receipt: state("$s", 10), before: []*event.Event{msg("$b", 90)}, after: []*event.Event{msg("$a", 95)},
			want: readPos{Event: "$b", TS: 90}, wantOK: true,
		},
		"the last event, read through the newest cached": {
			receipt: state("$s", 10), before: []*event.Event{msg("$b", 20)},
			newest: readPos{Event: "$c", TS: 30},
			want:   readPos{Event: "$c", TS: 30}, wantOK: true,
		},
		"the last event, later than every message": {
			receipt: state("$s", 99), before: []*event.Event{msg("$b", 20)},
			newest: readPos{Event: "$b", TS: 20},
			want:   readPos{Event: "$b", TS: 99}, wantOK: true,
		},
		"before every message": {
			receipt: state("$s", 70), after: []*event.Event{msg("$a", 60)},
			want: readPos{Event: "$s", TS: 59}, wantOK: true,
		},
		"an empty room": {
			receipt: state("$s", 70),
			want:    readPos{Event: "$s", TS: 70}, wantOK: true,
		},
		"a server that ignored the filter": {
			receipt: state("$s", 70), after: []*event.Event{state("$t", 71)},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := streamPosition(tt.receipt, tt.before, tt.after, tt.newest)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("streamPosition() = %+v, %v; want %+v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
