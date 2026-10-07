package tui

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// With every thread listed under its room, stepping down walks each in turn to the
// last, read ones too: a read thread under the cursor keeps its place in the list.
func TestSteppingDownTheThreadListReachesEveryThread(t *testing.T) {
	t.Parallel()
	threads := []domain.Thread{
		{RoomID: "!f:x", Root: "$a", Title: "A", LatestAt: at(9)},
		{RoomID: "!f:x", Root: "$b", Title: "B", LatestAt: at(8)},
		{RoomID: "!f:x", Root: "$c", Title: "C", LatestAt: at(7)},
		{RoomID: "!f:x", Root: "$d", Title: "D", LatestAt: at(6)},
	}
	display := config.Display{Threads: config.Threads{InRoomList: config.ThreadsAll}}
	m := sized(t, update(t, starterNew(&threadListBackend{}, display), roomsMsg{rooms: []domain.Room{{ID: "!f:x", Name: "Hikers"}}}))
	m = update(t, m, unreadMsg{list: []domain.Unread{{RoomID: "!f:x", Messages: 1, Threads: []domain.ThreadUnread{{Root: "$a", Title: "A", LatestAt: at(9)}}}}})
	m = update(t, m, roomThreadsMsg{roomID: "!f:x", threads: threads})
	// The room's timeline holds each thread's start and a reply in it, as an open
	// room's does.
	var page []domain.Message
	for i, th := range threads {
		page = append(page,
			domain.Message{ID: th.Root, RoomID: "!f:x", Sender: "@dana:x", Body: th.Title, Timestamp: at(i)},
			domain.Message{ID: th.Root + "-1", RoomID: "!f:x", Sender: "@dana:x", Body: "hi", Timestamp: at(i + 10), ThreadRoot: th.Root})
	}
	m = update(t, m, timelineMsg{roomID: "!f:x", page: domain.TimelinePage{Messages: page}})
	m.focus = paneRooms

	var walked []domain.EventID
	for range 6 {
		next, _ := m.stepRow(1)
		m = next
		walked = append(walked, m.rows.cursor)
	}
	if want := []domain.EventID{"$a", "$b", "$c", "$d", "$d", "$d"}; !slices.Equal(walked, want) {
		t.Errorf("stepping down walked %v, want %v", walked, want)
	}
}

// A room list that arrives later, a room already open, rebuilds the rail: a tag hidden
// while it held nothing shows once it holds a room.
func TestALaterRoomListRebuildsTheRail(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{
		{Name: "All", Rule: []string{"*"}},
		{Name: "Pinned", Picked: []string{"!p:x"}, HideWhenEmpty: true},
	}}
	m := update(t, configured(cfg), roomsMsg{rooms: []domain.Room{{ID: "!g:x", Name: "Plain"}}})
	if _, ok := findGroup(m.rail.groups, "tag:Pinned"); ok {
		t.Fatal("the empty tag shows before it holds anything")
	}
	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: "!g:x", Name: "Plain"}, {ID: "!p:x", Name: "Pinned one"}}})
	if _, ok := findGroup(m.rail.groups, "tag:Pinned"); !ok {
		t.Errorf("rail = %v, want the tag now that it holds a room", groupKeys(m.rail.groups))
	}
}
