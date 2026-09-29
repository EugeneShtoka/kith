package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The gutter and the count are one idea: the numbers answer "how far is that" and the
// count is what you do with the answer.

// selectedRoomName is which room the list is on, by name — enough to tell three
// different rooms apart, which is all these tests ask.
func selectedRoomName(t *testing.T, m Model) string {
	t.Helper()
	room, ok := m.currentRoom()
	if !ok {
		return ""
	}
	return room.Name
}

// digits presses a run of number keys, which is what typing a count looks like.
func digits(t *testing.T, m Model, run string) Model {
	t.Helper()
	for _, r := range run {
		next, _ := m.handleKey(tea.KeyPressMsg{Code: r})
		m = next
	}
	return m
}

func TestDigitsAccumulateIntoACount(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = digits(t, m, "12")
	if m.motion.count != 12 {
		t.Fatalf("count = %d after typing 12, want 12", m.motion.count)
	}
	// It is shown while it is pending: a count you cannot see is one you cannot tell
	// from a key that did nothing.
	if !strings.Contains(m.status(), "12") {
		t.Errorf("status = %q, want the pending count", m.status())
	}
}

// A leading zero is not a count.
func TestALeadingZeroIsNotACount(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = digits(t, m, "0")
	if m.motion.count != 0 {
		t.Errorf("count = %d after a leading zero, want none", m.motion.count)
	}
	// After a digit, zero extends it like any other.
	m = digits(t, m, "10")
	if m.motion.count != 10 {
		t.Errorf("count = %d after 1 then 0, want 10", m.motion.count)
	}
}

// The key after the digits takes them, and takes them once.
func TestACountIsTakenByTheNextMotionAndCleared(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.motion.count = 7
	m.motion.repeat, m.motion.count = m.motion.count, 0
	if got := m.take(); got != 7 {
		t.Fatalf("take = %d, want the count that was typed", got)
	}
	if got := m.take(); got != 1 {
		t.Errorf("take = %d the second time, want 1 — one count drives one motion", got)
	}
}

// A count typed and then abandoned must not multiply something three keystrokes later,
// which is the surprise vim avoids with the same rule.
func TestACountIsClearedByAKeyThatIsNotAMotion(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = digits(t, m, "9")
	next, _ := m.handleKey(tea.KeyPressMsg{Code: 'x'})
	m = next
	if m.motion.count != 0 {
		t.Errorf("count = %d after another key, want it cleared", m.motion.count)
	}
	// The key that follows takes what is pending — which is now nothing — so a count
	// typed and abandoned cannot multiply a motion two keystrokes later.
	after, _ := m.handleKey(tea.KeyPressMsg{Code: 'x'})
	m = after
	if got := m.take(); got != 1 {
		t.Errorf("the abandoned count survived as %d", got)
	}
}

// Rooms are stepped over one at a time rather than by index, because the rows between
// two rooms are not all rooms: a group header is a row and not a place.
func TestACountStepsThatManySelectableRows(t *testing.T) {
	t.Parallel()

	rooms := make([]domain.Room, 0, 6)
	for i := range 6 {
		rooms = append(rooms, domain.Room{ID: domain.RoomID(string(rune('a' + i))), Name: string(rune('A' + i))})
	}
	m := sized(t, update(t, newModel(), roomsMsg{rooms: rooms}))
	m.focus = paneRooms

	start := selectedRoomName(t, m)
	next, _ := m.stepRows(3, 1)
	m = next
	after := selectedRoomName(t, m)
	if start == after {
		t.Fatal("a count of three moved nothing")
	}
	// Three steps down, then three back, is where it started.
	back, _ := m.stepRows(3, -1)
	m = back
	if got := selectedRoomName(t, m); got != start {
		t.Errorf("three down and three up landed on %q, want %q", got, start)
	}
}

// Running out of list stops at the last row rather than refusing the motion, which is
// what every clamped cursor in this client does.
func TestACountPastTheEndClampsRatherThanRefusing(t *testing.T) {
	t.Parallel()

	rooms := []domain.Room{{ID: "!a", Name: "A"}, {ID: "!b", Name: "B"}, {ID: "!c", Name: "C"}}
	m := sized(t, update(t, newModel(), roomsMsg{rooms: rooms}))
	m.focus = paneRooms

	next, _ := m.stepRows(99, 1)
	m = next
	if selectedRoomName(t, m) == "" {
		t.Fatal("a count past the end selected nothing")
	}
	// And going back the same distance lands on the first, not somewhere before it.
	back, _ := m.stepRows(99, -1)
	m = back
	if got := selectedRoomName(t, m); got != rooms[0].Name {
		t.Errorf("99 back landed on %q, want the first room", got)
	}
}

// Off by default: a gutter is a column of screen given to a habit not everybody has.
func TestTheGutterIsOffUntilAskedFor(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	if m.gutterWidth() != 0 {
		t.Errorf("gutter width = %d by default, want none", m.gutterWidth())
	}
	wide := m.contentWidth()

	m.prefs.display.RowNumbers = true
	if m.gutterWidth() != gutterCells {
		t.Errorf("gutter width = %d when on, want %d", m.gutterWidth(), gutterCells)
	}
	// **Taken out of the content width**, not drawn over it: the layout wraps to this
	// number and the scroll arithmetic counts the rows it produces.
	if narrow := m.contentWidth(); narrow != wide-gutterCells {
		t.Errorf("content width = %d with the gutter on, want %d", narrow, wide-gutterCells)
	}
}

