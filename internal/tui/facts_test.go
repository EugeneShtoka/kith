package tui

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Codes and tracked words read a room as the daemon does: by the name you gave it,
// never the shortened list label, and on the first bridge holding it.
func TestTheClientReadsRoomsAsTheDaemonDoes(t *testing.T) {
	t.Parallel()
	m := starterNew(apitest.Nop{}, config.Display{
		Names: []config.DisplayName{{Target: mx + "!r:x", Name: "Daily"}},
	})
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: mx + "!r:x", Name: "Standup"},
		{ID: mx + "!m:x", Name: "Dana Levi, Sam Cole", Members: []string{"Dana Levi", "Sam Cole"}},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: mx + "!wa:x", Name: "WhatsApp", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{mx + "!r:x"}},
		{ID: mx + "!tg:x", Name: "Telegram", Bridge: domain.ProtocolTelegram, Children: []domain.RoomID{mx + "!r:x"}},
	}})
	room, _ := m.roomByID(mx + "!r:x")
	want := (domain.Places{
		Names: map[domain.RoomID]string{mx + "!r:x": "Daily"}, Tags: m.rail.tags,
	}).Facts(room, domain.HoldersOf(room.ID, m.rooms.spaces))
	got := m.factsFor(room)
	if got.Name != want.Name || got.Protocol != want.Protocol || !slices.Equal(got.Tags, want.Tags) || len(got.Spaces) != len(want.Spaces) {
		t.Errorf("factsFor = %+v, want %+v", got, want)
	}
	members, _ := m.roomByID(mx + "!m:x")
	if name := m.factsFor(members).Name; name != "Dana Levi, Sam Cole" {
		t.Errorf("a room named after its members is matched as %q, want its own name, not a shortened label", name)
	}
}
