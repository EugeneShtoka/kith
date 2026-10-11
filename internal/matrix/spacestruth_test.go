package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// listingServer is a homeserver where !chat:x is a room and !work:x a space holding it.
// It counts what it is asked, and holds /joined_rooms until gate closes, when set.
type listingServer struct {
	mu      sync.Mutex
	joined  int
	creates map[string]int
	gate    chan struct{}
	asked   chan struct{} // gets a value when /joined_rooms is asked
	fail    bool
}

func (h *listingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/joined_rooms"):
		h.mu.Lock()
		h.joined++
		h.mu.Unlock()
		if h.asked != nil {
			h.asked <- struct{}{}
		}
		if h.gate != nil {
			<-h.gate
		}
		if h.fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN"}`))
			return
		}
		_, _ = w.Write([]byte(`{"joined_rooms": ["!chat:x", "!work:x"]}`))
	case strings.Contains(path, "/state/m.room.create"):
		room := path[strings.Index(path, "/rooms/")+len("/rooms/"):]
		room = room[:strings.Index(room, "/")]
		h.mu.Lock()
		h.creates[room]++
		h.mu.Unlock()
		if room == "!work:x" {
			_, _ = w.Write([]byte(`{"type": "m.space"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	case strings.HasSuffix(path, "/rooms/!work:x/state"):
		_, _ = w.Write([]byte(`[{"type":"m.space.child","state_key":"!chat:x","sender":"@me:x","content":{"via":["x"]},"event_id":"$c","room_id":"!work:x"}]`))
	case strings.HasSuffix(path, "/joined_members"):
		_, _ = w.Write([]byte(`{"joined": {"@bob:x": {"display_name": "Bob"}}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND"}`))
	}
}

func serving(t *testing.T, h *listingServer, cache *db.Cache) *InProc {
	t.Helper()
	h.creates = map[string]int{}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatal(err)
	}
	b := New(cache)
	b.client = client
	return b
}

func onlyChat(t *testing.T, what string, rooms []domain.Room) {
	t.Helper()
	if len(rooms) != 1 || rooms[0].ID != "!chat:x" {
		t.Errorf("%s = %+v, want the chat alone", what, rooms)
	}
}

// A room list never holds a space, whether spaces were refreshed before, the cache
// is cold, or there is no cache.
func TestARoomListNeverHoldsASpace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cache := testCache(t)
	rooms, err := serving(t, &listingServer{}, cache).RefreshRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	onlyChat(t, "on a cold cache, RefreshRooms", rooms)
	cached, _ := cache.Rooms(ctx)
	onlyChat(t, "on a cold cache, the cached rooms", cached)

	rooms, err = serving(t, &listingServer{}, nil).RefreshRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	onlyChat(t, "without a cache, RefreshRooms", rooms)
}

// A rooms refresh and a spaces refresh asked together share one listing.
func TestRoomsAndSpacesShareOneListing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := &listingServer{gate: make(chan struct{}), asked: make(chan struct{}, 4)}
	b := serving(t, h, testCache(t))

	var wg sync.WaitGroup
	var rooms []domain.Room
	var spaces []domain.Space
	var roomsErr, spacesErr error
	wg.Add(1)
	go func() { defer wg.Done(); spaces, spacesErr = b.RefreshSpaces(ctx) }()
	<-h.asked // the spaces refresh is listing
	wg.Add(1)
	go func() { defer wg.Done(); rooms, roomsErr = b.RefreshRooms(ctx) }()
	close(h.gate)
	wg.Wait()
	if roomsErr != nil || spacesErr != nil {
		t.Fatalf("errors: %v, %v", roomsErr, spacesErr)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.joined != 1 {
		t.Errorf("/joined_rooms asked %d times, want once", h.joined)
	}
	onlyChat(t, "rooms", rooms)
	if len(spaces) != 1 || spaces[0].ID != "!work:x" {
		t.Errorf("spaces = %+v, want Work", spaces)
	}
}

// A room's type is read once: it never changes.
func TestARoomsTypeIsReadOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := &listingServer{}
	b := serving(t, h, testCache(t))
	for range 3 {
		if _, err := b.RefreshRooms(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := b.RefreshSpaces(ctx); err != nil {
			t.Fatal(err)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.creates["!chat:x"] != 1 || h.creates["!work:x"] != 1 {
		t.Errorf("m.room.create read %v, want once each", h.creates)
	}
}

// A listing that fails leaves the cached rooms as they were.
func TestAFailedListingKeepsTheRooms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := testCache(t)
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!chat:x", Name: "Chat"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := serving(t, &listingServer{fail: true}, cache).RefreshRooms(ctx); err == nil {
		t.Error("RefreshRooms succeeded on a failed listing")
	}
	cached, _ := cache.Rooms(ctx)
	onlyChat(t, "after a failed listing, the cached rooms", cached)
}
