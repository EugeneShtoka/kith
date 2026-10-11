package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// archiving is a bridge's space holding Alpha and Bravo, with Alpha in the starter's
// Archived tag: an archived room still belongs in its bridge's space.
func archiving(t *testing.T, display config.Display) Model {
	t.Helper()
	m := update(t, starterNew(&bulkReader{}, display), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!wa:x", Name: "WhatsApp UK", Keeper: "@whatsappbot:x", Bridge: domain.ProtocolWhatsApp, Children: []domain.RoomID{"!a:x", "!b:x"}},
	}})
	m = picking(t, sized(t, m.clearStatus()), "Archived", "!a:x")
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "WhatsApp UK")
	return m
}

func archivedListed(m Model) []domain.RoomID {
	var out []domain.RoomID
	rooms := m.filteredRooms()
	for i := range rooms {
		out = append(out, rooms[i].ID)
	}
	return out
}

// A room list shows archived rooms as [display] show_archived says, a place's rule
// over it; the archive's own list shows them whatever either says.
func TestArchivedRoomsAreShownAsConfigured(t *testing.T) {
	t.Parallel()
	if got := archivedListed(archiving(t, config.Display{})); !slices.Equal(got, []domain.RoomID{"!a:x", "!b:x"}) {
		t.Errorf("by default the bridge's space lists %v, want the archived room too", got)
	}
	hidden := archiving(t, config.Display{ShowArchived: new(false)})
	if got := archivedListed(hidden); !slices.Equal(got, []domain.RoomID{"!b:x"}) {
		t.Errorf("with show_archived off: %v, want Bravo alone", got)
	}
	hidden.rail.cursor = indexOfGroup(hidden.rail.groups, "tag:Archived")
	if got := archivedListed(hidden); !slices.Equal(got, []domain.RoomID{"!a:x"}) {
		t.Errorf("the archive's own list with show_archived off: %v, want Alpha", got)
	}
	ruled := archiving(t, config.Display{ShowArchived: new(false), SpaceRules: []config.SpaceRule{{Space: "WhatsApp UK", ShowArchived: new(true)}}})
	if got := archivedListed(ruled); len(got) != 2 {
		t.Errorf("a place shown whatever show_archived says: %v, want both", got)
	}
}

// The archived key flips the place in view, and that place alone, until restart; on
// the archive it says the archive always lists its rooms.
func TestTheArchivedKeyFlipsThePlaceInView(t *testing.T) {
	t.Parallel()
	m := archiving(t, config.Display{})
	m, _ = press(t, m, keyText("A"))
	if got := archivedListed(m); !slices.Equal(got, []domain.RoomID{"!b:x"}) || !strings.Contains(m.status(), "hidden") {
		t.Errorf("after A: %v, said %q; want the archived room hidden", got, m.status())
	}
	m.rail.cursor = indexOfGroup(m.rail.groups, "tag:Archived")
	m, _ = press(t, m, keyText("A"))
	if !strings.Contains(m.status(), "always lists") || !m.archivedShownIn("tag:Archived") {
		t.Errorf("A on the archive said %q", m.status())
	}
	m.rail.cursor = indexOfGroup(m.rail.groups, "WhatsApp UK")
	m, _ = m.runCommandLine("archived")
	if got := archivedListed(m); len(got) != 2 {
		t.Errorf(":archived again: %v, want both back", got)
	}
	if m.prefs.display.ShowArchived != nil || len(m.conf.base.Display.SpaceRules) != 0 {
		t.Error("the key wrote the config; it lasts until restart")
	}
}

// Settings offers each place's choice, written as its space_rule, and none for the
// archive itself.
func TestEachPlaceChoosesItsArchivedRooms(t *testing.T) {
	t.Parallel()
	m := archiving(t, config.Display{})
	m.choosing.settingGroup = "rooms"
	row, ok := m.setting(archivedKey + "WhatsApp UK")
	if !ok {
		t.Fatal("no archived-rooms row for the bridge's space")
	}
	if _, ok := m.setting(archivedKey + "tag:Archived"); ok {
		t.Error("the archive has a row of its own")
	}
	cfg := m.conf.base.Clone()
	if err := row.set(&cfg, "hidden"); err != nil {
		t.Fatal(err)
	}
	if r, ok := cfg.Display.SpaceRule("WhatsApp UK"); !ok || r.ShowArchived == nil || *r.ShowArchived {
		t.Errorf("rules = %+v, want WhatsApp UK hidden", cfg.Display.SpaceRules)
	}
	if got := row.show(cfg); got != "hidden" {
		t.Errorf("shown as %q", got)
	}
	if err := row.set(&cfg, "everywhere"); err != nil || len(cfg.Display.SpaceRules) != 0 {
		t.Errorf("as everywhere left %+v", cfg.Display.SpaceRules)
	}
}
