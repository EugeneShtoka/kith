package setup

import (
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// PlacesOf is the config's room names, home order and tags, used by domain.Places.Facts.
// The config is pre-validated; unparsable tags are left out.
func PlacesOf(cfg config.Config) domain.Places {
	display := cfg.Display
	names := make(map[domain.RoomID]string, len(display.Names))
	for room, name := range display.RoomNames() {
		names[domain.RoomID(room)] = name
	}
	tags, _, _ := Tags(cfg)
	archives, _ := Archives(cfg, tags)
	return domain.Places{
		Names:    names,
		Order:    domain.NewHomeOrder(display.Priority, display.Rail.Order, tags, nil, display.SpaceLabels()),
		Tags:     tags,
		Archives: archives,
	}
}
