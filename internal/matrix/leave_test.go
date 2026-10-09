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

// A space left is gone at once, and a space list the homeserver gave before the leave,
// from a refresh already under way, does not bring it back.
func TestALeftSpaceStaysLeft(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	asked, release := make(chan struct{}, 1), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/joined_rooms"):
			asked <- struct{}{}
			<-release // answered as it stood before the leave
			_, _ = w.Write([]byte(`{"joined_rooms": ["!s:x", "!t:x", "!a:x"]}`))
		case strings.HasSuffix(r.URL.Path, "/leave"):
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, "/state/m.room.create"):
			// The homeserver says which rooms are spaces, as a real one would: the
			// cache, which forgot the space at the leave, is not what drops it.
			if strings.Contains(r.URL.Path, "!a:x") {
				_, _ = w.Write([]byte(`{}`))
			} else {
				_, _ = w.Write([]byte(`{"type": "m.space"}`))
			}
		case strings.HasSuffix(r.URL.Path, "/state"):
			_, _ = w.Write([]byte(`[]`)) // a space read afresh, not the cache's
		default:
			http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	b := backendWith(t, srv, nil)
	if err := b.cache.SaveSpaces(ctx, domain.MatrixRooms, []domain.Space{
		{ID: "!s:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
		{ID: "!t:x", Name: "Fun", Children: []domain.RoomID{"!a:x"}},
	}); err != nil {
		t.Fatal(err)
	}
	ids := func(spaces []domain.Space) []domain.SpaceID {
		out := make([]domain.SpaceID, len(spaces))
		for i := range spaces {
			out[i] = spaces[i].ID
		}
		return out
	}

	refreshed := make(chan []domain.Space, 1)
	go func() {
		spaces, err := b.RefreshSpaces(ctx)
		if err != nil {
			t.Error(err)
		}
		refreshed <- spaces
	}()
	<-asked
	if err := b.LeaveRoom(ctx, "!s:x"); err != nil {
		t.Fatal(err)
	}
	if cached, _ := b.Spaces(ctx); slices.Contains(ids(cached), "!s:x") {
		t.Errorf("right after the leave the cache lists %v", ids(cached))
	}
	close(release)
	if got := ids(<-refreshed); slices.Contains(got, "!s:x") || !slices.Contains(got, "!t:x") {
		t.Errorf("a refresh asked for before the leave gave %v, want !t:x and not !s:x", got)
	}
	if cached, _ := b.Spaces(ctx); slices.Contains(ids(cached), "!s:x") {
		t.Errorf("after the refresh the cache lists %v", ids(cached))
	}
}
