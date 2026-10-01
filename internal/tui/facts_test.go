package tui

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Codes and tracked words read a room as the daemon does: by the name you gave it,
// never the shortened label the room list shows, and on the first bridge holding it.
func TestTheClientReadsRoomsAsTheDaemonDoes(t *testing.T) {
	t.Parallel()
	m := New(context.Background(), apitest.Nop{}, config.Display{
		Names:  []config.DisplayName{{Target: "!r:x", Name: "Daily"}},
		Pinned: []string{"room:Daily"},
	})
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!r:x", Name: "Standup"},
		{ID: "!m:x", Name: "Dana Levi, Sam Cole", Members: []string{"Dana Levi", "Sam Cole"}},
	}})
	m.rooms.spaces = []domain.Space{
		{ID: "!wa:x", Name: "WhatsApp", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{"!r:x"}},
		{ID: "!tg:x", Name: "Telegram", Bridge: domain.ProtocolTelegram, Children: []domain.RoomID{"!r:x"}},
	}
	room, _ := m.roomByID("!r:x")
	want := (domain.Places{
		Names: map[domain.RoomID]string{"!r:x": "Daily"}, Pinned: domain.Pinned{Entries: []string{"room:Daily"}},
	}).Facts(room, domain.HoldersOf(room.ID, m.rooms.spaces))
	got := m.factsFor(room)
	if got.Name != want.Name || got.Protocol != want.Protocol || got.Pinned != want.Pinned || len(got.Spaces) != len(want.Spaces) {
		t.Errorf("factsFor = %+v, want %+v", got, want)
	}
	members, _ := m.roomByID("!m:x")
	if name := m.factsFor(members).Name; name != "Dana Levi, Sam Cole" {
		t.Errorf("a room named after its members is matched as %q, want its own name, not a shortened label", name)
	}
}
