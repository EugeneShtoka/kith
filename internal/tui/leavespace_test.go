package tui

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// failingLeaver records the rooms left, and fails those in fail.
type failingLeaver struct {
	apitest.Nop
	left *[]domain.RoomID
	fail map[domain.RoomID]bool
}

func (l failingLeaver) LeaveRoom(_ context.Context, room domain.RoomID) error {
	if l.fail[room] {
		return errors.New("refused")
	}
	*l.left = append(*l.left, room)
	return nil
}

// spaceCase is a generated space to leave: its rooms, the other spaces some of them are
// in too (which may themselves be left, or be an account's own), and which rooms the
// network refuses to let go.
type spaceCase struct {
	target domain.Space
	others []domain.Space
	rooms  []domain.Room
	fail   map[domain.RoomID]bool
}

func genSpaceCase(r *rand.Rand) spaceCase {
	c := spaceCase{fail: map[domain.RoomID]bool{}}
	bridge := domain.ProtocolMatrix
	if r.IntN(3) == 0 {
		bridge = domain.ProtocolWhatsApp
	}
	c.target = domain.Space{ID: "!target:x", Name: "Target", Bridge: bridge,
		Leaving: []domain.SpaceLeaving{domain.LeftAlone, domain.LeftAlone, domain.LeftAlone, domain.LeftWithRooms, domain.LeftWhole, domain.NotLeft}[r.IntN(6)]}
	for i := range 1 + r.IntN(6) {
		id := domain.RoomID(fmt.Sprintf("!r%d:x", i))
		c.rooms = append(c.rooms, domain.Room{ID: id, Name: fmt.Sprintf("Room %d", i)})
		c.target.Children = append(c.target.Children, id)
		if r.IntN(4) == 0 {
			c.fail[id] = true
		}
	}
	if r.IntN(6) == 0 {
		c.fail[domain.RoomID(c.target.ID)] = true
	}
	for i := range r.IntN(3) {
		other := domain.Space{ID: domain.SpaceID(fmt.Sprintf("!other%d:x", i)), Name: fmt.Sprintf("Other %d", i),
			Bridge: domain.ProtocolMatrix, Leaving: []domain.SpaceLeaving{domain.LeftAlone, domain.NotLeft, domain.LeftWithRooms, domain.LeftByRoom, domain.LeftWhole}[r.IntN(5)]}
		for _, id := range c.target.Children {
			if r.IntN(2) == 0 {
				other.Children = append(other.Children, id)
			}
		}
		if len(other.Children) == 0 {
			other.Children = []domain.RoomID{c.target.Children[0]}
		}
		c.others = append(c.others, other)
	}
	return c
}

// sharedRooms is, independently of the TUI, which of the target's rooms are also in a
// space that could be left itself (an account's own space holds every room: not
// another place), and wholeRooms which are parts of another forum, left only with it.
func (c spaceCase) sharedRooms() map[domain.RoomID]bool {
	shared := map[domain.RoomID]bool{}
	for _, o := range c.others {
		if o.Leaving == domain.NotLeft || o.Leaving == domain.LeftByRoom || o.Leaving == domain.LeftWhole {
			continue
		}
		for _, id := range o.Children {
			shared[id] = true
		}
	}
	return shared
}

func (c spaceCase) wholeRooms() map[domain.RoomID]bool {
	whole := map[domain.RoomID]bool{}
	for _, o := range c.others {
		if o.Leaving == domain.LeftByRoom || o.Leaving == domain.LeftWhole {
			for _, id := range o.Children {
				whole[id] = true
			}
		}
	}
	return whole
}

