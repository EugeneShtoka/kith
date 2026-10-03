package tui

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// memberships is, for every room, which tags hold it.
func memberships(m Model) map[domain.RoomID][]bool {
	view := m.unreadView()
	out := make(map[domain.RoomID][]bool, len(m.rooms.all))
	for i := range m.rooms.all {
		out[m.rooms.all[i].ID] = slices.Clone(view.tagsOf(m.rooms.all[i]).in)
	}
	return out
}

// Over random tags, rooms sharing names, entries naming rooms by name, silent tags
// and unread state: /tag puts the room in the tag or takes it out — it holds it
// exactly when it did not before — and no other room's tags change. The written
// config reads back to the same answer.
func TestFilingIntoATagChangesThatRoomAlone(t *testing.T) {
	t.Parallel()
	names := []string{"A", "B", "C"}
	terms := []string{"*", "dm", "unread", "space:Made", "room:A", "room:B", "not dm", "not room:C", "tag:T0"}
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 23)) // #nosec G404 -- reproducible
		var rooms []domain.Room
		var made []domain.RoomID
		for i := range 2 + rng.IntN(5) {
			id := domain.RoomID(fmt.Sprintf("!r%d:x", i))
			rooms = append(rooms, domain.Room{ID: id, Name: names[rng.IntN(len(names))], IsDirect: rng.IntN(3) == 0})
			if rng.IntN(2) == 0 {
				made = append(made, id)
			}
		}
		entry := func() string {
			if rng.IntN(2) == 0 {
				return "room:" + names[rng.IntN(len(names))]
			}
			return string(rooms[rng.IntN(len(rooms))].ID)
		}
		var tags []config.Tag
		for i := range 1 + rng.IntN(3) {
			tag := config.Tag{Name: fmt.Sprintf("T%d", i), Exclusive: rng.IntN(4) == 0}
			if rng.IntN(3) == 0 {
				tag.CountsUnread = &off
			}
			for range rng.IntN(3) {
				if term := terms[rng.IntN(len(terms))]; i > 0 || term != "tag:T0" {
					tag.Rule = append(tag.Rule, term)
				}
			}
			for range rng.IntN(2) {
				tag.Picked = append(tag.Picked, entry())
			}
			for range rng.IntN(2) {
				tag.Excluded = append(tag.Excluded, entry())
			}
			tags = append(tags, tag)
		}
		m := update(t, configured(config.Config{Tags: tags}), roomsMsg{rooms: rooms})
		m = update(t, m, spacesMsg{spaces: []domain.Space{{ID: "!made:x", Name: "Made", Children: made}}})
		for _, r := range rooms {
			if rng.IntN(2) == 0 {
				m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: r.ID, Messages: 1, Counted: true}})
			}
		}
		for step := range 6 {
			room := rooms[rng.IntN(len(rooms))]
			tag := rng.IntN(len(tags))
			before := memberships(m)
			m, _ = m.toggleTag(tags[tag].Name, room)
			after := memberships(m)
			where := fmt.Sprintf("seed %d step %d: /tag T%d on %s (%s)", seed, step, tag, room.ID, room.Name)
			if after[room.ID][tag] == before[room.ID][tag] {
				t.Fatalf("%s: held = %t before and after; tags %+v", where, before[room.ID][tag], m.conf.base.Tags)
			}
			for id, was := range before {
				if id != room.ID && !slices.Equal(after[id], was) {
					t.Fatalf("%s: %s changed tags %v → %v; tags %+v", where, id, was, after[id], m.conf.base.Tags)
				}
			}
			reread := update(t, configured(m.conf.base.Clone()), roomsMsg{rooms: rooms})
			reread = update(t, reread, spacesMsg{spaces: m.rooms.spaces})
			reread.unread = m.unread
			if got := memberships(reread); !slices.Equal(got[room.ID], after[room.ID]) {
				t.Fatalf("%s: the written config reads back as %v, the running one says %v", where, got[room.ID], after[room.ID])
			}
		}
	}
}

// The filing picker's last row makes a tag: enter on it asks the name (the settings
// prompt), and the new tag holds the room. Escaping the name leaves everything as it
// was and opens nothing else.
func TestTheFilingPickerMakesANewTagWithTheRoomInIt(t *testing.T) {
	t.Parallel()
	open := func(t *testing.T) Model {
		t.Helper()
		m := counting(t, config.Display{})
		m.focus = paneRooms
		m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
		m, _ = m.selectRoom(roomByName(t, m, "!a:x"))
		m, _ = m.openSpacePicker()
		last := len(m.picker.items) - 1
		if last < 0 || m.picker.items[last].value != tagNew {
			t.Fatalf("filing rows = %v, want New tag last", m.picker.items)
		}
		m.picker.cursor = last
		m, _ = m.acceptPick()
		if m.picker.active() || m.prompt.kind != promptTagName {
			t.Fatalf("enter on New tag: picker open %v, prompt %v; want the tag name asked", m.picker.active(), m.prompt.kind)
		}
		return m
	}

	m := open(t)
	tags := len(m.conf.base.Tags)
	m.prompt.input = "Later"
	m, _ = m.submitPrompt()
	if got := m.conf.base.Tags[tagIndex(t, m, "Later")].Picked; len(got) != 1 || got[0] != "!a:x" {
		t.Errorf("the new tag picks %v, want the room's ID", got)
	}
	if len(m.conf.base.Tags) != tags+1 {
		t.Errorf("%d tags, want one more than %d", len(m.conf.base.Tags), tags)
	}
	if !strings.Contains(m.status(), "Alpha is in Later") {
		t.Errorf("status = %q, want it to say the room went in", m.status())
	}

	m = open(t)
	m, _ = m.cancelPrompt()
	if len(m.conf.base.Tags) != tags || m.picker.active() {
		t.Errorf("after escaping the name: %d tags (want %d), a picker open %v", len(m.conf.base.Tags), tags, m.picker.active())
	}
}
