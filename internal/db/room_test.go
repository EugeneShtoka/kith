package db

import (
	"errors"
	"testing"
)

// Writes the disk can hold pass; more than it has free is ErrNoRoom, and two folders
// on one disk add up.
func TestRoomForAddsUpWritesToOneDisk(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()
	d, err := diskOf(a)
	if err != nil {
		t.Fatal(err)
	}
	free := int64(min(d.free, 1<<62))
	if err := RoomFor(map[string]int64{a: 1 << 20}); err != nil {
		t.Fatalf("RoomFor(1 MiB) = %v; want room", err)
	}
	if err := RoomFor(map[string]int64{a: free}); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("RoomFor(all the free space, with its tenth spare) = %v; want ErrNoRoom", err)
	}
	// Each alone fits; together on one disk they do not.
	half := free * 6 / 10
	if err := RoomFor(map[string]int64{a: half}); err != nil {
		t.Fatalf("RoomFor(60%% of it) = %v; want room", err)
	}
	if err := RoomFor(map[string]int64{a: half, b: half}); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("RoomFor(60%% twice on one disk) = %v; want ErrNoRoom", err)
	}
}
