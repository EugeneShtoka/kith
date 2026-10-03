package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// ordering builds a space whose four rooms each order differently:
//
//	name      last message   unread
//	Alpha     oldest         no
//	Bravo     newest         no
//	Charlie   middle         yes
//	Delta     none cached    no
func ordering(t *testing.T, rooms config.Rooms) Model {
	display := config.Display{Rooms: rooms}
	t.Helper()
	m := update(t, starterNew(apitest.Nop{}, display), roomsMsg{rooms: []domain.Room{
		{ID: "!alpha:x", Name: "Alpha"},
		{ID: "!bravo:x", Name: "Bravo"},
		{ID: "!charlie:x", Name: "Charlie"},
		{ID: "!delta:x", Name: "Delta"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{{
		ID: "!w:x", Name: "Work",
		Children: []domain.RoomID{"!alpha:x", "!bravo:x", "!charlie:x", "!delta:x"},
	}}})
	m = update(t, m, unreadMsg{list: []domain.Unread{
		{RoomID: "!charlie:x", Messages: 2, Counted: true},
	}})
	spoke := func(min int) time.Time { return time.Date(2026, 9, 2, 10, min, 0, 0, time.UTC) }
	m = update(t, m, lastMessagesMsg{at: map[domain.RoomID]time.Time{
		"!alpha:x":   spoke(1),
		"!charlie:x": spoke(2),
		"!bravo:x":   spoke(3),
	}})
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	return sized(t, m.clearStatus())
}

func listed(m Model) []string {
	rooms := m.filteredRooms()
	names := make([]string, 0, len(rooms))
	for i := range rooms {
		names = append(names, rooms[i].Name)
	}
	return names
}

func sameOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// Each sort chain orders the fixture differently; name is the tie-break throughout,
// and uncached rooms go last even under ~recent.
func TestRoomOrders(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		sort  []string
		setup func(Model) Model
		want  []string
	}{
		{name: "default: unread then newest", want: []string{"Charlie", "Bravo", "Alpha", "Delta"}},
		{name: "recent", sort: []string{"recent", "name"}, want: []string{"Bravo", "Charlie", "Alpha", "Delta"}},
		{name: "name", sort: []string{"name"}, want: []string{"Alpha", "Bravo", "Charlie", "Delta"}},
		{name: "unread then name", sort: []string{"unread", "name"}, want: []string{"Charlie", "Alpha", "Bravo", "Delta"}},
		{name: "reversed recent", sort: []string{"~recent"}, want: []string{"Alpha", "Charlie", "Bravo", "Delta"}},
		{
			name: "mentions before unread", sort: []string{"mentions", "unread", "recent", "name"},
			setup: func(m Model) Model {
				return update(t, m, unreadMsg{list: []domain.Unread{
					{RoomID: "!charlie:x", Messages: 2, Counted: true},
					{RoomID: "!bravo:x", Messages: 1, Mentions: 1, Counted: true},
				}})
			},
			want: []string{"Bravo", "Charlie", "Alpha", "Delta"},
		},
		{
			name: "drafts", sort: []string{"drafts", "name"},
			setup: func(m Model) Model {
				m.drafts = map[domain.RoomID]draft{"!delta:x": {input: "half a thought"}}
				return m
			},
			want: []string{"Delta", "Alpha", "Bravo", "Charlie"},
		},
		{
			name: "name breaks equal timestamps", sort: []string{"recent", "name"},
			setup: func(m Model) Model {
				same := time.Date(2026, 9, 2, 10, 5, 0, 0, time.UTC)
				return update(t, m, lastMessagesMsg{at: map[domain.RoomID]time.Time{
					"!bravo:x": same, "!alpha:x": same, "!charlie:x": same, "!delta:x": same,
				}})
			},
			want: []string{"Alpha", "Bravo", "Charlie", "Delta"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := ordering(t, config.Rooms{Sort: tc.sort})
			if tc.setup != nil {
				m = tc.setup(m)
			}
			sameOrder(t, listed(m), tc.want...)
		})
	}
}

// A per-space order applies to that group only.
func TestPerSpaceOrder(t *testing.T) {
	t.Parallel()

	m := ordering(t, config.Rooms{
		Rules: []config.RoomsRule{{Group: "Work", Sort: []string{"name"}}},
	})
	sameOrder(t, listed(m), "Alpha", "Bravo", "Charlie", "Delta")

	m.rail.cursor = indexOfGroup(m.rail.groups, "home")
	sameOrder(t, listed(m), "Charlie", "Bravo", "Alpha", "Delta")
}

// A sort override applies to the group it was set in only.
func TestSortChordChangesThisGroupOnly(t *testing.T) {
	t.Parallel()

	m := ordering(t, config.Rooms{})
	m.focus = paneRooms
	m, _ = press(t, m, keyText("s"))
	m, _ = press(t, m, keyText("u"))
	sameOrder(t, listed(m), "Bravo", "Charlie", "Alpha", "Delta")

	m.rail.cursor = indexOfGroup(m.rail.groups, "home")
	sameOrder(t, listed(m), "Charlie", "Bravo", "Alpha", "Delta")
}

// A room unread when opened keeps its band while read, until left.
func TestOpenRoomKeepsItsBandWhileRead(t *testing.T) {
	t.Parallel()

	m := ordering(t, config.Rooms{})
	m, _ = m.selectRoom(roomByName(t, m, "!charlie:x"))
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!charlie:x", Counted: true}})
	sameOrder(t, listed(m), "Charlie", "Bravo", "Alpha", "Delta")

	m, _ = m.selectRoom(roomByName(t, m, "!alpha:x"))
	sameOrder(t, listed(m), "Bravo", "Charlie", "Alpha", "Delta")
}

// Opening a read room does not promote it.
func TestOpeningAReadRoomDoesNotPromoteIt(t *testing.T) {
	t.Parallel()

	m := ordering(t, config.Rooms{})
	m, _ = m.selectRoom(roomByName(t, m, "!alpha:x"))
	sameOrder(t, listed(m), "Charlie", "Bravo", "Alpha", "Delta")
}

// An incoming message moves its room without a cache re-read.
func TestIncomingMessageReordersTheList(t *testing.T) {
	t.Parallel()

	m := ordering(t, config.Rooms{Sort: []string{"recent", "name"}})
	sameOrder(t, listed(m), "Bravo", "Charlie", "Alpha", "Delta")

	m = update(t, m, incomingMsg{message: domain.Message{
		ID: "$new", RoomID: "!delta:x", Sender: "@someone:x", Body: "hello",
		Timestamp: time.Date(2026, 9, 2, 11, 0, 0, 0, time.UTC),
	}})
	sameOrder(t, listed(m), "Delta", "Bravo", "Charlie", "Alpha")
}

// An edit (original timestamp) does not move a room.
func TestAnEditDoesNotMoveARoom(t *testing.T) {
	t.Parallel()

	m := ordering(t, config.Rooms{Sort: []string{"recent", "name"}})
	m = update(t, m, incomingMsg{message: domain.Message{
		ID: "$old", RoomID: "!alpha:x", Sender: "@someone:x", Body: "fixed",
		Edited: true, Timestamp: time.Date(2026, 9, 2, 10, 1, 0, 0, time.UTC),
	}})
	sameOrder(t, listed(m), "Bravo", "Charlie", "Alpha", "Delta")
}

// A misspelled order fails startup rather than falling back.
func TestBadOrderIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		rooms config.Rooms
	}{
		{name: "global", rooms: config.Rooms{Sort: []string{"recency"}}},
		{name: "per group", rooms: config.Rooms{
			Rules: []config.RoomsRule{{Group: "Work", Sort: []string{"alphabetic"}}},
		}},
		{name: "a rule naming nothing", rooms: config.Rooms{
			Rules: []config.RoomsRule{{Sort: []string{"recent"}}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := derive(config.Config{Display: config.Display{Rooms: tc.rooms}}); err == nil {
				t.Error("a misspelled order was accepted")
			}
		})
	}
}

