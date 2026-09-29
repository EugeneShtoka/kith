package tui

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The index answers exactly what a scan of the hierarchy would: the same names, in
// hierarchy order, once per space, for rooms in several spaces, none, a space listing a
// child twice, and two spaces sharing a name.
func TestSpaceHoldersMatchAScan(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))
	rooms := make([]domain.RoomID, 60)
	for i := range rooms {
		rooms[i] = domain.RoomID(fmt.Sprintf("!r%d:x", i))
	}
	spaces := make([]domain.Space, 12)
	for i := range spaces {
		spaces[i] = domain.Space{ID: domain.SpaceID(fmt.Sprintf("!s%d:x", i)), Name: fmt.Sprintf("Space %d", i%9)}
		for range r.IntN(20) {
			spaces[i].Children = append(spaces[i].Children, rooms[r.IntN(len(rooms))])
		}
	}
	indexed := inventory{}.withSpaces(spaces)
	scanned := inventory{spaces: spaces} // no index: the fallback scan

	for _, id := range append(rooms, "!nowhere:x") {
		if got, want := indexed.spaceNames(id), scanned.spaceNames(id); !slices.Equal(got, want) {
			t.Errorf("%s: index %v, scan %v", id, got, want)
		}
	}
}
