package tui

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A forum has a row in the rail, after the spaces, named as the room is: its room
// list is the forum, then every topic, read or not, which is the only place a topic
// read through is listed. Like a space, it is listed on that row only, not as a room of
// its space; a plain room gets no row.
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
	if !slices.Equal(plain, []string{"!g:x"}) {
		t.Errorf("in its space = %v, want only the plain room: the forum has its own row", plain)
	}
}

// A forum filed into Archived, which takes its rooms out of the spaces, leaves the
// rail with them, and is listed in Archived as a room instead, so it is never out of
// reach; filed out, its row is back and Archived does not list it.
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
		archive2, _ := findGroup(m.rail.groups, "tag:Archived")
		all, _ := findGroup(m.rail.groups, "tag:All")
		room, _ := m.roomByID("!f:x")
		if inArchive := archive2.admits != nil && archive2.admits(m.unreadView(), room); inArchive != archived {
			t.Errorf("archived %v: Archived lists the forum %v", archived, inArchive)
		}
		if all.admits(m.unreadView(), room) {
			t.Errorf("archived %v: All lists the forum as a room", archived)
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

// Stepping down a forum's row walks every topic in turn to the last, read ones too: a
// read topic under the cursor keeps its place in the list.
func TestSteppingDownAForumReachesEveryTopic(t *testing.T) {
	t.Parallel()
	topics := []domain.Thread{
		{RoomID: "!f:x", Root: "$a", Title: "A", LatestAt: at(9)},
		{RoomID: "!f:x", Root: "$b", Title: "B", LatestAt: at(8)},
		{RoomID: "!f:x", Root: "$c", Title: "C", LatestAt: at(7)},
		{RoomID: "!f:x", Root: "$d", Title: "D", LatestAt: at(6)},
	}
	b := &threadListBackend{threads: map[domain.RoomID][]domain.Thread{"!f:x": topics}}
	m := sized(t, update(t, starterNew(b, config.Display{}), roomsMsg{rooms: []domain.Room{{ID: "!f:x", Name: "Hikers", Forum: true}}}))
	m = update(t, m, unreadMsg{list: []domain.Unread{{RoomID: "!f:x", Messages: 1, Threads: []domain.ThreadUnread{{Root: "$a", Title: "A", LatestAt: at(9)}}}}})
	next, cmd := m.jumpToGroup(forumGroupKey("Hikers"))
	listed, _ := msgOf[roomThreadsMsg](t, cmd)
	m = update(t, next, listed)
	// The forum's timeline holds each topic's start and a reply in it, as an open
	// forum's does.
	var page []domain.Message
	for i, topic := range topics {
		page = append(page,
			domain.Message{ID: topic.Root, RoomID: "!f:x", Sender: "@dana:x", Body: topic.Title, Timestamp: at(i)},
			domain.Message{ID: topic.Root + "-1", RoomID: "!f:x", Sender: "@dana:x", Body: "hi", Timestamp: at(i + 10), ThreadRoot: topic.Root})
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

// A topic is part of its forum: the filing picker and /tag refuse it rather than file
// the whole forum, and the forum's own row files the forum.
func TestATopicIsNotFiledOnItsOwn(t *testing.T) {
	t.Parallel()
	b := &threadListBackend{threads: map[domain.RoomID][]domain.Thread{"!f:x": {{RoomID: "!f:x", Root: "$trips", Title: "Trips", LatestAt: at(5)}}}}
	m := sized(t, update(t, starterNew(b, config.Display{}), roomsMsg{rooms: []domain.Room{{ID: "!f:x", Name: "Hikers", Forum: true}}}))
	next, cmd := m.jumpToGroup(forumGroupKey("Hikers"))
	listed, _ := msgOf[roomThreadsMsg](t, cmd)
	m = update(t, next, listed)
	m.focus = paneRooms
	room, _ := m.roomByID("!f:x")

	onForum, _ := m.openSpacePicker()
	if !onForum.picker.active() {
		t.Fatalf("the forum's own row opened no filing picker: %q", onForum.status())
	}
	m, _ = m.stepRow(1)
	if row, _ := m.selectedRow(); row.thread.Root != "$trips" {
		t.Fatalf("cursor on %q, want the topic", row.thread.Root)
	}
	onTopic, _ := m.openSpacePicker()
	if onTopic.picker.active() || onTopic.status() != topicNotFiled {
		t.Errorf("on a topic the picker opened %v, said %q", onTopic.picker.active(), onTopic.status())
	}
	tagged, _ := m.toggleTag("Archived", room)
	if tagged.status() != topicNotFiled {
		t.Errorf("/tag on a topic said %q", tagged.status())
	}
	if facts := tagged.factsFor(room); slices.Contains(facts.Tags, "Archived") {
		t.Error("/tag on a topic archived the whole forum")
	}
}
