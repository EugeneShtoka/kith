package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// manyRooms is a room list longer than any pane can show.
func manyRooms(n int) []domain.Room {
	rooms := make([]domain.Room, n)
	for i := range rooms {
		rooms[i] = domain.Room{
			ID:   domain.RoomID(fmt.Sprintf(mx+"!r%02d:x", i)),
			Name: fmt.Sprintf("Room %02d", i),
		}
	}
	return rooms
}

// A list longer than its pane scrolls under the cursor.
func TestRoomListScrollsUnderTheCursor(t *testing.T) {
	t.Parallel()

	m := sized(t, update(t, newModel(), roomsMsg{rooms: manyRooms(40)}))
	m.focus = paneRooms
	for _, cursor := range []int{0, 12, 30, 39} {
		// The highlight follows the selection, so select to move it.
		m.openRoom = domain.RoomID(fmt.Sprintf(mx+"!r%02d:x", cursor))
		frame := stripStyles(m.View().Content)
		want := fmt.Sprintf("▸ Room %02d", cursor)
		if !strings.Contains(frame, want) {
			t.Errorf("with the cursor at %d, %q is not on screen", cursor, want)
		}
		// The window moves the least it can: the first room stays shown at the top, the last at the
		// bottom.
		switch cursor {
		case 0:
			if !strings.Contains(frame, "Room 00") {
				t.Error("at the top of the list the first room should be visible")
			}
		case 39:
			if !strings.Contains(frame, "Room 39") {
				t.Error("at the bottom the last room should be visible")
			}
		}
	}
	// The header says where you are.
	m.openRoom = mx + "!r30:x"
	if frame := stripStyles(m.View().Content); !strings.Contains(frame, "31/40") {
		t.Error("a list longer than the pane should say the position in its header")
	}
	// A list that fits says nothing extra.
	short := sized(t, update(t, newModel(), roomsMsg{rooms: manyRooms(3)}))
	if frame := stripStyles(short.View().Content); strings.Contains(frame, "/3") {
		t.Error("a list that fits should not carry a position")
	}
}

// Walking down with the keyboard scrolls rather than leaving the pane.
func TestWalkingDownTheRoomListStaysVisible(t *testing.T) {
	t.Parallel()

	m := sized(t, update(t, newModel(), roomsMsg{rooms: manyRooms(40)}))
	m.focus = paneRooms
	for i := range 39 {
		m, _ = press(t, m, keyText("j"))
		want := fmt.Sprintf("▸ Room %02d", i+1)
		if frame := stripStyles(m.View().Content); !strings.Contains(frame, want) {
			t.Fatalf("after %d presses, %q is off screen", i+1, want)
		}
	}
}

// The rail scrolls too; its dividers take rows, so the cursor's row is not its group index.
func TestRailScrollsPastItsSeparators(t *testing.T) {
	t.Parallel()

	spaces := make([]domain.Space, 40)
	for i := range spaces {
		spaces[i] = domain.Space{
			ID:   domain.SpaceID(fmt.Sprintf(mx+"!s%02d:x", i)),
			Name: fmt.Sprintf("Space %02d", i),
		}
	}
	m := sized(t, update(t, newModel(), spacesMsg{spaces: spaces}))
	m.focus = paneRail
	for _, cursor := range []int{0, len(m.rail.groups) / 2, len(m.rail.groups) - 1} {
		m.rail.cursor = cursor
		frame := stripStyles(m.View().Content)
		want := "▸ " + m.rail.groups[cursor].label
		if !strings.Contains(frame, want) {
			t.Errorf("with the rail cursor at %d, %q is not on screen", cursor, want)
		}
	}
}

// windowRows holds still while the cursor moves inside the window.
func TestWindowRows(t *testing.T) {
	t.Parallel()

	rows := []string{"a", "b", "c", "d", "e"}
	for name, tc := range map[string]struct {
		cursor, visible int
		want            string
	}{
		"fits":         {cursor: 4, visible: 5, want: "abcde"},
		"top":          {cursor: 0, visible: 3, want: "abc"},
		"inside":       {cursor: 2, visible: 3, want: "abc"},
		"steps":        {cursor: 3, visible: 3, want: "bcd"},
		"bottom":       {cursor: 4, visible: 3, want: "cde"},
		"one row":      {cursor: 2, visible: 1, want: "c"},
		"more visible": {cursor: 0, visible: 9, want: "abcde"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := strings.Join(windowRows(rows, tc.cursor, tc.visible), ""); got != tc.want {
				t.Errorf("windowRows(cursor=%d, visible=%d) = %q, want %q",
					tc.cursor, tc.visible, got, tc.want)
			}
		})
	}
}

// paneBodyRows must agree with where framePane clips.
func TestPaneBodyRowsMatchesWhatFramePaneShows(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	for _, h := range []int{6, 10, 24, 40} {
		body := paneBodyRows(h)
		rows := make([]string, body+5)
		for i := range rows {
			rows[i] = fmt.Sprintf("row%02d", i)
		}
		frame := stripStyles(m.framePane("T", rows, 20, h, false))
		shown := 0
		for i := range rows {
			if strings.Contains(frame, rows[i]) {
				shown++
			}
		}
		if shown != body {
			t.Errorf("height %d: framePane showed %d rows, paneBodyRows says %d", h, shown, body)
		}
	}
}