// Distance is counted in messages — what the cursor moves in — and only the first row
// of a message carries a number, because that is the row you can land on.
func TestTheGutterNumbersMessagesNotRows(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.prefs.display.RowNumbers = true
	rows := []string{"first", "wrapped", "second", "third"}
	owners := []int{4, 4, 5, 6}

	got := m.numbered(rows, owners, 0)
	if len(got) != len(rows) {
		t.Fatalf("%d rows back, want %d", len(got), len(rows))
	}
	for i, want := range []string{"4", "", "5", "6"} {
		cell := strings.TrimSpace(stripStyles(got[i])[:gutterCells])
		if cell != want {
			t.Errorf("row %d gutter = %q, want %q", i, cell, want)
		}
	}
}

// A date divider belongs to no message, so it carries no number: measuring from a row
// the cursor can never be on would be measuring the wrong thing.
func TestTheGutterSkipsRowsThatBelongToNoMessage(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.prefs.display.RowNumbers = true
	got := m.numbered([]string{"— Tuesday —", "hello"}, []int{noOwner, 3}, 0)
	if cell := strings.TrimSpace(stripStyles(got[0])[:gutterCells]); cell != "" {
		t.Errorf("the divider got %q in the gutter", cell)
	}
	if cell := strings.TrimSpace(stripStyles(got[1])[:gutterCells]); cell != "3" {
		t.Errorf("the message got %q, want its distance", cell)
	}
}

func TestTheGutterMarksTheCursorAndCapsTheDistance(t *testing.T) {
	t.Parallel()

	for distance, want := range map[int]string{0: "▸", 1: "1", -1: "1", 99: "99", -99: "99", 100: "~", -412: "~"} {
		if got := gutterLabel(distance); got != want {
			t.Errorf("gutterLabel(%d) = %q, want %q", distance, got, want)
		}
	}
}

// `12G` in the room list: the count with a go-to-the-end key means *that row*, which is
// the one reading of it that says anything — and it is what the numbers are for.
func TestACountWithTheEndKeyGoesToThatRow(t *testing.T) {
	t.Parallel()

	rooms := make([]domain.Room, 0, 8)
	for i := range 8 {
		rooms = append(rooms, domain.Room{ID: domain.RoomID("!" + string(rune('a'+i))), Name: string(rune('A' + i))})
	}
	m := sized(t, update(t, newModel(), roomsMsg{rooms: rooms}))
	m.focus = paneRooms

	next, _ := m.gotoRow(3)
	m = next
	third := selectedRoomName(t, m)

	// The same row from the other end of the list is the same row.
	back, _ := m.gotoRow(1)
	m = back
	again, _ := m.gotoRow(3)
	m = again
	if got := selectedRoomName(t, m); got != third {
		t.Errorf("row 3 = %q, then %q; a row number must mean one row", third, got)
	}
	if third == "" {
		t.Fatal("row 3 selected nothing")
	}
}

// Past the end lands on the last row rather than refusing, like every clamped cursor
// here.
func TestGotoRowPastTheEndClamps(t *testing.T) {
	t.Parallel()

	rooms := []domain.Room{{ID: "!a", Name: "A"}, {ID: "!b", Name: "B"}}
	m := sized(t, update(t, newModel(), roomsMsg{rooms: rooms}))
	m.focus = paneRooms

	next, _ := m.gotoRow(99)
	m = next
	if got := selectedRoomName(t, m); got != "B" {
		t.Errorf("row 99 of two landed on %q, want the last", got)
	}
	last, _ := m.gotoLastRow()
	m = last
	if got := selectedRoomName(t, m); got != "B" {
		t.Errorf("G landed on %q, want the last room", got)
	}
}

// The rail is a list like any other: `3G` is the third group, and its positions are
// stable enough for a number to mean one thing.
func TestACountReachesTheRailToo(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	if len(m.rail.groups) < 2 {
		t.Skip("the rail needs two groups to move between")
	}
	last := len(m.rail.groups)

	next, _ := m.railEnd(actSelectNewest)
	m = next
	if m.rail.cursor != last-1 {
		t.Errorf("G landed on group %d, want the last (%d)", m.rail.cursor+1, last)
	}

	// With a count, the same key means that group.
	m.motion.repeat = 2
	after, _ := m.railEnd(actSelectNewest)
	m = after
	if m.rail.cursor != 1 {
		t.Errorf("2G landed on group %d, want the second", m.rail.cursor+1)
	}

	// And the count is spent: pressing it again is the plain end key.
	again, _ := m.railEnd(actSelectNewest)
	m = again
	if m.rail.cursor != last-1 {
		t.Errorf("G after a count landed on group %d, want the last", m.rail.cursor+1)
	}
}

// Past the end clamps rather than refusing, like every other cursor here.
func TestARailCountPastTheEndClamps(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	if len(m.rail.groups) == 0 {
		t.Skip("no rail")
	}
	m.motion.repeat = 99
	next, _ := m.railEnd(actSelectOldest)
	m = next
	if m.rail.cursor != len(m.rail.groups)-1 {
		t.Errorf("99G landed on group %d of %d, want the last", m.rail.cursor+1, len(m.rail.groups))
	}
}

// One pair of keys, every list: `gg`/`G` are `[keys.nav]`, shared by every pane, so the
// top of the timeline, of the rail, of the room list and of a chooser are all the same
// keystroke — and a count before them means that row in each.
func TestTheEndKeysAreOnePairForEveryList(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	for _, sc := range []scope{scopeRail, scopeRooms, scopeTimeline} {
		if act := m.keys.lookup("G", sc, scopeNav, scopeCommand); act != actSelectNewest {
			t.Errorf("G in scope %v resolves to %v, want the one shared action", sc, act)
		}
	}
}
