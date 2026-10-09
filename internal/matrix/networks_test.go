package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Each joined room not read yet has its network read from its bridge state in one
// filtered sync and kept: by its bridge's bot, by the protocol it names, Matrix for a
// room with none, by the older half-shot event as well; a room known already keeps its network whatever the reading says; a
// room the reading does not carry stays unread; a failed reading is asked for again;
// and a reading with nothing to read asks nothing.
func TestTheRoomsNetworksAreReadOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var syncs atomic.Int32
	var failing atomic.Bool
	bridge := func(content string) string {
		return `{"type": "m.bridge", "state_key": "k", "sender": "@bot:x", "event_id": "$e", "content": ` + content + `}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sync") {
			http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
			return
		}
		syncs.Add(1)
		if failing.Load() {
			http.Error(w, `{"errcode":"M_UNKNOWN"}`, http.StatusInternalServerError)
			return
		}
		if !strings.Contains(r.URL.Query().Get("filter"), "m.bridge") || r.URL.Query().Get("full_state") != "true" {
			t.Errorf("the reading's sync asked %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"next_batch": "s1", "rooms": {"join": {
			"!wa:x": {"state": {"events": [` + bridge(`{"bridgebot": "@whatsappbot_bg:x", "protocol": {"id": "whatsapp"}}`) + `]}},
			"!slack:x": {"state": {"events": [` + bridge(`{"protocol": {"id": "slackgo"}}`) + `]}},
			"!plain:x": {"state": {"events": []}},
			"!older:x": {"state": {"events": [{"type": "uk.half-shot.bridge", "state_key": "k", "sender": "@bot:x", "event_id": "$h", "content": {"protocol": {"id": "telegram"}}}]}},
			"!known:x": {"state": {"events": [` + bridge(`{"protocol": {"id": "telegram"}}`) + `]}}
		}}}`))
	}))
	t.Cleanup(srv.Close)
	b := backendWith(t, srv, nil)
	stale := 0
	b.onRoomsStale = func() { stale++ }
	if err := b.cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!wa:x"}, {ID: "!slack:x"}, {ID: "!plain:x"}, {ID: "!older:x"}, {ID: "!known:x"}, {ID: "!missing:x"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.cache.KeepRoomNetworks(ctx, map[domain.RoomID]domain.Protocol{"!known:x": domain.ProtocolSignal}); err != nil {
		t.Fatal(err)
	}

	failing.Store(true)
	b.readRoomNetworks(ctx)
	if !b.networks.wanted.Load() {
		t.Error("a failed reading is not asked for again")
	}
	failing.Store(false)
	b.readRoomNetworks(ctx)
	rooms, err := b.cache.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[domain.RoomID]domain.Protocol{}
	for _, r := range rooms {
		got[r.ID] = r.Network
	}
	want := map[domain.RoomID]domain.Protocol{
		"!wa:x": domain.ProtocolWhatsApp, "!slack:x": domain.ProtocolSlack, "!plain:x": domain.ProtocolMatrix,
		"!older:x": domain.ProtocolTelegram,
		"!known:x": domain.ProtocolSignal, "!missing:x": "",
	}
	for room, w := range want {
		if got[room] != w {
			t.Errorf("%s's network = %q, want %q", room, got[room], w)
		}
	}
	if stale == 0 {
		t.Error("the rooms were not said to have changed")
	}

	// Only !missing:x is unread now, and the reading still does not carry it.
	before := syncs.Load()
	if err := b.cache.KeepRoomNetworks(ctx, map[domain.RoomID]domain.Protocol{"!missing:x": domain.ProtocolMatrix}); err != nil {
		t.Fatal(err)
	}
	b.readRoomNetworks(ctx)
	if syncs.Load() != before {
		t.Error("a reading with every room read asked the homeserver")
	}
}

// A reading runs when asked for, one at a time: asked again while one runs, it does
// not start a second; a room refresh asks for one, for the rooms it brought, and
// answers with what was read.
func TestAReadingIsAskedForAndRunsAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var syncs atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sync"):
			syncs.Add(1)
			<-release
			_, _ = w.Write([]byte(`{"next_batch": "s1", "rooms": {"join": {"!a:x": {"state": {"events": []}}}}}`))
		case strings.HasSuffix(r.URL.Path, "/joined_rooms"):
			_, _ = w.Write([]byte(`{"joined_rooms": ["!a:x"]}`))
		default:
			http.Error(w, `{"errcode":"M_NOT_FOUND"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	b := backendWith(t, srv, nil)
	if _, err := b.RefreshRooms(ctx); err != nil {
		t.Fatal(err)
	}
	if !b.networks.wanted.Load() {
		t.Fatal("a room refresh did not ask for a reading")
	}
	b.readNetworksIfWanted(ctx)
	b.wantNetworks()
	b.readNetworksIfWanted(ctx) // asked again while the first runs
	close(release)
	b.fetches.wg.Wait()
	if n := syncs.Load(); n != 1 {
		t.Errorf("%d readings ran, want one at a time", n)
	}
	if !b.networks.wanted.Load() {
		t.Error("the asking that came while a reading ran was lost")
	}
	// Read, a room's network is in what the next refresh answers.
	rooms, err := b.RefreshRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].Network != domain.ProtocolMatrix {
		t.Errorf("the refresh after the reading answered %+v, want !a:x on Matrix", rooms)
	}
}
