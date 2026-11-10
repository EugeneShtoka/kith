package tui

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

var (
	foldAlpha = domain.RoomID(mx + "!a:x")
	foldBravo = domain.RoomID(mx + "!b:x")
	// foldThreads are Alpha's unread threads, newest first: one more than the cap of two.
	foldThreads = []domain.ThreadUnread{
		{Root: mx + "$t1", Unread: 1, Latest: mx + "$l1", Title: "first"},
		{Root: mx + "$t2", Unread: 2, Latest: mx + "$l2", Title: "second"},
		{Root: mx + "$t3", Unread: 3, Latest: mx + "$l3", Title: "third"},
	}
	foldNewest = domain.ThreadUnread{Root: mx + "$t0", Unread: 1, Latest: mx + "$l0", Title: "newest"}
)

// unreadOf is a room's unread state with threads, its count their sum.
func unreadOf(room domain.RoomID, threads []domain.ThreadUnread) unreadUpdateMsg {
	return unreadUpdateMsg{u: domain.Unread{RoomID: room, Counted: true}.WithThreads(threads)}
}

// foldStart is the list the walk starts from: Alpha with three unread threads under a cap of
// two, Bravo with two, Alpha open with the cursor on its own row; its device file at path
// ("" not saved).
func foldStart(t *testing.T, path string) Model {
	t.Helper()
	dd := config.DeviceDisplay{Threads: config.Threads{MaxInRoomList: 2}}
	m := starterNew(apitest.Nop{}, config.Display{}, dd).WithDevice(path, config.Device{Display: dd})
	m = sized(t, withRooms(t, m))
	m = update(t, m, unreadOf(foldAlpha, foldThreads))
	m = update(t, m, unreadOf(foldBravo, foldThreads[:2]))
	m.focus = paneRooms
	alpha, _ := m.rooms.byID(foldAlpha)
	m, _ = m.selectRoom(alpha)
	return m
}

// rowAt puts the cursor on the n-th row (from 0) by stepping down to it.
func rowAt(t *testing.T, m Model, n int) Model {
	t.Helper()
	m, _ = m.gotoRow(n + 1)
	return m
}

// foldStep is one thing that can happen to the room list.
type foldStep struct {
	name string
	do   func(t *testing.T, m Model) Model
}

func onKey(key tea.KeyPressMsg) func(t *testing.T, m Model) Model {
	return func(t *testing.T, m Model) Model {
		t.Helper()
		m, _ = press(t, m, key)
		return m
	}
}

// foldSteps are the walk's moves: the keys, and the changes that arrive from elsewhere.
var foldSteps = []foldStep{
	{"j", onKey(keyText("j"))},
	{"k", onKey(keyText("k"))},
	{"za", func(t *testing.T, m Model) Model {
		t.Helper()
		m, _ = chord(t, m, "z", "a")
		return m
	}},
	{"enter", onKey(tea.KeyPressMsg{Code: tea.KeyEnter})},
	{"a newer thread arrives in Alpha, or goes read", func(t *testing.T, m Model) Model {
		t.Helper()
		threads := m.unread[foldAlpha].Threads
		if len(threads) > 0 && threads[0].Root == foldNewest.Root {
			return update(t, m, unreadOf(foldAlpha, threads[1:]))
		}
		return update(t, m, unreadOf(foldAlpha, append([]domain.ThreadUnread{foldNewest}, threads...)))
	}},
	{"Alpha's oldest thread is read", func(t *testing.T, m Model) Model {
		t.Helper()
		threads := m.unread[foldAlpha].Threads
		if len(threads) == 0 {
			return m
		}
		return update(t, m, unreadOf(foldAlpha, threads[:len(threads)-1]))
	}},
	{"the cap changes between two and one", func(t *testing.T, m Model) Model {
		t.Helper()
		dev := m.conf.device.Clone()
		dev.Display.Threads.MaxInRoomList = 3 - dev.Display.Threads.MaxInRoomList
		m, _ = m.applyDevice(dev, "")
		return m
	}},
	{"another window folds or unfolds Alpha", func(t *testing.T, m Model) Model {
		t.Helper()
		dev := m.conf.device.Clone()
		folded := maps.Clone(dev.Display.Threads.Folded)
		if folded[string(foldAlpha)] {
			delete(folded, string(foldAlpha))
		} else {
			folded = withEntry(folded, string(foldAlpha), true)
		}
		dev.Display.Threads.Folded = folded
		m, _ = m.adoptSettings(config.Settings{Bridge: m.conf.base, Device: dev}, "")
		return m
	}},
	{"Alpha leaves the list, or comes back", func(t *testing.T, m Model) Model {
		t.Helper()
		bravo := domain.Room{ID: foldBravo, Name: "Bravo", IsDirect: true}
		if _, listed := m.rooms.byID(foldAlpha); listed {
			return update(t, m, roomsMsg{rooms: []domain.Room{bravo}})
		}
		return update(t, m, roomsMsg{rooms: []domain.Room{{ID: foldAlpha, Name: "Alpha"}, bravo}})
	}},
}

