package tui

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Stepping down the thread list reaches every thread, read ones too: a read thread
// under the cursor keeps its place.
func TestSteppingDownTheThreadListReachesEveryThread(t *testing.T) {
	t.Parallel()
	threads := []domain.Thread{
		{RoomID: mx + "!f:x", Root: mx + "$a", Title: "A", LatestAt: at(9)},
		{RoomID: mx + "!f:x", Root: mx + "$b", Title: "B", LatestAt: at(8)},
		{RoomID: mx + "!f:x", Root: mx + "$c", Title: "C", LatestAt: at(7)},
		{RoomID: mx + "!f:x", Root: mx + "$d", Title: "D", LatestAt: at(6)},
	}
	device := config.DeviceDisplay{Threads: config.Threads{InRoomList: config.ThreadsAll}}
	m := sized(t, update(t, starterNew(&threadListBackend{}, config.Display{}, device), roomsMsg{rooms: []domain.Room{{ID: mx + "!f:x", Name: "Hikers"}}}))
	m = update(t, m, unreadMsg{list: []domain.Unread{{RoomID: mx + "!f:x", Messages: 1, Threads: []domain.ThreadUnread{{Root: mx + "$a", Title: "A", LatestAt: at(9)}}}}})
	m = update(t, m, roomThreadsMsg{roomID: mx + "!f:x", threads: threads})
	// The timeline holds each thread's start and a reply in it, as an open room's does.
	var page []domain.Message
	for i, th := range threads {
		page = append(page,
			domain.Message{ID: th.Root, RoomID: mx + "!f:x", Sender: "matrix:@dana:x", Body: th.Title, Timestamp: at(i)},
			domain.Message{ID: th.Root + "-1", RoomID: mx + "!f:x", Sender: "matrix:@dana:x", Body: "hi", Timestamp: at(i + 10), ThreadRoot: th.Root})
	}
	m = update(t, m, timelineMsg{roomID: mx + "!f:x", page: domain.TimelinePage{Messages: page}})
	m.focus = paneRooms

	var walked []domain.EventID
	for range 6 {
		next, _ := m.stepRow(1)
		m = next
		walked = append(walked, m.rows.cursor)
	}
	if want := []domain.EventID{mx + "$a", mx + "$b", mx + "$c", mx + "$d", mx + "$d", mx + "$d"}; !slices.Equal(walked, want) {
		t.Errorf("stepping down walked %v, want %v", walked, want)
	}
}

// A later room list rebuilds the rail: a tag hidden while empty shows once it holds a room.
func TestALaterRoomListRebuildsTheRail(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Tags: []config.Tag{
		{Name: "All", Rule: []string{"*"}},
		{Name: "Pinned", Picked: []string{mx + "!p:x"}, HideWhenEmpty: true},
	}}
	m := update(t, configured(cfg), roomsMsg{rooms: []domain.Room{{ID: mx + "!g:x", Name: "Plain"}}})
	if _, ok := findGroup(m.rail.groups, "tag:Pinned"); ok {
		t.Fatal("the empty tag shows before it holds anything")
	}
	m = update(t, m, roomsMsg{rooms: []domain.Room{{ID: mx + "!g:x", Name: "Plain"}, {ID: mx + "!p:x", Name: "Pinned one"}}})
	if _, ok := findGroup(m.rail.groups, "tag:Pinned"); !ok {
		t.Errorf("rail = %v, want the tag now that it holds a room", groupKeys(m.rail.groups))
	}
}
