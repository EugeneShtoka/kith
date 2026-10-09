package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A room left is gone from the cache at once, before any sync says so, and the room
// list is refreshed; a room list the homeserver gave before the leave, from a refresh
// already under way, does not bring it back.
func TestALeftRoomStaysLeft(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	asked, release := make(chan struct{}, 1), make(chan struct{})
	var stalled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/joined_rooms"):
			if stalled.Load() {
				asked <- struct{}{}
				<-release // answered as it stood before the leave
			}
			_, _ = w.Write([]byte(`{"joined_rooms": ["!a:x", "!b:x"]}`))
		case strings.HasSuffix(r.URL.Path, "/leave"):
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	b := backendWith(t, srv, nil)
	stale := 0
	b.onRoomsStale = func() { stale++ }
	cached := func() []domain.RoomID {
		t.Helper()
		rooms, err := b.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]domain.RoomID, len(rooms))
		for i := range rooms {
			ids[i] = rooms[i].ID
		}
		return ids
	}
	if _, err := b.RefreshRooms(ctx); err != nil {
		t.Fatal(err)
	}

	stalled.Store(true)
	refreshed := make(chan []domain.Room, 1)
	go func() {
		rooms, err := b.RefreshRooms(ctx)
		if err != nil {
			t.Error(err)
		}
		refreshed <- rooms
	}()
	<-asked
	if err := b.LeaveRoom(ctx, "!a:x"); err != nil {
		t.Fatal(err)
	}
	if got := cached(); slices.Contains(got, "!a:x") || stale == 0 {
		t.Errorf("right after the leave the cache lists %v (rooms refreshed %d times)", got, stale)
	}
	close(release)
	rooms := <-refreshed
	for i := range rooms {
		if rooms[i].ID == "!a:x" {
			t.Error("a refresh asked for before the leave brought the room back")
		}
	}
	if got := cached(); slices.Contains(got, "!a:x") || !slices.Contains(got, "!b:x") {
		t.Errorf("after the refresh the cache lists %v, want !b:x and not !a:x", got)
	}
}
