package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// srvEvent is one event of an eventServer's room, in stream order.
type srvEvent struct {
	ID   string
	Type string // "" is m.room.message
	TS   int64
	// Thread is a reply's thread root.
	Thread string
}

func (e srvEvent) json() map[string]any {
	typ := e.Type
	if typ == "" {
		typ = "m.room.message"
	}
	out := map[string]any{
		"event_id": e.ID, "type": typ, "sender": "@them:x", "origin_server_ts": e.TS,
		"content": map[string]any{"msgtype": "m.text", "body": "old"},
	}
	if typ != "m.room.message" {
		out["state_key"] = ""
		out["content"] = map[string]any{}
	}
	if e.Thread != "" {
		out["content"] = map[string]any{"msgtype": "m.text", "body": "reply",
			"m.relates_to": map[string]any{"rel_type": "m.thread", "event_id": e.Thread}}
	}
	return out
}

// eventServer serves one room's stream (oldest first): GET /event/{id} and
// /context/{id} (filtered by the filter's types, the limit split around the event,
// as Synapse does), 404ing unknown events and counting each ask; GET /messages
// (backwards: the stream's newest first); and receipts, recorded and never echoed.
type eventServer struct {
	*httptest.Server
	mu       sync.Mutex
	asked    map[string]int
	receipts []string
}

// eventServerOpts shape an eventServer.
type eventServerOpts struct {
	// noContext 404s /context, as a failing server would.
	noContext bool
}

