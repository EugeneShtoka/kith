package tui

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A forum has a row in the rail, after the spaces, named as the room is: its room
// list is the forum, then every topic, read or not, which is the only place a topic
// read through is listed. The forum stays in its space too; a plain room gets no row.
func TestAForumsTopicsAreReachedFromTheRail(t *testing.T) {
	t.Parallel()
	b := &threadListBackend{threads: map[domain.RoomID][]domain.Thread{"!f:x": {
		{RoomID: "!f:x", Root: "$trips", Title: "Trips", Count: 4, LatestAt: at(5)},
		{RoomID: "!f:x", Root: "$gear", Title: "Gear", Count: 9, LatestAt: at(3)},
	}}}
	m := sized(t, update(t, starterNew(b, config.Display{Names: []config.DisplayName{{Target: "!f:x", Name: "Hiking"}}}),
		roomsMsg{rooms: []domain.Room{{ID: "!f:x", Name: "Hikers", Forum: true}, {ID: "!g:x", Name: "Plain"}}}))
	m = update(t, m, spacesMsg{spaces: []domain.Space{{ID: "!s:x", Name: "Outdoors", Children: []domain.RoomID{"!f:x", "!g:x"}}}})
	m = update(t, m, unreadMsg{list: []domain.Unread{{RoomID: "!f:x", Messages: 1, Threads: []domain.ThreadUnread{{Root: "$trips", Title: "Trips", LatestAt: at(5)}}}}})

	keys := make([]string, 0, len(m.rail.groups))
	for _, g := range m.rail.groups {
		keys = append(keys, g.key)
	}
	at := slices.Index(keys, forumGroupKey("Hikers"))
	if at < 0 || at < slices.Index(keys, "Outdoors") || slices.Contains(keys, forumGroupKey("Plain")) {
		t.Fatalf("rail = %v, want the forum's row after the spaces, and none for a plain room", keys)
	}
	if label := m.rail.groups[at].label; label != "Hiking" {
		t.Errorf("label = %q, want the room's own name for it", label)
	}

	next, cmd := m.jumpToGroup(forumGroupKey("Hikers"))
	listed, ok := msgOf[roomThreadsMsg](t, cmd)
	if !ok {
		t.Fatal("opening the forum's row asked for none of its topics")
	}
	m = update(t, next, listed)
	var rows []string
	for _, r := range m.roomRows() {
		rows = append(rows, string(r.room.ID)+string(r.thread.Root))
	}
	if want := []string{"!f:x", "!f:x$trips", "!f:x$gear"}; !slices.Equal(rows, want) {
		t.Errorf("rows = %v, want the forum then every topic, the unread one first", rows)
	}

	// A forum's row is no space: a rule begun there is not written as one.
	if ruled, _ := m.openRuleForGroup(); ruled.picker.active() {
		t.Error("a rule from the forum's row was offered as if the row were a space")
	}

	next, _ = m.jumpToGroup("Outdoors")
	m = next
	var plain []string
	for _, r := range m.roomRows() {
		plain = append(plain, string(r.room.ID)+string(r.thread.Root))
	}
	if !slices.Contains(plain, "!f:x") || slices.Contains(plain, "!f:x$gear") {
		t.Errorf("in its space = %v, want the forum with only its unread topic", plain)
	}
}

// A forum filed into Archived, which takes its rooms out of the spaces, leaves the
// rail with them, and is back once filed out.
func TestAnArchivedForumHasNoRowInTheRail(t *testing.T) {
	t.Parallel()
	for _, archived := range []bool{false, true} {
		archive := config.Tag{Name: "Archived", Exclusive: true, SpaceExclusive: true}
		if archived {
			archive.Picked = []string{"!f:x"}
		}
		cfg := config.Config{Tags: []config.Tag{{Name: "All", Rule: []string{"*"}}, archive}}
		m := update(t, configured(cfg), roomsMsg{rooms: []domain.Room{{ID: "!f:x", Name: "Hikers", Forum: true}}})
		if _, shown := findGroup(m.rail.groups, forumGroupKey("Hikers")); shown == archived {
			t.Errorf("archived %v: the forum's row shown %v (rail %v)", archived, shown, groupKeys(m.rail.groups))
		}
	}
}

// A forum that arrives with a later room list, a room already open, gets its row.
func TestAForumListedLaterGetsItsRow(t *testing.T) {
	t.Parallel()
	m := update(t, starterNew(&threadListBackend{}, config.Display{}), roomsMsg{rooms: []domain.Room{{ID: "!g:x", Name: "Plain"}}})
	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: "!g:x", Name: "Plain"}, {ID: "!f:x", Name: "Hikers", Forum: true}}})
	if _, ok := findGroup(m.rail.groups, forumGroupKey("Hikers")); !ok {
		t.Errorf("rail = %v, want the forum's row", groupKeys(m.rail.groups))
	}
}