// foldStarts are where the walk begins: the cursor on Alpha's row, a thread row, the overflow
// row; Alpha's threads all listed; Alpha folded; Bravo open.
var foldStarts = []foldStep{
	{"on Alpha's row", func(t *testing.T, m Model) Model { t.Helper(); return m }},
	{"on Alpha's first thread", func(t *testing.T, m Model) Model { t.Helper(); return rowAt(t, m, 1) }},
	{"on Alpha's overflow row", func(t *testing.T, m Model) Model { t.Helper(); return rowAt(t, m, 3) }},
	{"Alpha's threads all listed", func(t *testing.T, m Model) Model {
		t.Helper()
		m, _ = press(t, rowAt(t, m, 3), tea.KeyPressMsg{Code: tea.KeyEnter})
		return m
	}},
	{"Alpha folded", func(t *testing.T, m Model) Model {
		t.Helper()
		m, _ = chord(t, m, "z", "a")
		return m
	}},
	{"Bravo open", func(t *testing.T, m Model) Model { t.Helper(); return rowAt(t, m, 4) }},
}

// foldDepth is how many moves the walk makes from each start: every sequence of three of
// the nine (a fourth took 15s for nothing three missed in the mutation check).
const foldDepth = 3

// The room list's thread rows hold their rules over every sequence of up to foldDepth moves
// (keys, and changes another window or kithd makes) from each named start:
//   - the cursor is drawn on a row of the open room, and after za folds, on its own row;
//   - a folded room lists no thread rows, and its badge counts as it would unfolded;
//   - a room lists at most its cap of threads, with one row counting the rest, unless it
//     is the open room whose overflow row was entered; that lasts until it is left.
func TestThreadRowsFoldAndExpandOverAnyMoveSequence(t *testing.T) {
	t.Parallel()
	for _, start := range foldStarts {
		m := start.do(t, foldStart(t, ""))
		m.focus = paneRooms
		checkFoldRules(t, m, []string{start.name})
		walkFolds(t, m, []string{start.name}, foldDepth)
	}
}

func walkFolds(t *testing.T, m Model, path []string, depth int) {
	t.Helper()
	if depth == 0 || t.Failed() {
		return
	}
	for _, step := range foldSteps {
		next := step.do(t, m)
		next.focus = paneRooms // enter goes into the room; the walk stays in the list
		next.compose.insertMode = false
		at := append(slices.Clone(path), step.name)
		checkFoldRules(t, next, at)
		if step.name == "za" {
			checkFoldMovedTheCursor(t, m, next, at)
		}
		walkFolds(t, next, at, depth-1)
	}
}

func checkFoldRules(t *testing.T, m Model, path []string) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf("after %s: "+format, append([]any{strings.Join(path, " → ")}, args...)...)
	}
	rows := m.roomRows()
	if m.rows.expanded != "" && m.rows.expanded != m.openRoom {
		fail("%s's threads stay listed past the cap with %s open", m.rows.expanded, m.openRoom)
	}
	if slices.ContainsFunc(rows, func(r roomRow) bool { return r.room.ID == m.openRoom }) {
		if at := rows[m.rowCursor(rows)]; at.room.ID != m.openRoom {
			fail("the cursor is drawn on %s's row, with %s open", at.room.ID, m.openRoom)
		}
	}
	unfolded := m
	unfolded.prefs.device.Threads.Folded = nil
	rooms := m.filteredRooms()
	for i := range rooms {
		room := &rooms[i]
		threads, more := 0, 0
		for j := range rows {
			switch r := &rows[j]; {
			case r.room.ID != room.ID:
			case r.isThread():
				threads++
			case r.more > 0:
				more = r.more
			}
		}
		listed := len(m.threadsUnder(*room, config.ThreadsUnread))
		capped := m.threadCap(*room)
		badge, _ := m.unreadBadge(*room)
		if want, _ := unfolded.unreadBadge(*room); badge != want {
			fail("%s's badge is %q folded, %q unfolded", room.Name, badge, want)
		}
		switch {
		case m.prefs.device.Threads.Folds(string(room.ID)):
			if threads+more > 0 {
				fail("folded %s lists %d thread rows and +%d", room.Name, threads, more)
			}
		case room.ID == m.rows.expanded:
			if threads != listed || more != 0 {
				fail("expanded %s lists %d threads and +%d of %d", room.Name, threads, more, listed)
			}
		case threads != min(listed, capped) || more != listed-min(listed, capped):
			fail("%s lists %d threads and +%d of %d under a cap of %d", room.Name, threads, more, listed, capped)
		}
	}
}

