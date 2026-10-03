package tui

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A tag is a room's home beside its spaces: ranked by [display] priority, the first
// home's name rule is the room's; a mute can aim at a tag the room is in.
func TestATagIsAHomeForRules(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{{Name: "Family", Rule: []string{"dm"}}}}
	cfg.Display.Priority = []string{"tag:Family", "Friends"}
	cfg.Display.SpaceRules = []config.SpaceRule{{Space: "tag:Family", FirstNameOnly: true}}
	dm := domain.Room{ID: "!a:x", Name: "Michael Livingston", Members: []string{"Michael Livingston"}, IsDirect: true}
	m := update(t, configured(cfg), roomsMsg{rooms: []domain.Room{dm}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{{ID: "!f:x", Name: "Friends", Children: []domain.RoomID{"!a:x"}}}})

	if got := m.homesOf("!a:x"); len(got) != 2 || got[0] != "tag:Family" || got[1] != "Friends" {
		t.Fatalf("homesOf = %v, want the tag first, then the space", got)
	}
	if got := m.roomLabel(dm); got != "Michael" {
		t.Errorf("label = %q, want the first home's (the tag's) first-names rule", got)
	}

	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
	m.openRoom = "!a:x"
	found := false
	for _, target := range m.muteTargets() {
		if target.match == "tag:Family" && target.kind == "tag" {
			found = true
		}
	}
	if !found {
		t.Errorf("mute targets %+v, want the room's tag", m.muteTargets())
	}

	cfg.Display.Priority = []string{"Friends"}
	m2 := update(t, configured(cfg), roomsMsg{rooms: []domain.Room{dm}})
	m2 = update(t, m2, spacesMsg{spaces: []domain.Space{{ID: "!f:x", Name: "Friends", Children: []domain.RoomID{"!a:x"}}}})
	if got := m2.roomLabel(dm); got != "Michael Livingston" {
		t.Errorf("label with Friends first = %q, want Friends' (no rule)", got)
	}
}
