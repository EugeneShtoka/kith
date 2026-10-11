package tui

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The index matches a hierarchy scan: same names, in hierarchy order, once per space,
// for rooms in several spaces or none, a duplicated child, and two same-named spaces.
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

	for _, id := range append(rooms, "!nowhere:x") {
		var want []string
		for i := range spaces {
			if slices.Contains(spaces[i].Children, id) {
				want = append(want, spaces[i].DisplayName())
			}
		}
		if got := indexed.spaceNames(id); !slices.Equal(got, want) {
			t.Errorf("%s: index %v, scan %v", id, got, want)
		}
	}
}
