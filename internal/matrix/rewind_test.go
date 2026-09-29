package matrix

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// sinceLog records the since token of every /sync, answering each with a new one.
type sinceLog struct {
	mu     sync.Mutex
	sinces []string
}

func (l *sinceLog) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.sinces...)
}

func (l *sinceLog) waitFor(t *testing.T, what string, ok func([]string) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok(l.seen()) {
		if time.Now().After(deadline) {
			seen := l.seen()
			t.Fatalf("timed out waiting for %s after %d syncs; the last: %q",
				what, len(seen), seen[max(len(seen)-5, 0):])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func syncingServer(t *testing.T, log *sinceLog) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		switch path := r.URL.Path; {
		case strings.HasSuffix(path, "/filter"):
			_, _ = rw.Write([]byte(`{"filter_id": "f"}`))
		case strings.HasSuffix(path, "/sync"):
			log.mu.Lock()
			log.sinces = append(log.sinces, r.URL.Query().Get("since"))
			n := len(log.sinces)
			log.mu.Unlock()
			time.Sleep(2 * time.Millisecond) // a long poll, briefly
			_, _ = fmt.Fprintf(rw, `{"next_batch": "s%d"}`, n)
		default:
			http.Error(rw, "unexpected path: "+path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --clear-cache reaches a daemon that is already syncing. The loop keeps its token in
// memory and saves it after each response, so the rewind has to restart the loop: the
// next /sync must go out with no since token.
func TestClearCacheRewindsARunningSync(t *testing.T) {
	t.Parallel()

	var log sinceLog
	b := backendOn(t, syncingServer(t, &log))
	b.client.Store = mautrix.NewMemorySyncStore()
	b.client.Syncer = &failingSyncer{DefaultSyncer: mautrix.NewDefaultSyncer(), b: b}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.syncUntilDone(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	log.waitFor(t, "an incremental sync", func(s []string) bool { return len(s) >= 3 })
	if err := b.ClearCache(ctx); err != nil {
		t.Fatalf("ClearCache() error = %v", err)
	}
	before := len(log.seen())
	log.waitFor(t, "a full sync after the rewind", func(s []string) bool {
		return slices.Contains(s[before:], "")
	})
}

// Before the loop starts, the rewind is immediate, and the loop starts from it.
func TestClearCacheBeforeSyncRewindsAtOnce(t *testing.T) {
	t.Parallel()

	b := backendOn(t, syncingServer(t, &sinceLog{}))
	ctx := context.Background()
	store := mautrix.NewMemorySyncStore()
	if err := store.SaveNextBatch(ctx, "@me:x", "s99"); err != nil {
		t.Fatal(err)
	}
	b.client.Store = store
	if err := b.ClearCache(ctx); err != nil {
		t.Fatalf("ClearCache() error = %v", err)
	}
	if token, _ := store.LoadNextBatch(ctx, "@me:x"); token != "" {
		t.Errorf("token = %q after ClearCache, want empty", token)
	}
}

// The response a rewind is taken on still reaches the sync listeners: the crypto
// machine's among them. A full initial sync carries no device_lists.changed, so a
// device list change dropped here would never be fetched, and messages would keep
// being encrypted to a contact's old devices.
func TestTheRewindingResponseStillReachesTheListeners(t *testing.T) {
	t.Parallel()

	b := New(nil)
	syncer := &failingSyncer{DefaultSyncer: mautrix.NewDefaultSyncer(), b: b}
	var changed []id.UserID
	syncer.OnSync(func(_ context.Context, res *mautrix.RespSync, _ string) bool {
		changed = append(changed, res.DeviceLists.Changed...)
		return true
	})
	b.rewind.running(true)
	if err := b.rewind.request(func() error { t.Fatal("rewound at once while syncing"); return nil }); err != nil {
		t.Fatal(err)
	}

	var roomEvents int
	syncer.OnEventType(event.EventMessage, func(context.Context, *event.Event) { roomEvents++ })

	res := &mautrix.RespSync{NextBatch: "s2"}
	res.DeviceLists.Changed = []id.UserID{"@bob:x"}
	room := &mautrix.SyncJoinedRoom{}
	room.Timeline.Events = []*event.Event{{Type: event.EventMessage, ID: "$e", Sender: "@bob:x",
		Content: event.Content{VeryRaw: []byte(`{"msgtype":"m.text","body":"hi"}`)}}}
	res.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{"!r:x": room}
	err := syncer.ProcessResponse(context.Background(), res, "s1")
	if !errors.Is(err, errRewind) {
		t.Errorf("ProcessResponse = %v, want the rewind", err)
	}
	if !slices.Equal(changed, []id.UserID{"@bob:x"}) {
		t.Errorf("listeners saw device list changes %v, want @bob:x", changed)
	}
	if roomEvents != 0 {
		t.Errorf("%d room events were handled; the full sync that follows brings them, and the emptied cache stays empty", roomEvents)
	}
	// Taken once: the next response is ordinary.
	if err := syncer.ProcessResponse(context.Background(), &mautrix.RespSync{NextBatch: "s3"}, "s2"); err != nil {
		t.Errorf("the next response = %v, want it processed", err)
	}
}

// A backend syncs once: a second Start is refused, and registers nothing.
func TestASecondStartIsRefused(t *testing.T) {
	t.Parallel()
	b := backendWithCache(t, "@me:x")
	inner := mautrix.NewDefaultSyncer()
	var seen int
	inner.OnEventType(event.EventMessage, func(context.Context, *event.Event) { seen++ })
	b.client.Syncer = &failingSyncer{DefaultSyncer: inner, b: b} // what the first Start left
	if err := b.Start(context.Background()); !errors.Is(err, errStartedTwice) {
		t.Fatalf("second Start = %v, want errStartedTwice", err)
	}
	evt := &event.Event{Type: event.EventMessage, ID: "$e", RoomID: "!r:x",
		Content: event.Content{VeryRaw: []byte(`{"msgtype":"m.text","body":"hi"}`)}}
	room := &mautrix.SyncJoinedRoom{}
	room.Timeline.Events = []*event.Event{evt}
	res := &mautrix.RespSync{}
	res.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{"!r:x": room}
	_ = inner.ProcessResponse(context.Background(), res, "s1")
	if seen != 1 {
		t.Errorf("a message was handled %d times, want once: the refused Start added handlers", seen)
	}
}

// Clearing with no cache open says so: success would read as done.
func TestClearingWithNoCacheIsAnError(t *testing.T) {
	t.Parallel()
	if err := New(nil).ClearCache(context.Background()); !errors.Is(err, errNoCache) {
		t.Fatalf("ClearCache() with no cache = %v, want errNoCache", err)
	}
}
