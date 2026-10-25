package matrix

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A left room leaves the cache at once, and a refresh begun before the leave does not
// bring it back.
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
	if err := b.LeaveRoom(ctx, mx+"!a:x"); err != nil {
		t.Fatal(err)
	}
	if got := cached(); slices.Contains(got, mx+"!a:x") || stale == 0 {
		t.Errorf("right after the leave the cache lists %v (rooms refreshed %d times)", got, stale)
	}
	close(release)
	rooms := <-refreshed
	for i := range rooms {
		if rooms[i].ID == mx+"!a:x" {
			t.Error("a refresh asked for before the leave brought the room back")
		}
	}
	if got := cached(); slices.Contains(got, mx+"!a:x") || !slices.Contains(got, mx+"!b:x") {
		t.Errorf("after the refresh the cache lists %v, want !b:x and not !a:x", got)
	}
}

// A left space leaves the cache at once, and a refresh begun before the leave does not
// bring it back.
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
			// The server, not the cache, says which rooms are spaces.
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
	if err := b.cache.SaveSpaces(ctx, testIDs.owner(), []domain.Space{
		{ID: mx + "!s:x", Name: "Work", Children: []domain.RoomID{mx + "!a:x"}},
		{ID: mx + "!t:x", Name: "Fun", Children: []domain.RoomID{mx + "!a:x"}},
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
	if err := b.LeaveRoom(ctx, mx+"!s:x"); err != nil {
		t.Fatal(err)
	}
	if cached, _ := b.Spaces(ctx); slices.Contains(ids(cached), mx+"!s:x") {
		t.Errorf("right after the leave the cache lists %v", ids(cached))
	}
	close(release)
	if got := ids(<-refreshed); slices.Contains(got, mx+"!s:x") || !slices.Contains(got, mx+"!t:x") {
		t.Errorf("a refresh asked for before the leave gave %v, want !t:x and not !s:x", got)
	}
	if cached, _ := b.Spaces(ctx); slices.Contains(ids(cached), mx+"!s:x") {
		t.Errorf("after the refresh the cache lists %v", ids(cached))
	}
}

// A space's leaving mode comes from its state and is cached: a bridge's per-account
// space is not left, a Telegram forum is left whole, anything else alone; unreadable
// state keeps the cached mode.
func TestHowAMatrixSpaceIsLeftComesFromItsState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bridgeEvent := func(kind, content string) string {
		return `{"type": "` + kind + `", "state_key": "x", "sender": "@telegrambot:x", "event_id": "$e", "room_id": "!r:x", "content": ` + content + `}`
	}
	states := map[string]string{
		"!account:x":   bridgeEvent("m.bridge", `{"bridgebot": "@telegrambot:x", "channel": {"id": "__personal_filtering_space__"}}`),
		"!older:x":     bridgeEvent("uk.half-shot.bridge", `{"channel": {"id": "c"}, "com.beeper.room_type.v2": "personal_filtering_space"}`),
		"!forum:x":     bridgeEvent("m.bridge", `{"bridgebot": "@telegrambot:x", "protocol": {"id": "telegram"}, "channel": {"id": "channel:1804057518:-1"}}`),
		"!commun:x":    bridgeEvent("m.bridge", `{"bridgebot": "@telegrambot:x", "protocol": {"id": "telegram"}, "channel": {"id": "channel:1804057519"}}`),
		"!lookalike:x": bridgeEvent("m.bridge", `{"bridgebot": "@slackbot:x", "protocol": {"id": "slackgo"}, "channel": {"id": "channel:5:-1"}}`),
		"!mine:x":      ``,
		"!garbled:x":   bridgeEvent("m.bridge", `{"channel": "not an object"}`),
		"!unread:x":    "unreadable",
		"!unreadA:x":   "unreadable",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/joined_rooms"):
			_, _ = w.Write([]byte(`{"joined_rooms": ["!account:x", "!older:x", "!forum:x", "!commun:x", "!lookalike:x", "!mine:x", "!garbled:x", "!unread:x", "!unreadA:x"]}`))
		case strings.Contains(path, "/state/m.room.create"):
			_, _ = w.Write([]byte(`{"type": "m.space"}`))
		case strings.HasSuffix(path, "/state"):
			for room, state := range states {
				if strings.Contains(path, "/rooms/"+room+"/") {
					if state == "unreadable" {
						http.Error(w, `{"errcode":"M_FORBIDDEN"}`, http.StatusForbidden)
						return
					}
					_, _ = w.Write([]byte("[" + state + "]"))
					return
				}
			}
			http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
		default:
			http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	b := backendWith(t, srv, nil)
	// What the cache knew of the two it cannot read now.
	if err := b.cache.SaveSpaces(ctx, testIDs.owner(), []domain.Space{
		{ID: mx + "!unread:x", Name: "Kept", Leaving: domain.LeftAlone},
		{ID: mx + "!unreadA:x", Name: "Kept account"},
	}); err != nil {
		t.Fatal(err)
	}
	want := map[domain.SpaceID]domain.SpaceLeaving{
		mx + "!account:x": domain.NotLeft, mx + "!older:x": domain.NotLeft, mx + "!forum:x": domain.LeftWhole,
		mx + "!commun:x": domain.LeftAlone, mx + "!lookalike:x": domain.LeftAlone,
		mx + "!mine:x": domain.LeftAlone, mx + "!garbled:x": domain.LeftAlone,
		mx + "!unread:x": domain.LeftAlone, mx + "!unreadA:x": domain.NotLeft,
	}
	check := func(how string, spaces []domain.Space) {
		t.Helper()
		got := map[domain.SpaceID]domain.SpaceLeaving{}
		for _, s := range spaces {
			got[s.ID] = s.Leaving
		}
		if !maps.Equal(got, want) {
			t.Errorf("%s: leaving = %v, want %v", how, got, want)
		}
	}
	refreshed, err := b.RefreshSpaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check("refreshed", refreshed)
	cached, err := b.Spaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check("read back from the cache", cached)
}
