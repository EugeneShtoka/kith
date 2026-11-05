package domain

import "time"

// PictureCheck is what the cache knows of a room's picture without its bytes: the
// network's ID for it ("" none) and when the network was last asked.
type PictureCheck struct {
	ID      string
	Checked time.Time
}