// The sort chords edit the current group's chain, each leaving the other half alone,
// and the status names the whole chain.
func TestSortKeysEditTheChain(t *testing.T) {
	t.Parallel()

	m := ordering(t, config.Rooms{}) // unread, recent, name
	m.focus = paneRooms
	if pending, _ := press(t, m, keyText("s")); pending.chordPending() == "" {
		t.Fatal("the first step of the sort chord did not wait for the second")
	}
	byName := chordPress(t, m, "s", "a")
	sameOrder(t, listed(byName), "Charlie", "Alpha", "Bravo", "Delta")
	if !strings.Contains(byName.status(), "unread first, then by name") {
		t.Errorf("status = %q, want the whole chain in words", byName.status())
	}
	flat := chordPress(t, byName, "s", "u")
	sameOrder(t, listed(flat), "Alpha", "Bravo", "Charlie", "Delta")
	if !strings.Contains(flat.status(), "by name") {
		t.Errorf("status = %q", flat.status())
	}
	mentions := chordPress(t, flat, "s", "m")
	if !strings.Contains(mentions.status(), "mentions first") {
		t.Errorf("status = %q, want mentions in the chain", mentions.status())
	}
	drafts := chordPress(t, mentions, "s", "d")
	if !strings.Contains(drafts.status(), "drafts first, mentions first") {
		t.Errorf("status = %q, want both partitions, newest addition first", drafts.status())
	}
	newest := chordPress(t, flat, "s", "r")
	sameOrder(t, listed(newest), "Bravo", "Charlie", "Alpha", "Delta")
}

func chordPress(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		m, _ = press(t, m, keyText(k))
	}
	return m
}

// The sort chords belong to the room list and are inert in other panes.
func TestSortChordsOnlyFireInTheRoomList(t *testing.T) {
	t.Parallel()

	for _, pane := range []struct {
		name  string
		focus pane
	}{
		{"timeline", paneTimeline},
		{"rail", paneRail},
	} {
		t.Run(pane.name, func(t *testing.T) {
			t.Parallel()

			m := ordering(t, config.Rooms{})
			m.focus = paneRooms
			before := listed(m)

			m.focus = pane.focus
			m, _ = press(t, m, keyText("s"))
			if got := m.chordPending(); got != "" {
				t.Fatalf("s started a chord in the %s, pending %q", pane.name, got)
			}
			m, _ = press(t, m, keyText("a"))

			m.focus = paneRooms
			sameOrder(t, listed(m), before...)
		})
	}
}
