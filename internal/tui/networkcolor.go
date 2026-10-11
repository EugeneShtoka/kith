package tui

import (
	"image/color"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/theme"
)

// A room's name can be drawn in its network's color ([display.theme.networks]): in room lists
// per [display] network_colors (a space_rule may override), and on the rail for a space or tag
// whose rooms are all on one network. A bridged room's network is its bridge's.

// networkColor is the color network's rooms are named in, nil for none.
func (m Model) networkColor(network domain.Protocol) color.Color {
	if network == "" {
		return nil
	}
	c, ok := theme.ParseColor(m.prefs.display.Theme.Networks.For(network.String()))
	if !ok {
		return nil
	}
	return c
}

// roomNameColor is the color of a room's name in the rail-selected place's list, nil for the
// row's own.
func (m Model) roomNameColor(room domain.Room) color.Color {
	if !m.prefs.display.NetworkColorsIn(m.namesHome(m.rail.key())) {
		return nil
	}
	return m.networkColor(m.unreadView().factsOf(room).Protocol)
}

// withNetworks tags each group with the one network all its rooms are on, if any.
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