// Leaving a space from the rail, over every kind of space, overlap with other spaces,
// answer and refusal: nothing is left before the last yes, and a no there leaves
// nothing; a WhatsApp community asks once and takes all its rooms; a Matrix space asks
// whether its rooms go, then about each one also in another space (naming that space,
// not an account's own), yes and no to all answering the rest, and a room of another
// forum is kept, left only with all of it, which the last question says; a forum
// itself goes with all its rooms, asked once; what goes is left, the
// space last, and a room the network refuses neither stops the others nor the space,
// and is named; a bridged room going is said to be left on its network only if its
// bridge passes leaves on; a bridge's own space for an account is not left.
func TestLeavingASpaceFromTheRail(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(11, 23))
	for i := range 300 {
		c := genSpaceCase(r)
		var left []domain.RoomID
		m := update(t, New(context.Background(), failingLeaver{left: &left, fail: c.fail}, config.Display{}), roomsMsg{rooms: c.rooms})
		m = sized(t, update(t, m, spacesMsg{spaces: append([]domain.Space{c.target}, c.others...)}))
		m.focus = paneRail
		m.rail.cursor = indexOfGroup(m.rail.groups, "Target")
		if m.rail.cursor < 0 {
			t.Fatalf("case %d: the space is not on the rail", i)
		}
		m = pressKey(t, m, "L")
		if c.target.Leaving == domain.NotLeft {
			if m.confirm.active() || !strings.Contains(m.status(), "only the bridge signs out") {
				t.Fatalf("case %d: a bridge's account space: confirm %v, status %q", i, m.confirm.action, m.status())
			}
			continue
		}

		var want []domain.RoomID
		shared, whole := c.sharedRooms(), c.wholeRooms()
		kept := 0
		stray(t, i, r, m)
		if c.target.Leaving == domain.LeftWithRooms || c.target.Leaving == domain.LeftWhole {
			want = slices.Clone(c.target.Children)
		} else {
			if m.confirm.action != pendingLeaveSpaceRooms {
				t.Fatalf("case %d: first question %v %q, want whether its rooms go", i, m.confirm.action, m.confirmPrompt())
			}
			roomsToo := r.IntN(3) > 0
			m = pressKey(t, m, map[bool]string{true: "y", false: "n"}[roomsToo])
			var asked []domain.RoomID
			if roomsToo {
				for _, id := range c.target.Children {
					if whole[id] {
						kept++
						continue
					}
					if shared[id] {
						asked = append(asked, id)
					} else {
						want = append(want, id)
					}
				}
			}
			var all string
			for _, id := range asked {
				if all != "" {
					if all == "Y" {
						want = append(want, id)
					}
					continue
				}
				if m.confirm.action != pendingLeaveSharedRoom || !strings.Contains(m.confirmPrompt(), "Room "+strings.TrimPrefix(strings.Split(string(id), ":")[0], "!r")) {
					t.Fatalf("case %d: asked %v %q, want about %s", i, m.confirm.action, m.confirmPrompt(), id)
				}
				for _, o := range c.others {
					in := slices.Contains(o.Children, id) && o.Leaving != domain.NotLeft && !whole[id]
					if strings.Contains(m.confirmPrompt(), o.Name) != in {
						t.Fatalf("case %d: %q names %s: %v, want %v", i, m.confirmPrompt(), o.Name, !in, in)
					}
				}
				answer := []string{"y", "n", "Y", "N"}[r.IntN(4)]
				m = pressKey(t, m, answer)
				switch answer {
				case "y":
					want = append(want, id)
				case "Y":
					want, all = append(want, id), "Y"
				case "N":
					all = "N"
				}
			}
		}
		stray(t, i, r, m)
		if len(left) > 0 {
			t.Fatalf("case %d: left %v before the last question was answered", i, left)
		}
		if m.confirm.action != pendingLeaveSpaceItself {
			t.Fatalf("case %d: last question %v %q, want whether to leave the space", i, m.confirm.action, m.confirmPrompt())
		}
		prompt := m.confirmPrompt()
		if strings.Contains(prompt, "of forums stay") != (kept > 0) {
			t.Fatalf("case %d: %q, %d forum rooms kept", i, prompt, kept)
		}
		if bridged := c.target.Bridge == domain.ProtocolWhatsApp && len(want) > 0; strings.Contains(prompt, "on WhatsApp") != bridged {
			t.Fatalf("case %d: %q says bridged rooms leave WhatsApp: %v, want %v", i, prompt, !bridged, bridged)
		}
		if r.IntN(5) == 0 {
			m = pressKey(t, m, "n")
			if len(left) > 0 || m.confirm.active() {
				t.Fatalf("case %d: a last no left %v (confirm %v)", i, left, m.confirm.action)
			}
			continue
		}
		next, cmd := asModel(m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"}))
		msg, ok := msgOf[spaceLeftMsg](t, cmd)
		if !ok {
			t.Fatalf("case %d: the last yes left nothing", i)
		}
		next = update(t, next, msg)

		var wantLeft []domain.RoomID
		for _, id := range append(slices.Clone(want), domain.RoomID(c.target.ID)) {
			if !c.fail[id] {
				wantLeft = append(wantLeft, id)
			}
		}
		if !slices.Equal(left, wantLeft) {
			t.Fatalf("case %d: left %v, want %v (refused: %v)", i, left, wantLeft, c.fail)
		}
		status := next.status()
		for _, id := range want {
			_, listed := next.roomByID(id)
			if c.fail[id] {
				if !listed || !strings.Contains(status, next.spaceRoomName(id)) {
					t.Fatalf("case %d: refused %s: listed %v, status %q", i, id, listed, status)
				}
			} else if listed {
				t.Fatalf("case %d: %s was left but is still listed", i, id)
			}
		}
		if strings.Contains(status, "could not leave Target") != c.fail[domain.RoomID(c.target.ID)] {
			t.Fatalf("case %d: status %q, space refused: %v", i, status, c.fail[domain.RoomID(c.target.ID)])
		}
	}
}

// stray presses yes or no to all where neither is asked: the question stays as it was.
func stray(t *testing.T, i int, r *rand.Rand, m Model) {
	t.Helper()
	if r.IntN(2) == 0 {
		return
	}
	got := pressKey(t, m, []string{"Y", "N"}[r.IntN(2)])
	if got.confirm.action != m.confirm.action || got.confirmPrompt() != m.confirmPrompt() {
		t.Fatalf("case %d: a stray answer to all turned %q into %q", i, m.confirmPrompt(), got.confirmPrompt())
	}
}
