package tui

import (
	"image/color"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/theme"
)

// A room's name can be drawn in its network's color ([display.network_colors]): in the
// room list of a place that asks for it ([[display.space_rule]] network_colors), and,
// on the rail, a space or tag whose rooms are all on one network ([display.rail]
// network_colors). A room's network is its facts' (domain.RoomFacts.Protocol): a
// bridged Matrix room's is its bridge's.

// networkColor is the color network's rooms are named in, nil for none.
func (m Model) networkColor(network domain.Protocol) color.Color {
	if network == "" {
		return nil
	}
	c, ok := theme.ParseColor(m.prefs.display.NetworkColors.For(network.String()))
	if !ok {
		return nil
	}
	return c
}

// colorsByNetwork reports whether the place a rail key is (a space's name, tag:<name>)
// names its rooms in their networks' colors.
func (m Model) colorsByNetwork(place string) bool {
	for _, r := range m.prefs.display.SpaceRules {
		if strings.EqualFold(r.Space, place) {
			return r.NetworkColors
		}
	}
	return false
}

// roomNameColor is the color a room's name is drawn in in the room list of the place
// selected on the rail, nil for the row's own.
func (m Model) roomNameColor(room domain.Room) color.Color {
	if !m.colorsByNetwork(m.rail.key()) {
		return nil
	}
	return m.networkColor(m.unreadView().factsOf(room).Protocol)
}

// withNetworks is groups, each with the one network all its rooms are on, if they are.
func (m Model) withNetworks(groups []group) []group {
	view := m.unreadView()
	for i := range groups {
		var network domain.Protocol
		for j := range m.rooms.all {
			if !groups[i].admits(view, m.rooms.all[j]) {
				continue
			}
			on := view.factsOf(m.rooms.all[j]).Protocol
			if network != "" && on != network {
				network = ""
				break
			}
			network = on
		}
		groups[i].network = network
	}
	return groups
}
