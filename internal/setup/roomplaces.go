package setup

import (
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// PlacesOf is what [display] says about rooms beyond the rooms themselves: the names
// given them, the order of spaces, the pins. Every scope reads a room's facts with
// these (domain.Places.Facts).
func PlacesOf(display config.Display) domain.Places {
	names := make(map[domain.RoomID]string, len(display.Names))
	for room, name := range display.RoomNames() {
		names[domain.RoomID(room)] = name
	}
	return domain.Places{
		Names:    names,
		Priority: display.SpacePriority,
		Pinned:   domain.Pinned{Entries: display.Pinned},
	}
}
