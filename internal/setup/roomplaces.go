package setup

import (
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// PlacesOf is what the config says about rooms beyond the rooms themselves: the names
// given them, the order homes are chosen in, the tags. Every scope reads
// a room's facts with these (domain.Places.Facts). The config is validated already: a
// tag that will not parse leaves the tags out.
func PlacesOf(cfg config.Config) domain.Places {
	display := cfg.Display
	names := make(map[domain.RoomID]string, len(display.Names))
	for room, name := range display.RoomNames() {
		names[domain.RoomID(room)] = name
	}
	tags, _, _ := Tags(cfg)
	return domain.Places{
		Names: names,
		Order: domain.NewHomeOrder(display.Priority, display.Rail.Order, tags, nil),
		Tags:  tags,
	}
}