// checkFoldMovedTheCursor: folding the open room's threads leaves the cursor on its own row.
func checkFoldMovedTheCursor(t *testing.T, was, m Model, path []string) {
	t.Helper()
	if was.prefs.device.Threads.Folds(string(was.openRoom)) || !m.prefs.device.Threads.Folds(string(m.openRoom)) {
		return
	}
	if m.rows.cursor != "" || m.rows.onMore {
		t.Errorf("after %s: the cursor stays on a folded row (thread %q, overflow %v)",
			strings.Join(path, " → "), m.rows.cursor, m.rows.onMore)
	}
}

// enter on "+N more threads" lists them all, the cursor on the first it had hidden; leaving
// the room and coming back caps them again.
func TestEnterOnTheOverflowRowListsEveryThreadUntilTheRoomIsLeft(t *testing.T) {
	t.Parallel()
	m := rowAt(t, foldStart(t, ""), 3)
	if row, _ := m.selectedRow(); row.more != 1 {
		t.Fatalf("row 3 is %+v, want the overflow row", row)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if frame := stripStyles(m.View().Content); !strings.Contains(frame, "third") || strings.Contains(frame, "more thread") {
		t.Fatalf("the overflow row did not list the rest:\n%s", frame)
	}
	if m.rows.cursor != foldThreads[2].Root || m.focus != paneRooms {
		t.Errorf("cursor on %q (focus %v), want the thread it had hidden, in the list", m.rows.cursor, m.focus)
	}

	m = rowAt(t, m, 4) // Bravo
	m, _ = m.stepRows(4, -1)
	if m.openRoom != foldAlpha {
		t.Fatalf("back in %q, want Alpha", m.openRoom)
	}
	if frame := stripStyles(m.View().Content); strings.Contains(frame, "third") || !strings.Contains(frame, "+1 more thread") {
		t.Errorf("back in Alpha, its threads are not capped again:\n%s", frame)
	}
}

// za on a thread's row hides the room's threads and puts the cursor on the room's row; the
// fold is kept in the device file, so the next start has it, and za again shows them.
func TestFoldingARoomsThreadsIsRememberedAcrossARestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), config.DeviceFile)
	m := rowAt(t, foldStart(t, path), 2)
	m, cmd := chord(t, m, "z", "a")
	m = deliver(t, m, cmd)
	if row, _ := m.selectedRow(); row.room.ID != foldAlpha || row.isThread() || row.more > 0 {
		t.Errorf("after folding, the cursor is on %+v, want Alpha's row", row)
	}
	if frame := stripStyles(m.View().Content); strings.Contains(frame, "third") || !strings.Contains(frame, "●6") {
		t.Errorf("folded Alpha shows thread rows, or not its whole count:\n%s", frame)
	}

	dev, err := config.LoadDevice(path)
	if err != nil {
		t.Fatal(err)
	}
	if !dev.Display.Threads.Folds(string(foldAlpha)) {
		t.Fatalf("the device file has folded %v, want Alpha", dev.Display.Threads.Folded)
	}
	restarted := starterNew(apitest.Nop{}, config.Display{}, dev.Display).WithDevice(path, dev)
	restarted = update(t, sized(t, withRooms(t, restarted)), unreadOf(foldAlpha, foldThreads))
	restarted.focus = paneRooms
	if frame := stripStyles(restarted.View().Content); strings.Contains(frame, "first") {
		t.Errorf("after a restart Alpha's threads are listed again:\n%s", frame)
	}

	restarted, cmd = chord(t, restarted, "z", "a")
	restarted = deliver(t, restarted, cmd)
	if frame := stripStyles(restarted.View().Content); !strings.Contains(frame, "first") {
		t.Errorf("za again did not show Alpha's threads:\n%s", frame)
	}
	if dev, _ := config.LoadDevice(path); len(dev.Display.Threads.Folded) != 0 {
		t.Errorf("unfolded, the device file still has %v", dev.Display.Threads.Folded)
	}
}
