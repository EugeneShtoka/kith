package db

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// ErrNoRoom is a write the disks cannot hold now.
var ErrNoRoom = errors.New("not enough free disk space")

// RoomFor reports, as ErrNoRoom, writes the disks cannot hold: bytes to write into each
// directory, those on one disk adding up, each disk keeping a tenth of what it takes
// spare (as roomToVacuum does).
func RoomFor(writes map[string]int64) error {
	disks := make([]disk, 0, len(writes))
	need := map[string]uint64{}
	for _, dir := range slices.Sorted(maps.Keys(writes)) {
		d, err := diskOf(dir)
		if err != nil {
			return fmt.Errorf("db: free space in %s: %w", dir, err)
		}
		n := uint64(max(writes[dir], 0))
		need[d.id] += n + n/10
		disks = append(disks, d)
	}
	for _, d := range disks {
		if d.free < need[d.id] {
			return fmt.Errorf("%w: it takes %d MiB where only %d MiB are free", ErrNoRoom, need[d.id]>>20, d.free>>20)
		}
	}
	return nil
}