func newEventServer(t *testing.T, stream []srvEvent, opts ...eventServerOpts) *eventServer {
	t.Helper()
	var opt eventServerOpts
	if len(opts) > 0 {
		opt = opts[0]
	}
	es := &eventServer{asked: map[string]int{}}
	es.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) >= 3 && parts[len(parts)-3] == "receipt" {
			if !receiptBodyOK(w, r) {
				return
			}
			es.mu.Lock()
			es.receipts = append(es.receipts, parts[len(parts)-1])
			es.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if parts[len(parts)-1] == "messages" {
			chunk := []map[string]any{}
			if len(stream) > 0 {
				chunk = append(chunk, stream[len(stream)-1].json())
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"start": "s", "end": "e", "chunk": chunk})
			return
		}
		kind := ""
		if len(parts) >= 3 {
			kind = parts[len(parts)-2]
		}
		if kind != "event" && (kind != "context" || opt.noContext) {
			http.Error(w, `{"errcode":"M_UNRECOGNIZED"}`, http.StatusNotFound)
			return
		}
		eventID := parts[len(parts)-1]
		es.mu.Lock()
		es.asked[eventID]++
		es.mu.Unlock()
		at := -1
		for i, e := range stream {
			if e.ID == eventID {
				at = i
			}
		}
		if at < 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"no"}`))
			return
		}
		if kind == "event" {
			_ = json.NewEncoder(w).Encode(stream[at].json())
			return
		}
		_ = json.NewEncoder(w).Encode(contextOf(r, stream, at))
	}))
	t.Cleanup(es.Close)
	return es
}

// posted is every receipt's event, in order.
func (es *eventServer) posted() []string {
	es.mu.Lock()
	defer es.mu.Unlock()
	return append([]string(nil), es.receipts...)
}

// contextOf answers /context for stream[at].
func contextOf(r *http.Request, stream []srvEvent, at int) map[string]any {
	var filter struct {
		Types []string `json:"types"`
	}
	_ = json.Unmarshal([]byte(r.URL.Query().Get("filter")), &filter)
	keep := func(e srvEvent) bool {
		if len(filter.Types) == 0 {
			return true
		}
		typ := e.Type
		if typ == "" {
			typ = "m.room.message"
		}
		return slices.Contains(filter.Types, typ)
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit == 0 {
		limit = 10
	}
	beforeN, afterN := limit/2, limit-limit/2
	before, after := []map[string]any{}, []map[string]any{}
	for i := at - 1; i >= 0 && len(before) < beforeN; i-- {
		if keep(stream[i]) {
			before = append(before, stream[i].json())
		}
	}
	for i := at + 1; i < len(stream) && len(after) < afterN; i++ {
		if keep(stream[i]) {
			after = append(after, stream[i].json())
		}
	}
	return map[string]any{
		"event": stream[at].json(), "events_before": before, "events_after": after,
		"start": "s", "end": "e", "state": []any{},
	}
}

func (es *eventServer) times(eventID string) int {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.asked[eventID]
}

const readRoom = domain.RoomID("!r:x")

var readBase = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)

func readAt(minutes int) time.Time { return readBase.Add(time.Duration(minutes) * time.Minute) }

// readBackend is a backend on srv whose cache holds $old (minute 0) and $new
// (minute 10) from someone else in readRoom, plus extra.
func readBackend(t *testing.T, srv *httptest.Server, extra ...domain.Message) *InProc {
	t.Helper()
	msgs := append([]domain.Message{
		{ID: "$old", RoomID: readRoom, Sender: "@them:x", Body: "old", Timestamp: readAt(0)},
		{ID: "$new", RoomID: readRoom, Sender: "@them:x", Body: "new", Timestamp: readAt(10)},
	}, extra...)
	return backendWith(t, srv, map[domain.RoomID][]domain.Message{readRoom: msgs})
}

// syncWith is a sync response for readRoom carrying the given ephemeral and
// account-data events.
func syncWith(ephemeral []*event.Event, accountData ...*event.Event) *mautrix.RespSync {
	resp := &mautrix.RespSync{}
	resp.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{
		id.RoomID(readRoom): {
			Ephemeral:   mautrix.SyncEventsList{Events: ephemeral},
			AccountData: mautrix.SyncEventsList{Events: accountData},
		},
	}
	return resp
}

func markEvent(unread bool) *event.Event {
	raw := `{"unread":false}`
	if unread {
		raw = `{"unread":true}`
	}
	return &event.Event{Type: event.AccountDataMarkedUnread, Content: event.Content{VeryRaw: []byte(raw)}}
}

// wantRead checks the room's read event, in memory and in the cache.
func wantRead(t *testing.T, b *InProc, want domain.EventID) {
	t.Helper()
	got := b.unread.get(readRoom).ReadEvent
	if got != want {
		t.Errorf("read event = %q, want %q", got, want)
	}
	cached, err := b.cache.Unread(context.Background())
	if err != nil {
		t.Fatalf("Unread: %v", err)
	}
	for _, u := range cached {
		if u.RoomID == readRoom && u.ReadEvent != want {
			t.Errorf("cached read event = %q, want %q", u.ReadEvent, want)
		}
	}
}

// Regression (root cause A): tuwunel keeps a receipt per kind and sync delivers them
// all; the unthreaded one used to win whatever it pointed at, so a full sync put the
// room back at an older receipt.
func TestTheNewestReceiptIsTheRoomsPosition(t *testing.T) {
	t.Parallel()

	tests := map[string][]*event.Event{
		"an older unthreaded receipt beside a newer main one": {
			receiptEvent(`{"$old":{"m.read":{"@me:x":{"ts":1}}}}`),
			receiptEvent(`{"$new":{"m.read":{"@me:x":{"ts":2,"thread_id":"main"}}}}`),
		},
		"a private unthreaded receipt, delivered last, on an older event": {
			receiptEvent(`{"$new":{"m.read":{"@me:x":{"ts":1,"thread_id":"main"}}}}`),
			receiptEvent(`{"$old":{"m.read.private":{"@me:x":{"ts":2}}}}`),
		},
	}
	for name, events := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := readBackend(t, newEventServer(t, nil).Server)
			b.onSync(context.Background(), syncWith(events), "")
			wantRead(t, b, "$new")
		})
	}
}

// Regression (root cause B): a receipt behind the room's position, in a later sync,
// does not move it back.
func TestAnOlderReceiptDoesNotMoveTheRoomBack(t *testing.T) {
	t.Parallel()

	b := readBackend(t, newEventServer(t, nil).Server)
	ctx := context.Background()
	b.onSync(ctx, syncWith([]*event.Event{receiptEvent(`{"$new":{"m.read":{"@me:x":{"ts":1,"thread_id":"main"}}}}`)}), "")
	wantRead(t, b, "$new")

	b.onSync(ctx, syncWith([]*event.Event{receiptEvent(`{"$old":{"m.read":{"@me:x":{"ts":2}}}}`)}), "")
	wantRead(t, b, "$new")

	// A restart reads the position back with its time, and still refuses the older one.
	restarted := New(b.cache)
	restarted.client = b.client
	restarted.seedUnread(ctx)
	restarted.onSync(ctx, syncWith([]*event.Event{receiptEvent(`{"$old":{"m.read":{"@me:x":{"ts":3}}}}`)}), "")
	wantRead(t, restarted, "$new")
}

// Marking a room unread is its own flag and survives the forward-only position.
func TestMarkingUnreadStillWorks(t *testing.T) {
	t.Parallel()

	b := readBackend(t, newEventServer(t, nil).Server)
	ctx := context.Background()
	b.onSync(ctx, syncWith([]*event.Event{receiptEvent(`{"$new":{"m.read":{"@me:x":{"ts":1,"thread_id":"main"}}}}`)}), "")

	marked := func() bool {
		return b.unread.get(readRoom).Marked
	}
	cachedMarked := func() bool {
		cached, err := b.cache.Unread(ctx)
		if err != nil || len(cached) != 1 {
			t.Fatalf("Unread = %v, %v", cached, err)
		}
		return cached[0].Marked
	}

	b.onSync(ctx, syncWith(nil, markEvent(true)), "")
	if !marked() || !cachedMarked() {
		t.Fatalf("marked = %v (cached %v) after the mark arrived, want true", marked(), cachedMarked())
	}
	wantRead(t, b, "$new")

	// An older receipt beside it changes neither.
	b.onSync(ctx, syncWith([]*event.Event{receiptEvent(`{"$old":{"m.read":{"@me:x":{"ts":2}}}}`)}), "")
	if !marked() {
		t.Error("an older receipt cleared the mark")
	}
	wantRead(t, b, "$new")

	b.onSync(ctx, syncWith(nil, markEvent(false)), "")
	if marked() || cachedMarked() {
		t.Errorf("marked = %v (cached %v) after it was cleared, want false", marked(), cachedMarked())
	}
}

// A receipt whose event was not cached yet lands once the event is.
func TestAHeldReceiptLandsWhenItsEventArrives(t *testing.T) {
	t.Parallel()

	b := readBackend(t, newEventServer(t, nil).Server)
	ctx := context.Background()
	b.onSync(ctx, syncWith([]*event.Event{receiptEvent(`{"$late":{"m.read":{"@me:x":{"ts":1,"thread_id":"main"}}}}`)}), "")
	wantRead(t, b, "")

	late := domain.Message{ID: "$late", RoomID: readRoom, Sender: "@them:x", Body: "late", Timestamp: readAt(20)}
	b.cacheMessages(ctx, readRoom, []domain.Message{late})
	b.settleReceipts(ctx, readRoom)
	wantRead(t, b, "$late")
}

// Root cause C: a read event older than the cache is fetched once for its time, so
// the room is counted locally and gets a thread floor.
func TestAnUncachedReadEventIsFetchedForItsTime(t *testing.T) {
	t.Parallel()

	srv := newEventServer(t, []srvEvent{
		{ID: "$old", TS: readAt(0).UnixMilli()},
		{ID: "$reply", TS: readAt(3).UnixMilli()},
		{ID: "$gone", TS: readAt(5).UnixMilli()},
		{ID: "$new", TS: readAt(10).UnixMilli()},
	})
	// A thread reply before the read position: read, once the floor is seeded.
	reply := domain.Message{ID: "$reply", RoomID: readRoom, Sender: "@them:x", Body: "in a thread",
		ThreadRoot: "$old", Timestamp: readAt(3)}
	b := readBackend(t, srv.Server, reply)
	ctx := context.Background()
	if err := b.cache.SaveUnread(ctx, domain.Unread{RoomID: readRoom, Notifications: 7, ReadEvent: "$gone"}, 0); err != nil {
		t.Fatalf("SaveUnread: %v", err)
	}

	b.seedUnread(ctx)
	b.fetches.wg.Wait()

	got, err := b.CachedUnread(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("CachedUnread = %v, %v", got, err)
	}
	if !got[0].Counted {
		t.Fatal("counted = false: the fetched time did not make the room countable")
	}
	// $new only: $old and the reply are before the read position.
	if got[0].Messages != 1 || len(got[0].Threads) != 0 {
		t.Errorf("messages = %d, threads = %v; want 1 and none", got[0].Messages, got[0].Threads)
	}

	// Asked once per run.
	b.seedUnread(ctx)
	b.fetches.wg.Wait()
	if n := srv.times("$gone"); n != 1 {
		t.Errorf("$gone fetched %d times in one run, want 1", n)
	}
	// A restart asks again only while the room counts unread on an uncached event
	// (an earlier build may have placed it by a bridge's clock): not once it is read.
	restart := func() {
		again := New(b.cache)
		again.client = b.client
		again.seedUnread(ctx)
		again.fetches.wg.Wait()
	}
	restart()
	if n := srv.times("$gone"); n != 2 {
		t.Errorf("$gone fetched %d times after a restart with the room unread, want 2", n)
	}
	b.advanceRead(ctx, readRoom, readPos{Event: "$new", TS: readAt(10).UnixMilli()})
	restart()
	if n := srv.times("$gone"); n != 2 {
		t.Errorf("$gone fetched %d times after a restart with the room read, want still 2", n)
	}
}

// A fetch that fails changes nothing: the room keeps the server's count.
func TestAFailedReadFetchKeepsTheServerCount(t *testing.T) {
	t.Parallel()

	srv := newEventServer(t, nil)
	b := readBackend(t, srv.Server)
	ctx := context.Background()
	if err := b.cache.SaveUnread(ctx, domain.Unread{RoomID: readRoom, Notifications: 7, ReadEvent: "$gone"}, 0); err != nil {
		t.Fatalf("SaveUnread: %v", err)
	}
	b.seedUnread(ctx)
	b.fetches.wg.Wait()
	got, err := b.CachedUnread(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("CachedUnread = %v, %v", got, err)
	}
	if got[0].Counted || got[0].Notifications != 7 || got[0].ReadEvent != "$gone" {
		t.Errorf("unread = %+v, want uncounted with the server's 7 and $gone kept", got[0])
	}
	if n := srv.times("$gone"); n != 1 {
		t.Errorf("$gone fetched %d times, want 1", n)
	}
}

// A receipt still held a sync later is fetched, and then moves the room.
func TestAReceiptStillHeldASyncLaterIsFetched(t *testing.T) {
	t.Parallel()

	srv := newEventServer(t, []srvEvent{
		{ID: "$old", TS: readAt(0).UnixMilli()},
		{ID: "$new", TS: readAt(10).UnixMilli()},
		{ID: "$far", TS: readAt(30).UnixMilli()},
	})
	b := readBackend(t, srv.Server)
	ctx := context.Background()

	b.onSync(ctx, syncWith([]*event.Event{receiptEvent(`{"$far":{"m.read":{"@me:x":{"ts":1}}}}`)}), "")
	b.fetches.wg.Wait()
	if n := srv.times("$far"); n != 0 {
		t.Fatalf("$far fetched %d times in the sync that carried it; its event may still be on the way", n)
	}
	wantRead(t, b, "")

	b.onSync(ctx, &mautrix.RespSync{}, "")
	b.fetches.wg.Wait()
	wantRead(t, b, "$far")
	got, err := b.CachedUnread(ctx)
	if err != nil || len(got) != 1 || !got[0].Counted || got[0].Messages != 0 {
		t.Errorf("CachedUnread = %+v, %v; want counted with nothing unread", got, err)
	}
}

// receiptBodyOK answers like tuwunel does for a receipt body that is not a JSON object
// (a typed nil marshals as `null`): 400 M_BAD_JSON. It reports whether the body was fine.
func receiptBodyOK(w http.ResponseWriter, r *http.Request) bool {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body == nil {
		http.Error(w, `{"errcode":"M_BAD_JSON","error":"expected an object"}`, http.StatusBadRequest)
		return false
	}
	return true
}
