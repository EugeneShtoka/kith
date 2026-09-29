package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/theme"
)

// at is a compact timestamp constructor for timeline-ordering tests.
func at(sec int) time.Time { return time.Unix(int64(sec), 0) }

// manyMessages returns n placeholder messages in room !a:x, in time order.
func manyMessages(n int) []domain.Message {
	msgs := make([]domain.Message, n)
	for i := range msgs {
		msgs[i] = domain.Message{ID: domain.EventID(fmt.Sprintf("$%d", i)), RoomID: "!a:x", Timestamp: at(i)}
	}
	return msgs
}

func newModel() Model {
	return New(context.Background(), apitest.Nop{}, config.Display{})
}

// press feeds a key into Update and returns the new Model.
func press(t *testing.T, m Model, key tea.KeyPressMsg) (Model, tea.Cmd) {
	t.Helper()
	return asModel(m.Update(key))
}

// chord presses a multi-key sequence and returns what the last press produced.
func chord(t *testing.T, m Model, keys ...string) (Model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, key := range keys {
		m, cmd = press(t, m, keyText(key))
	}
	return m, cmd
}

func update(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := asModel(m.Update(msg))
	return next
}

// settle runs a command and folds in what it produces until nothing new comes back.
// Room loads are armed on a timer, so they are two turns of the loop away.
func settle(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	pending := []tea.Cmd{cmd}
	for range 4 {
		var next []tea.Cmd
		for _, c := range pending {
			if c == nil || isTimer(c) || isListener(c) {
				continue
			}
			switch msg := c().(type) {
			case nil:
			case tea.BatchMsg:
				next = append(next, msg...)
			default:
				var follow tea.Cmd
				m, follow = asModel(m.Update(msg))
				next = append(next, follow)
			}
		}
		if len(next) == 0 {
			return m
		}
		pending = next
	}
	return m
}

// deliver runs a command and folds every message it produced (batches flattened) into the model.
func deliver(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	for _, msg := range msgsOf(t, cmd) {
		m = update(t, m, msg)
	}
	return m
}

// rested is m once the room it just opened has settled: cmd (what selectRoom returned)
// run, and then the room load its timer would fire, delivered here instead.
func rested(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	m = settle(t, m, cmd)
	next, load := asModel(m.Update(roomLoadMsg{roomID: m.openRoom, armed: m.loadArmed}))
	return settle(t, next, load)
}

// The command checks below match a closure by the part of its name that survives
// inlining: instrumented builds (-race, -cover) inline differently, and a closure
// inlined into its caller is named after both ("Model.askHold.Model.holdDraftCmd.func1").

// isListener reports whether cmd waits on a stream (listen): the tests deliver what a
// stream would carry themselves, and a listener run here could wait forever.
func isListener(cmd tea.Cmd) bool { return strings.Contains(cmdFunc(cmd), "listen[") }

// cmdFunc is the full name of the function a command is, or "".
func cmdFunc(cmd tea.Cmd) string {
	if fn := runtime.FuncForPC(reflect.ValueOf(cmd).Pointer()); fn != nil {
		return fn.Name()
	}
	return ""
}

// isTimer reports whether cmd is a tea.Tick (a debounce, a sweep, the player's
// pulse): running one would sleep through its wait, so the helpers drop it, and a
// test that needs the timer's message delivers that message itself.
func isTimer(cmd tea.Cmd) bool {
	name := cmdFunc(cmd)
	return strings.HasPrefix(name, "charm.land/bubbletea/v2.Tick.") || strings.Contains(name, ".Tick.func")
}

// timers counts the timers a command arms, walking into batches.
func timers(t *testing.T, cmd tea.Cmd) int {
	t.Helper()
	if cmd == nil {
		return 0
	}
	if isTimer(cmd) {
		return 1
	}
	// Only a batch is opened: any other command is left unrun.
	if !isBatch(cmd) {
		return 0
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		return 0
	}
	n := 0
	for _, c := range batch {
		n += timers(t, c)
	}
	return n
}

// untimed is what a command does besides arming timers: its batch opened (and nothing
// else run), timers left out, and one command or a batch of the rest, nil for none.
func untimed(t *testing.T, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	var rest []tea.Cmd
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		switch {
		case c == nil, isTimer(c):
		case isBatch(c):
			batch, _ := c().(tea.BatchMsg)
			for _, inner := range batch {
				walk(inner)
			}
		default:
			rest = append(rest, c)
		}
	}
	walk(cmd)
	switch len(rest) {
	case 0:
		return nil
	case 1:
		return rest[0]
	default:
		return tea.Batch(rest...)
	}
}

// isBatch reports whether cmd is a tea.Batch (bubbletea names it compactCmds).
func isBatch(cmd tea.Cmd) bool { return strings.Contains(cmdFunc(cmd), "compactCmds[") }

// msgsOf runs a command and returns every message it produced, flattening batches.
// Timers are not run (isTimer).
func msgsOf(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil || isTimer(cmd) || isListener(cmd) {
		return nil
	}
	switch msg := cmd().(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		out := make([]tea.Msg, 0, len(msg))
		for _, c := range msg {
			out = append(out, msgsOf(t, c)...)
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

// runCmd executes a command for its side effects, walking into batches.
func runCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	msgsOf(t, cmd)
}

// msgOf returns the first message of type T a command produced.
func msgOf[T tea.Msg](t *testing.T, cmd tea.Cmd) (T, bool) {
	t.Helper()
	for _, msg := range msgsOf(t, cmd) {
		if typed, ok := msg.(T); ok {
			return typed, true
		}
	}
	var zero T
	return zero, false
}

func keyCode(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// sendKey is the composer's send binding (ctrl+enter; plain enter is a newline).
func sendKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}
}
func keyText(text string) tea.KeyPressMsg {
	code, _ := utf8.DecodeRuneInString(text)
	return tea.KeyPressMsg{Code: code, Text: text}
}

// msgAt is a message from @a:x in !a:x at second sec.
func msgAt(id domain.EventID, body string, sec int) domain.Message {
	return domain.Message{ID: id, RoomID: "!a:x", Sender: "@a:x", Body: body, Timestamp: at(sec)}
}

// loadPage delivers msgs as a live timeline page for !a:x.
func loadPage(t *testing.T, m Model, msgs []domain.Message) Model {
	t.Helper()
	return update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: msgs}})
}

// withRooms feeds a two-room list (one of them a DM) into the model.
func withRooms(t *testing.T, m Model) Model {
	t.Helper()
	return update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo", IsDirect: true},
	}})
}

// sized marks the model ready with a comfortable frame.
func sized(t *testing.T, m Model) Model {
	t.Helper()
	return update(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
}

func TestHandleRoomsSelectsFirst(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	if len(m.rooms.all) != 2 {
		t.Fatalf("rooms = %d, want 2", len(m.rooms.all))
	}
	room, ok := m.currentRoom()
	if !ok || room.Name != "Alpha" {
		t.Fatalf("currentRoom = %+v ok=%v, want Alpha", room, ok)
	}
	if m.status() != "loading history…" {
		t.Errorf("status = %q, want loading history", m.status())
	}
}

// Tab walks right and stops at the timeline (leaving tab to the composer); shift+tab
// walks left and stops at the rail.
func TestFocusMovesAndStops(t *testing.T) {
	t.Parallel()

	m := newModel()
	m, _ = press(t, m, keyCode(tea.KeyTab))
	if m.focus != paneRooms {
		t.Fatalf("focus after tab = %d, want paneRooms", m.focus)
	}
	m, _ = press(t, m, keyCode(tea.KeyTab))
	if m.focus != paneTimeline {
		t.Fatalf("focus after 2 tabs = %d, want paneTimeline", m.focus)
	}
	m, _ = press(t, m, keyCode(tea.KeyTab))
	if m.focus != paneTimeline {
		t.Fatalf("focus after 3 tabs = %d, want to stay on paneTimeline", m.focus)
	}
	for range 3 {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	}
	if m.focus != paneRail {
		t.Fatalf("focus after 3 shift+tabs = %d, want paneRail", m.focus)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.focus != paneRail {
		t.Fatalf("focus past the left end = %d, want to stay on paneRail", m.focus)
	}
}

func TestRailFiltersToDMs(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel()) // focus rail, group Home
	if got := len(m.filteredRooms()); got != 2 {
		t.Fatalf("Home rooms = %d, want 2", got)
	}
	m, _ = press(t, m, keyCode(tea.KeyDown)) // rail → DMs group
	if m.rail.cursor != 1 {
		t.Fatalf("groupCursor = %d, want 1 (DMs)", m.rail.cursor)
	}
	fr := m.filteredRooms()
	if len(fr) != 1 || fr[0].Name != "Bravo" {
		t.Fatalf("DMs rooms = %+v, want [Bravo]", fr)
	}
	room, ok := m.currentRoom()
	if !ok || room.Name != "Bravo" {
		t.Errorf("currentRoom after switching to DMs = %+v, want Bravo", room)
	}
}

func TestRefreshPreservesRoomSelection(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel()) // Alpha (!a:x), Bravo (!b:x, DM)
	m.focus = paneRooms
	m, _ = press(t, m, keyCode(tea.KeyDown)) // select Bravo
	if room, _ := m.currentRoom(); room.ID != "!b:x" {
		t.Fatalf("precondition: selected %q, want !b:x", room.ID)
	}

	// A reordered refresh keeps Bravo selected and does not reload it.
	m, cmd := asModel(m.Update(roomsMsg{rooms: []domain.Room{
		{ID: "!c:x", Name: "Charlie"},
		{ID: "!b:x", Name: "Bravo", IsDirect: true},
		{ID: "!a:x", Name: "Alpha"},
	}}))
	if room, _ := m.currentRoom(); room.ID != "!b:x" {
		t.Errorf("after refresh selected %q, want !b:x preserved", room.ID)
	}
	if cmd != nil {
		t.Error("preserving the same room should not reload its timeline")
	}
}

func TestRefreshReselectsWhenRoomGone(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m.focus = paneRooms
	m, _ = press(t, m, keyCode(tea.KeyDown)) // select Bravo (!b:x)

	// Bravo is gone from the refreshed list → re-select the first room.
	m, cmd := asModel(m.Update(roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}}))
	if room, _ := m.currentRoom(); room.ID != "!a:x" {
		t.Errorf("selected %q, want !a:x after selection vanished", room.ID)
	}
	if cmd == nil {
		t.Error("re-selecting a room should load its timeline")
	}
}

func TestRefreshRoomsErrorKeepsList(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m = update(t, m, roomsMsg{err: context.Canceled}) // refresh failed
	if len(m.rooms.all) != 2 {
		t.Errorf("rooms after refresh error = %d, want 2 kept", len(m.rooms.all))
	}
}

func TestSpacesRefreshPreservesGroup(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	m.rail.cursor = 3 // select Work (after Home, DMs, Unread)

	// A refresh reorders/extends the spaces; the Work selection follows by label.
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!f:x", Name: "Friends"},
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	if m.rail.groups[m.rail.cursor].label != "Work" {
		t.Errorf("selected group = %q, want Work preserved", m.rail.groups[m.rail.cursor].label)
	}
}

func TestSpacesJoinRail(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel()) // rooms: !a:x (Alpha), !b:x (Bravo, DM)
	if len(m.rail.groups) != 3 {
		t.Fatalf("initial groups = %d, want 3 (Home, DMs, Unread)", len(m.rail.groups))
	}
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!work:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	if len(m.rail.groups) != 4 {
		t.Fatalf("groups after spaces = %d, want 4 (Home, DMs, Unread, Work)", len(m.rail.groups))
	}
	if m.rail.groups[3].label != "Work" {
		t.Errorf("space group = %+v, want label Work", m.rail.groups[3])
	}

	// Selecting the Work space filters the room list to its child room.
	m.rail.cursor = 3
	fr := m.filteredRooms()
	if len(fr) != 1 || fr[0].Name != "Alpha" {
		t.Fatalf("Work rooms = %+v, want [Alpha]", fr)
	}
}

func TestSpacesErrorKeepsSyntheticRail(t *testing.T) {
	t.Parallel()

	m := update(t, newModel(), spacesMsg{err: context.Canceled})
	if len(m.rail.groups) != 3 {
		t.Errorf("groups after spaces error = %d, want 3 synthetic entries (Home, DMs, Unread)", len(m.rail.groups))
	}
}

func TestRailConfig(t *testing.T) {
	t.Parallel()

	spaces := []domain.Space{{ID: "!w:x", Name: "Work"}, {ID: "!f:x", Name: "Friends"}}
	keys := func(gs []group) []string {
		out := make([]string, len(gs))
		for i, g := range gs {
			out[i] = g.key
		}
		return out
	}

	// Default: home (labeled All), dms, unread, then spaces in order.
	def := railGroups(spaces, config.Rail{}, nil, unreadView{}, 0, nil, nil)
	if got := strings.Join(keys(def), ","); got != "home,dms,unread,Work,Friends" {
		t.Errorf("default order = %q", got)
	}
	if def[0].label != "All" {
		t.Errorf("home default label = %q, want All", def[0].label)
	}

	// Rename changes only the label, not the key.
	ren := railGroups(spaces, config.Rail{}, []config.DisplayName{{Target: config.NameTargetGroup + "home", Name: "Everything"}}, unreadView{}, 0, nil, nil)
	if ren[0].key != "home" || ren[0].label != "Everything" {
		t.Errorf("renamed home = %+v, want key home / label Everything", ren[0])
	}

	// Hidden removes a group; listed order comes first, unlisted follow.
	ord := railGroups(spaces, config.Rail{
		Order:  []string{"Work", "dms"},
		Hidden: []string{"home"},
	}, nil, unreadView{}, 0, nil, nil)
	if got := strings.Join(keys(ord), ","); got != "Work,dms,unread,Friends" {
		t.Errorf("ordered+hidden = %q, want Work,dms,unread,Friends", got)
	}

	// Hiding every group falls back to keeping home so the rail is never empty.
	guard := railGroups(spaces, config.Rail{Hidden: []string{"home", "dms", "unread", "Work", "Friends"}}, nil, unreadView{}, 0, nil, nil)
	if len(guard) != 1 || guard[0].key != "home" {
		t.Errorf("hide-all guard = %+v, want [home]", keys(guard))
	}

	// A dash token draws a divider after the preceding group; unlisted unread follows.
	sep := railGroups(spaces, config.Rail{Order: []string{"home", "dms", "-", "Work", "Friends"}}, nil, unreadView{}, 0, nil, nil)
	if got := strings.Join(keys(sep), ","); got != "home,dms,Work,Friends,unread" {
		t.Errorf("separator order = %q, want home,dms,Work,Friends,unread", got)
	}
	for _, g := range sep {
		if (g.key == "dms") != g.sepAfter {
			t.Errorf("sepAfter on %q = %v, want divider only after dms", g.key, g.sepAfter)
		}
	}
}

// "*" places every unnamed group, and a group named after it is not swallowed by it.
func TestRailWildcardPlacesTheUnnamed(t *testing.T) {
	t.Parallel()

	spaces := []domain.Space{{ID: "!w:x", Name: "Work"}, {ID: "!f:x", Name: "Friends"}}
	keys := func(gs []group) string {
		out := make([]string, len(gs))
		for i, g := range gs {
			out[i] = g.key
		}
		return strings.Join(out, ",")
	}

	tail := railGroups(spaces, config.Rail{Order: []string{"unread", "*", "dms"}}, nil, unreadView{}, 0, nil, nil)
	if got := keys(tail); got != "unread,home,Work,Friends,dms" {
		t.Errorf("wildcard order = %q, want unread,home,Work,Friends,dms", got)
	}

	// Dividers around the wildcard fall where written.
	div := railGroups(spaces, config.Rail{Order: []string{"unread", "-", "*", "-", "dms"}}, nil, unreadView{}, 0, nil, nil)
	for _, g := range div {
		want := g.key == "unread" || g.key == "Friends" // the last of the wildcard block
		if g.sepAfter != want {
			t.Errorf("sepAfter on %q = %v, want %v", g.key, g.sepAfter, want)
		}
	}

	twice := railGroups(spaces, config.Rail{Order: []string{"*", "*"}}, nil, unreadView{}, 0, nil, nil)
	if got := keys(twice); got != "home,dms,unread,Work,Friends" {
		t.Errorf("two wildcards = %q, want the default order once", got)
	}
	none := railGroups(spaces, config.Rail{Order: []string{"Work"}}, nil, unreadView{}, 0, nil, nil)
	if got := keys(none); got != "Work,home,dms,unread,Friends" {
		t.Errorf("no wildcard = %q, want the unnamed appended as before", got)
	}
}

// Both left panes draw a marker, the name and the count, with no glyphs.
func TestRailAndRoomsShowMarkerButNoGlyphs(t *testing.T) {
	t.Parallel()

	m := update(t, newModel(), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo", IsDirect: true},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	m = sized(t, m)

	rail := ansi.Strip(m.renderRail(20))
	rooms := ansi.Strip(m.renderRooms(roomsWidth, 20))
	for pane, out := range map[string]string{"rail": rail, "rooms": rooms} {
		for _, glyph := range []string{"⌂", "◆", "●D"} {
			if strings.Contains(out, glyph) {
				t.Errorf("%s still draws the %s glyph:\n%s", pane, glyph, out)
			}
		}
		if strings.Count(out, "▸") != 1 {
			t.Errorf("%s should mark exactly one row, got %d:\n%s", pane, strings.Count(out, "▸"), out)
		}
	}
	// Every label starts in the same column: border, then the marker's two.
	for _, tc := range []struct{ pane, out, label string }{
		{"rail", rail, "All"}, {"rail", rail, "Unread"}, {"rail", rail, "Work"},
		{"rooms", rooms, "Alpha"}, {"rooms", rooms, "Bravo"},
	} {
		line, ok := lineWith(tc.out, tc.label)
		if !ok {
			t.Fatalf("%s: no row for %s:\n%s", tc.pane, tc.label, tc.out)
		}
		at := strings.Index(line, tc.label) // lineWith found it, so this is never -1
		// Columns, not bytes: the border glyph is multi-byte.
		if col := ansi.StringWidth(line[:at]); col != 1+cursorWidth {
			t.Errorf("%s: %s starts in column %d, want %d", tc.pane, tc.label, col, 1+cursorWidth)
		}
	}
}

// lineWith returns the first line of a rendered pane containing want.
func lineWith(rendered, want string) (string, bool) {
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.Contains(line, want) {
			return line, true
		}
	}
	return "", false
}

func TestEnterAdvancesFocus(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m, _ = press(t, m, keyCode(tea.KeyEnter)) // rail → rooms
	if m.focus != paneRooms {
		t.Fatalf("focus = %d, want paneRooms", m.focus)
	}
	m, _ = press(t, m, keyCode(tea.KeyEnter)) // rooms → timeline
	if m.focus != paneTimeline {
		t.Fatalf("focus = %d, want paneTimeline", m.focus)
	}
	// The first esc leaves the composer, the second the pane.
	m, _ = press(t, m, keyCode(tea.KeyEsc))
	if m.focus != paneTimeline || m.compose.insertMode {
		t.Fatalf("after one esc: focus = %d insert = %v, want the message cursor", m.focus, m.compose.insertMode)
	}
	m, _ = press(t, m, keyCode(tea.KeyEsc))
	if m.focus != paneRooms {
		t.Fatalf("focus after the second esc = %d, want paneRooms", m.focus)
	}
}

func TestRoomOpensInInsertMode(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m, _ = press(t, m, keyCode(tea.KeyEnter)) // rail → rooms
	m, _ = press(t, m, keyCode(tea.KeyEnter)) // rooms → timeline
	if !m.compose.insertMode {
		t.Error("opening a room should start in the composer")
	}
	// Typing goes into the message straight away, with no mode change first.
	m, _ = press(t, m, keyText("h"))
	m, _ = press(t, m, keyText("i"))
	if m.compose.input != "hi" {
		t.Errorf("input = %q, want typing to land in the composer immediately", m.compose.input)
	}

	// Browsing the room list does not force the composer open — only opening does.
	browse := sized(t, withRooms(t, newModel()))
	browse, _ = press(t, browse, keyCode(tea.KeyEnter)) // rail → rooms
	browse, _ = press(t, browse, keyText("j"))          // move down the list
	if browse.compose.insertMode {
		t.Error("moving through the room list should not open the composer")
	}

	// And the knob turns it off.
	off := false
	opt := sized(t, withRooms(t, New(context.Background(), apitest.Nop{},
		config.Display{OpenInInsert: &off})))
	opt, _ = press(t, opt, keyCode(tea.KeyEnter))
	opt, _ = press(t, opt, keyCode(tea.KeyEnter))
	if opt.compose.insertMode {
		t.Error("open_in_insert_mode = false should land in the message cursor")
	}
}

func TestOpeningRoomBackfillsFullHistory(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m.focus = paneRooms
	// The initial live page has landed and more history remains.
	m.timeline.hist.ready = true
	m.timeline.hist.token = "tok"
	m.timeline.hist.atStart = false
	m.timeline.hist.loading = false

	m, cmd := press(t, m, keyCode(tea.KeyEnter)) // open the room
	if m.focus != paneTimeline {
		t.Fatalf("focus = %d, want paneTimeline", m.focus)
	}
	if !m.timeline.hist.backfilling || cmd == nil {
		t.Errorf("opening a room should start a full-history backfill: backfilling=%v cmd=%v", m.timeline.hist.backfilling, cmd)
	}
}

func TestOpeningRoomBeforeInitialLoadDefersBackfill(t *testing.T) {
	t.Parallel()

	// withRooms already fired the initial load, so loadOlder cannot run yet.
	m := withRooms(t, newModel())
	m.focus = paneRooms

	m, _ = press(t, m, keyCode(tea.KeyEnter)) // open before the first page lands
	if !m.timeline.hist.backfilling {
		t.Fatalf("opening should arm backfill even before the first page: backfilling=%v", m.timeline.hist.backfilling)
	}

	// The initial page arrives with more history — the armed backfill resumes.
	m, cmd := asModel(m.Update(timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{{ID: "$1", RoomID: "!a:x", Timestamp: at(1)}},
		Next:     "tok",
	}}))
	if !m.timeline.hist.backfilling || cmd == nil {
		t.Errorf("backfill should resume once the first page lands: backfilling=%v cmd=%v", m.timeline.hist.backfilling, cmd)
	}
}

func TestOpeningFullyLoadedRoomDoesNotBackfill(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m.focus = paneRooms
	m.timeline.hist.ready = true
	m.timeline.hist.atStart = true // the whole room is already loaded

	m, _ = press(t, m, keyCode(tea.KeyEnter))
	if m.focus != paneTimeline {
		t.Fatalf("focus = %d, want paneTimeline", m.focus)
	}
	if m.timeline.hist.backfilling {
		t.Error("opening an already-complete room should not backfill")
	}
}

func TestRoomNavigationChangesSelection(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m.focus = paneRooms
	m, cmd := press(t, m, keyCode(tea.KeyDown))
	if m.roomCursor() != 1 {
		t.Fatalf("roomCursor = %d, want 1", m.roomCursor())
	}
	if cmd == nil {
		t.Error("moving to a new room should load its timeline")
	}
	room, _ := m.currentRoom()
	if room.Name != "Bravo" {
		t.Errorf("currentRoom = %q, want Bravo", room.Name)
	}
}

func TestComposer(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m.focus = paneTimeline
	m, _ = press(t, m, keyText("i")) // enter insert mode (vim-like); then type
	m, _ = press(t, m, keyText("h"))
	m, _ = press(t, m, keyText("i"))
	if m.compose.input != "hi" {
		t.Fatalf("input = %q, want hi", m.compose.input)
	}
	if !m.compose.insertMode {
		t.Error("should be in insert mode while composing")
	}
	m, _ = press(t, m, keyCode(tea.KeyBackspace))
	if m.compose.input != "h" {
		t.Fatalf("input after backspace = %q, want h", m.compose.input)
	}
	// esc leaves insert mode without leaving the pane.
	m, _ = press(t, m, keyCode(tea.KeyEsc))
	if m.compose.insertMode || m.focus != paneTimeline {
		t.Errorf("esc from insert: insertMode=%v focus=%v, want normal mode, still timeline", m.compose.insertMode, m.focus)
	}
}

func TestSubmit(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m.focus = paneTimeline
	m, _ = press(t, m, keyText("i")) // insert mode
	m, _ = press(t, m, keyText("y"))
	m, cmd := press(t, m, sendKey())
	if cmd == nil {
		t.Error("submitting non-empty input should return a send command")
	}
	if m.compose.input != "" {
		t.Errorf("input after send = %q, want empty", m.compose.input)
	}
	if m.status() != "sending…" {
		t.Errorf("status = %q, want sending", m.status())
	}
}

func TestSubmitEmptyIsNoop(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m.focus = paneTimeline
	m, _ = press(t, m, keyText("i")) // insert mode
	if _, cmd := press(t, m, sendKey()); cmd != nil {
		t.Error("submitting empty input should be a no-op")
	}
}

func TestVerificationFlow(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))

	// A request raises the overlay.
	m = update(t, m, verifyMsg{v: domain.Verification{Kind: domain.VerificationRequested, TxnID: "t1", From: "@me:x", Device: "DEV"}})
	if !m.verify.active || m.verify.stage != domain.VerificationRequested {
		t.Fatalf("request should activate the overlay: %+v", m.verify)
	}

	// Accepting sets waiting and fires the accept command.
	m, cmd := press(t, m, keyText("y"))
	if !m.verify.waiting || cmd == nil {
		t.Fatalf("accepting should wait and issue a command: waiting=%v cmd=%v", m.verify.waiting, cmd)
	}

	// The SAS step populates emoji and clears waiting.
	m = update(t, m, verifyMsg{v: domain.Verification{
		Kind: domain.VerificationSAS, TxnID: "t1",
		Emojis:   []domain.SASEmoji{{Glyph: "🐶", Name: "Dog"}},
		Decimals: []int{1234, 5678},
	}})
	if m.verify.stage != domain.VerificationSAS || len(m.verify.emojis) != 1 || m.verify.waiting {
		t.Fatalf("SAS step should show emoji and clear waiting: %+v", m.verify)
	}

	// Confirming the match fires the confirm command.
	m, cmd = press(t, m, keyText("y"))
	if !m.verify.waiting || cmd == nil {
		t.Fatalf("confirming should wait and issue a command: waiting=%v cmd=%v", m.verify.waiting, cmd)
	}

	// Done dismisses the overlay.
	m = update(t, m, verifyMsg{v: domain.Verification{Kind: domain.VerificationDone, TxnID: "t1"}})
	if m.verify.active {
		t.Errorf("done should dismiss the overlay: %+v", m.verify)
	}
	if !strings.Contains(m.status(), "verified") {
		t.Errorf("status = %q, want a verified message", m.status())
	}
}

func TestVerificationRestoredUpdatesStatus(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, verifyMsg{v: domain.Verification{Kind: domain.VerificationRestored, Reason: "restored 42 room keys from backup"}})
	if m.verify.active {
		t.Error("restore result must not reopen the overlay")
	}
	if !strings.Contains(m.status(), "42 room keys") {
		t.Errorf("status = %q, want the restore summary", m.status())
	}
}

func TestVerificationRejectCancels(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, verifyMsg{v: domain.Verification{Kind: domain.VerificationRequested, TxnID: "t1", From: "@me:x"}})
	m, cmd := press(t, m, keyText("n"))
	if m.verify.active || cmd == nil {
		t.Fatalf("rejecting should dismiss the overlay and issue a cancel: active=%v cmd=%v", m.verify.active, cmd)
	}
}

func TestVerificationOverlayCapturesKeys(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.focus = paneRooms
	before := m.roomCursor()
	m = update(t, m, verifyMsg{v: domain.Verification{Kind: domain.VerificationRequested, TxnID: "t1", From: "@me:x"}})
	// A navigation key must not leak to the room list while the overlay is up.
	m, _ = press(t, m, keyCode(tea.KeyDown))
	if m.roomCursor() != before {
		t.Errorf("overlay should capture navigation keys: roomCursor moved to %d", m.roomCursor())
	}
}

func TestVerificationIgnoresOtherTransaction(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, verifyMsg{v: domain.Verification{Kind: domain.VerificationRequested, TxnID: "t1", From: "@me:x"}})
	// A SAS step for a different transaction must not alter the shown flow.
	m = update(t, m, verifyMsg{v: domain.Verification{Kind: domain.VerificationSAS, TxnID: "other", Emojis: []domain.SASEmoji{{Glyph: "🐱"}}}})
	if m.verify.stage != domain.VerificationRequested || len(m.verify.emojis) != 0 {
		t.Errorf("a step for another transaction should be ignored: %+v", m.verify)
	}
}

func TestIncomingMessage(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel()) // current room !a:x
	m, cmd := asModel(m.Update(incomingMsg{message: domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", Body: "hey"}}))
	if len(m.timeline.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(m.timeline.messages))
	}
	if cmd == nil {
		t.Error("handleIncoming should re-issue the listen command")
	}
	// A duplicate event ID is ignored.
	m = update(t, m, incomingMsg{message: domain.Message{ID: "$1", RoomID: "!a:x", Body: "hey"}})
	if len(m.timeline.messages) != 1 {
		t.Error("duplicate event ID should be de-duplicated")
	}
	// A message for another room is ignored.
	m = update(t, m, incomingMsg{message: domain.Message{ID: "$2", RoomID: "!other:x", Body: "nope"}})
	if len(m.timeline.messages) != 1 {
		t.Error("message for another room should be ignored")
	}
}

func TestTimelineMergesByTimestamp(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel()) // current room !a:x
	page := domain.TimelinePage{
		Messages: []domain.Message{
			{ID: "$1", RoomID: "!a:x", Body: "first", Timestamp: at(1)},
			{ID: "$2", RoomID: "!a:x", Body: "second", Timestamp: at(2)},
		},
		Next: "tok",
	}
	m = update(t, m, timelineMsg{roomID: "!a:x", page: page})
	if len(m.timeline.messages) != 2 || m.timeline.messages[0].Body != "first" {
		t.Fatalf("messages = %+v, want [first second]", m.timeline.messages)
	}
	if m.timeline.hist.atStart || !m.timeline.hist.ready {
		t.Error("want atStart=false, histReady=true after a page with a Next token")
	}

	// An older page merges ahead of the rest by timestamp; Next="" marks start.
	older := domain.TimelinePage{Messages: []domain.Message{{ID: "$0", RoomID: "!a:x", Body: "zeroth", Timestamp: at(0)}}}
	m = update(t, m, timelineMsg{roomID: "!a:x", page: older})
	if len(m.timeline.messages) != 3 || m.timeline.messages[0].Body != "zeroth" {
		t.Fatalf("messages = %+v, want zeroth ordered first", m.timeline.messages)
	}
	if !m.timeline.hist.atStart {
		t.Error("atStart should be true once Next is empty")
	}
	m.focus = paneTimeline
	if _, cmd := press(t, m, keyCode(tea.KeyPgUp)); cmd != nil {
		t.Error("pgup at start of room should be a no-op")
	}
}

func TestCachedTimelineShownThenReconciled(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel()) // current room !a:x

	// The cache preview shows instantly, but pgup isn't armed (no token yet).
	m = update(t, m, cachedTimelineMsg{roomID: "!a:x", messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Body: "cached", Timestamp: at(1)},
	}})
	if len(m.timeline.messages) != 1 || m.timeline.messages[0].Body != "cached" {
		t.Fatalf("messages = %+v, want [cached]", m.timeline.messages)
	}
	m.focus = paneTimeline
	if _, cmd := press(t, m, keyCode(tea.KeyPgUp)); cmd != nil {
		t.Error("pgup before the live fetch (no token) should be a no-op")
	}

	// The live page reconciles: dedups the cached event, adds a newer one, arms pgup.
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{
			{ID: "$1", RoomID: "!a:x", Body: "cached", Timestamp: at(1)},
			{ID: "$2", RoomID: "!a:x", Body: "live", Timestamp: at(2)},
		},
		Next: "tok",
	}})
	if len(m.timeline.messages) != 2 || m.timeline.messages[1].Body != "live" {
		t.Fatalf("messages = %+v, want [cached live] deduped", m.timeline.messages)
	}
	if !m.timeline.hist.ready {
		t.Error("histReady should be set after the live fetch")
	}
}

func TestHomeStartsBackfill(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel())
	m.focus = paneTimeline
	m.timeline.hist.ready = true
	m.timeline.hist.token = "tok"
	m.timeline.hist.loading = false // the initial load has settled
	m, cmd := press(t, m, keyCode(tea.KeyHome))
	if !m.timeline.hist.backfilling || cmd == nil {
		t.Errorf("home should start a backfill: backfilling=%v cmd=%v", m.timeline.hist.backfilling, cmd)
	}
}

func TestBackfillChainsUntilStart(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel()) // current room !a:x
	m.timeline.hist.backfilling = true

	// A page that still has older history chains another fetch.
	m, cmd := asModel(m.Update(timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{{ID: "$1", RoomID: "!a:x", Timestamp: at(1)}},
		Next:     "tok",
	}}))
	if cmd == nil || !m.timeline.hist.backfilling {
		t.Fatalf("backfill should continue while history remains: cmd=%v backfilling=%v", cmd, m.timeline.hist.backfilling)
	}
	if !strings.Contains(m.status(), "full history") {
		t.Errorf("status = %q, want backfill progress", m.status())
	}

	// The final page (no Next token) ends the backfill at the room start.
	m, _ = asModel(m.Update(timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{{ID: "$0", RoomID: "!a:x", Timestamp: at(0)}},
	}}))
	if m.timeline.hist.backfilling {
		t.Error("backfill should stop once the room start is reached")
	}
	if !m.timeline.hist.atStart {
		t.Error("atStart should be set at the room start")
	}
}

func TestTimelineForOtherRoomIgnored(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel()) // current room !a:x
	page := domain.TimelinePage{Messages: []domain.Message{{ID: "$z", RoomID: "!b:x", Body: "elsewhere"}}}
	m = update(t, m, timelineMsg{roomID: "!b:x", page: page})
	if len(m.timeline.messages) != 0 {
		t.Error("a page for a non-current room should be ignored")
	}
	m = update(t, m, cachedTimelineMsg{roomID: "!b:x", messages: []domain.Message{{ID: "$y", RoomID: "!b:x"}}})
	if len(m.timeline.messages) != 0 {
		t.Error("a cached preview for a non-current room should be ignored")
	}
}

func TestRedactedMessageRendersDeleted(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x, comfortable width
	m = loadPage(t, m, []domain.Message{{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "secret", Timestamp: at(1), Redacted: true}})
	rows := strings.Join(m.layoutRows(), "\n")
	if !strings.Contains(rows, redactedBody) {
		t.Errorf("redacted message not shown as %q:\n%s", redactedBody, rows)
	}
	if strings.Contains(rows, "secret") {
		t.Errorf("redacted message leaked its original body:\n%s", rows)
	}
}

func TestIncomingRedactionFoldsOntoMessage(t *testing.T) {
	t.Parallel()

	m := withRooms(t, newModel()) // current room !a:x
	m = loadPage(t, m, []domain.Message{msgAt("$1", "secret", 1)})
	// A live redaction arrives as a synthetic message (only ID + Redacted).
	m = update(t, m, incomingMsg{message: domain.Message{ID: "$1", RoomID: "!a:x", Redacted: true}})
	if len(m.timeline.messages) != 1 {
		t.Fatalf("redaction added a row instead of folding: %+v", m.timeline.messages)
	}
	// The row stays, the words go.
	if got := m.timeline.messages[0]; !got.Redacted || got.Timestamp.IsZero() {
		t.Errorf("fold lost original data: %+v", got)
	}
	if got := m.timeline.messages[0]; got.Body != "" {
		t.Errorf("the deleted words are still in the timeline: %q", got.Body)
	}
}

func TestEditedMessageRendersMarker(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x, comfortable width
	m = loadPage(t, m, []domain.Message{{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "new body", Timestamp: at(1), Edited: true}})
	rows := strings.Join(m.layoutRows(), "\n")
	if !strings.Contains(rows, editedMarker) {
		t.Errorf("edited message missing %q marker:\n%s", editedMarker, rows)
	}
	if !strings.Contains(rows, "new body") {
		t.Errorf("edited body missing:\n%s", rows)
	}
}

func TestIncomingEditFoldsOntoMessage(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x
	m = loadPage(t, m, []domain.Message{msgAt("$1", "old", 1)})
	// A live edit arrives carrying the target ID + replacement body + a later ts.
	m = update(t, m, incomingMsg{message: domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "new", Timestamp: at(9), Edited: true}})
	if len(m.timeline.messages) != 1 {
		t.Fatalf("edit added a row instead of folding: %+v", m.timeline.messages)
	}
	if got := m.timeline.messages[0]; !got.Edited || got.Body != "new" || !got.Timestamp.Equal(at(1)) {
		t.Errorf("fold wrong (want edited, body=new, ts kept at original): %+v", got)
	}
}

func TestReactionsRender(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{msgAt("$1", "hi", 1)},
		Reactions: []domain.Reaction{
			{ID: "$r1", RoomID: "!a:x", Target: "$1", Sender: "@b:x", Key: "👍"},
			{ID: "$r2", RoomID: "!a:x", Target: "$1", Sender: "@c:x", Key: "👍"},
		},
	}})
	rows := strings.Join(m.layoutRows(), "\n")
	if !strings.Contains(rows, "👍 2") {
		t.Errorf("aggregated reaction chip missing:\n%s", rows)
	}
}

func TestReactionUpdateAddsThenRemoves(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x
	m = loadPage(t, m, []domain.Message{msgAt("$1", "hi", 1)})

	// A live reaction add lands under its target.
	add := domain.ReactionUpdate{Reaction: domain.Reaction{ID: "$r1", RoomID: "!a:x", Target: "$1", Sender: "@b:x", Key: "🎉"}}
	m = update(t, m, reactionUpdateMsg{u: add})
	if got := domain.AggregateReactions(m.timeline.reactions["$1"], ""); len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("after add: %+v, want one 🎉", got)
	}
	// The same reaction re-sent is de-duplicated by event ID.
	m = update(t, m, reactionUpdateMsg{u: add})
	if got := domain.AggregateReactions(m.timeline.reactions["$1"], ""); got[0].Count != 1 {
		t.Errorf("re-send double-counted: %+v", got)
	}
	// An un-react removes it.
	rm := domain.ReactionUpdate{Removed: true, Reaction: domain.Reaction{ID: "$r1", RoomID: "!a:x", Target: "$1"}}
	m = update(t, m, reactionUpdateMsg{u: rm})
	if got := m.timeline.reactions["$1"]; len(got) != 0 {
		t.Errorf("after un-react: %+v, want none", got)
	}
}

func TestReactionForOtherRoomIgnored(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x
	add := domain.ReactionUpdate{Reaction: domain.Reaction{ID: "$r1", RoomID: "!b:x", Target: "$1", Sender: "@b:x", Key: "👍"}}
	m = update(t, m, reactionUpdateMsg{u: add})
	if len(m.timeline.reactions["$1"]) != 0 {
		t.Error("a reaction for a non-current room should be ignored by the open timeline")
	}
}

func TestTimelineMessageSelection(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x
	m = loadPage(t, m, []domain.Message{
		msgAt("$1", "one", 1),
		msgAt("$2", "two", 2),
		msgAt("$3", "three", 3),
	})
	m.focus = paneTimeline // normal (browse) mode by default

	if got := m.selectedID(); got != "$3" {
		t.Fatalf("default selection = %q, want newest $3", got)
	}
	m, _ = press(t, m, keyText("k")) // up = older
	m, _ = press(t, m, keyText("k"))
	if got := m.selectedID(); got != "$1" {
		t.Errorf("after k k = %q, want $1", got)
	}
	m, _ = press(t, m, keyText("j")) // down = newer
	if got := m.selectedID(); got != "$2" {
		t.Errorf("after j = %q, want $2", got)
	}
	m, _ = press(t, m, keyText("G")) // newest
	if got := m.selectedID(); got != "$3" {
		t.Errorf("after G = %q, want $3", got)
	}
	// "gg" — two presses, the first of which is held while it might still become one.
	m, _ = press(t, m, keyText("g"))
	m, _ = press(t, m, keyText("g")) // oldest
	if got := m.selectedID(); got != "$1" {
		t.Errorf("after g = %q, want $1", got)
	}
}

// recordingReact records the arguments of the last SendReaction call.
type recordingReact struct {
	apitest.Nop
	room   domain.RoomID
	target domain.EventID
	key    string
	called bool
}

func (r *recordingReact) SendReaction(_ context.Context, room domain.RoomID, target domain.EventID, key string) error {
	r.room, r.target, r.key, r.called = room, target, key, true
	return nil
}

func TestReactFlowTargetsSelectedMessage(t *testing.T) {
	t.Parallel()

	rb := &recordingReact{}
	m := sized(t, withRooms(t, New(context.Background(), rb, config.Display{})))
	m = loadPage(t, m, []domain.Message{
		msgAt("$1", "one", 1),
		msgAt("$2", "two", 2),
	})
	m.focus = paneTimeline

	m, _ = press(t, m, keyText("k")) // select the older message $1
	m, _ = press(t, m, keyText("e")) // open the react prompt
	if !m.compose.reacting {
		t.Fatal("r should open the react prompt")
	}
	m, _ = press(t, m, keyText("👍")) // type the emoji
	if m.compose.reactInput != "👍" {
		t.Fatalf("reactInput = %q, want 👍", m.compose.reactInput)
	}
	m, cmd := press(t, m, keyCode(tea.KeyEnter)) // send
	if m.compose.reacting {
		t.Error("react prompt should close after enter")
	}
	if cmd == nil {
		t.Fatal("enter with a reaction should return a send command")
	}
	runCmd(t, cmd) // execute the backend call
	if !rb.called || rb.room != "!a:x" || rb.target != "$1" || rb.key != "👍" {
		t.Errorf("SendReaction(room=%q target=%q key=%q called=%v), want !a:x/$1/👍", rb.room, rb.target, rb.key, rb.called)
	}
}

func TestReactPaletteAndShortcodes(t *testing.T) {
	t.Parallel()

	newModelWith := func() (*recordingReact, Model) {
		rb := &recordingReact{}
		m := sized(t, withRooms(t, New(context.Background(), rb, config.Display{})))
		m = loadPage(t, m, []domain.Message{msgAt("$1", "hi", 1)})
		m.focus = paneTimeline
		return rb, m
	}

	// A digit pressed first sends the palette emoji immediately (two keystrokes).
	rb, m := newModelWith()
	m, _ = press(t, m, keyText("e"))
	m, cmd := press(t, m, keyText("3"))
	if m.compose.reacting {
		t.Error("a palette pick should send and close the prompt")
	}
	if cmd == nil {
		t.Fatal("palette pick should return a send command")
	}
	runCmd(t, cmd)
	if rb.key != "😂" || rb.target != "$1" {
		t.Errorf("palette 3 sent key=%q target=%q, want 😂/$1", rb.key, rb.target)
	}

	// A :shortcode: is resolved to its emoji on enter.
	rb, m = newModelWith()
	m, _ = press(t, m, keyText("e"))
	for _, r := range ":fire:" {
		m, _ = press(t, m, keyText(string(r)))
	}
	_, cmd = press(t, m, keyCode(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("shortcode should return a send command")
	}
	runCmd(t, cmd)
	if rb.key != "🔥" {
		t.Errorf("shortcode :fire: sent %q, want 🔥", rb.key)
	}
}

func TestFrequentPaletteApplied(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // current room !a:x, default (room) scope
	m = loadPage(t, m, []domain.Message{msgAt("$1", "hi", 1)})
	// The palette leads with the most-used, then static defaults fill it.
	m = update(t, m, emojiScoresMsg{roomID: "!a:x", kind: domain.EmojiReaction,
		scores: map[string]int{"🔥": 30, "🎉": 20}})
	if len(m.glyphs.palette) != paletteSize {
		t.Fatalf("palette size = %d, want %d", len(m.glyphs.palette), paletteSize)
	}
	if m.glyphs.palette[0] != "🔥" || m.glyphs.palette[1] != "🎉" {
		t.Errorf("palette leads with %v, want 🔥,🎉", m.glyphs.palette[:2])
	}
	// And the browser grid opens in the same order — one ranking, three ways in.
	if grid := m.rankedEmoji(domain.EmojiReaction); grid[0] != "🔥" || grid[1] != "🎉" {
		t.Errorf("the grid leads with %v, want the palette's order", grid[:2])
	}
	// Key 1 now reacts with the top frequent emoji.
	if n, ok := pickOf(m.keys.lookup("1", scopeReact)); !ok || m.glyphs.palette[n] != "🔥" {
		t.Errorf("key 1 picks slot %d,%v, want the 🔥 slot", n, ok)
	}
}

func TestStaticScopeAndCustomStatic(t *testing.T) {
	t.Parallel()

	m := New(context.Background(), apitest.Nop{}, config.Display{
		Reactions: config.Reactions{Scope: "static", Static: []string{"✅", "🚀"}},
	})
	if m.glyphs.scope != "static" {
		t.Errorf("scope = %q, want static", m.glyphs.scope)
	}
	if len(m.glyphs.palette) != 2 || m.glyphs.palette[0] != "✅" || m.glyphs.palette[1] != "🚀" {
		t.Errorf("custom static palette = %v, want [✅ 🚀]", m.glyphs.palette)
	}
	// Static scope ignores usage everywhere, grid included.
	scored := update(t, sized(t, withRooms(t, m)), emojiScoresMsg{roomID: "!a:x",
		kind: domain.EmojiReaction, scores: map[string]int{"🔥": 300}})
	if got := scored.rankedEmoji(domain.EmojiReaction); got[0] == "🔥" {
		t.Error("static scope let usage reorder the grid")
	}
	if scored.glyphs.palette[0] != "✅" {
		t.Errorf("static palette = %v, want the configured order kept", scored.glyphs.palette)
	}
}

func TestResolveReaction(t *testing.T) {
	t.Parallel()
	if got, _ := newModel().resolveReaction(":+1:"); got != "👍" {
		t.Errorf(":+1: = %q, want 👍", got)
	}
	if got, _ := newModel().resolveReaction("  :TADA:  "); got != "🎉" {
		t.Errorf("trimmed, upper :TADA: = %q, want 🎉", got)
	}
	if got, _ := newModel().resolveReaction("👍"); got != "👍" {
		t.Errorf("raw emoji should pass through, got %q", got)
	}
	// A misspelled shortcode is refused with the nearest name, not sent as text.
	got, ok := newModel().resolveReaction(":dance:")
	if ok {
		t.Errorf(":dance: resolved to %q, want a refusal — the name is :dancer:", got)
	}
	if !strings.Contains(got, ":dancer:") {
		t.Errorf("refusal = %q, want it to offer the nearest name", got)
	}
	// Text that merely contains a colon is still text.
	if key, textOK := newModel().resolveReaction("9:30 sharp"); !textOK || key != "9:30 sharp" {
		t.Errorf("text reaction = %q (ok=%v), want it sent verbatim", key, textOK)
	}
}

func TestReactPromptEscCancels(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = loadPage(t, m, []domain.Message{msgAt("$1", "hi", 1)})
	m.focus = paneTimeline
	m, _ = press(t, m, keyText("e"))
	m, _ = press(t, m, keyText("👍"))
	m, cmd := press(t, m, keyCode(tea.KeyEsc))
	if m.compose.reacting || m.compose.reactInput != "" {
		t.Errorf("esc should cancel the react prompt: reacting=%v input=%q", m.compose.reacting, m.compose.reactInput)
	}
	if cmd != nil {
		t.Error("canceling should not send a reaction")
	}
}

func TestReplyPreviewRenders(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = loadPage(t, m, []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "original text", Timestamp: at(1)},
		// The reply reaches back past the row above, which is when a quote is drawn.
		{ID: "$1b", RoomID: "!a:x", Sender: "@alice:x", SenderName: "Alice", Body: "and another", Timestamp: at(2)},
		{ID: "$2", RoomID: "!a:x", Sender: "@bob:x", SenderName: "Bob", Body: "the reply", Timestamp: at(3), ReplyTo: "$1"},
	})
	rows := strings.Join(m.layoutRows(), "\n")
	if !strings.Contains(rows, "↪") || !strings.Contains(rows, "original text") {
		t.Errorf("reply preview (quote of the target) missing:\n%s", rows)
	}
	if !strings.Contains(rows, "the reply") {
		t.Errorf("reply body missing:\n%s", rows)
	}
}

// recordingReply records the arguments of the last SendReply call.
type recordingReply struct {
	apitest.Nop
	room     domain.RoomID
	target   domain.EventID
	body     string
	mentions []domain.Mention
	called   bool
}

func (r *recordingReply) Send(_ context.Context, room domain.RoomID, draft domain.Draft) error {
	r.room, r.target, r.body, r.called = room, draft.ReplyTo, draft.Body, true
	r.mentions = draft.LiveMentions()
	return nil
}

func TestReplyFlowTargetsSelectedMessage(t *testing.T) {
	t.Parallel()

	rb := &recordingReply{}
	m := sized(t, withRooms(t, New(context.Background(), rb, config.Display{})))
	m = loadPage(t, m, []domain.Message{
		msgAt("$1", "one", 1),
		msgAt("$2", "two", 2),
	})
	m.focus = paneTimeline

	m, _ = press(t, m, keyText("k")) // select the older message $1
	m, _ = press(t, m, keyText("r")) // r opens a reply to it
	if m.compose.replyTo != "$1" || !m.compose.insertMode {
		t.Fatalf("reply setup: replyTo=%q insertMode=%v, want $1 / true", m.compose.replyTo, m.compose.insertMode)
	}
	m, _ = press(t, m, keyText("h"))
	m, _ = press(t, m, keyText("i"))
	m, cmd := press(t, m, sendKey()) // send
	if m.compose.replyTo != "" {
		t.Error("replyTo should clear after sending")
	}
	if cmd == nil {
		t.Fatal("sending a reply should return a command")
	}
	m = deliver(t, m, cmd)
	if !rb.called || rb.room != "!a:x" || rb.target != "$1" || rb.body != "hi" {
		t.Errorf("Send(room=%q replyTo=%q body=%q called=%v), want !a:x/$1/hi", rb.room, rb.target, rb.body, rb.called)
	}
}

func TestReplyEscCancels(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = loadPage(t, m, []domain.Message{msgAt("$1", "hi", 1)})
	m.focus = paneTimeline
	m, _ = press(t, m, keyCode(tea.KeyEnter)) // reply to newest, enters insert
	m, _ = press(t, m, keyCode(tea.KeyEsc))   // cancel
	if m.compose.replyTo != "" || m.compose.insertMode {
		t.Errorf("esc should cancel the reply: replyTo=%q insertMode=%v", m.compose.replyTo, m.compose.insertMode)
	}
}

// jumpMatch wraps past either end and says so; with nothing selected the cursor
// is the newest message, so "next" only ever finds anything by wrapping.
func TestJumpMatch(t *testing.T) {
	t.Parallel()

	file := &domain.Media{Type: domain.MediaImage, Name: "a.jpg", Mime: "image/jpeg", Width: 40, Height: 40}
	mention := func(id domain.EventID, sec int) domain.Message {
		msg := msgAt(id, string(id), sec)
		msg.Mentioned = true
		return msg
	}
	attached := func(id domain.EventID, sec int) domain.Message {
		msg := msgAt(id, string(id), sec)
		msg.Media = file
		return msg
	}
	type step struct {
		key    string
		want   domain.EventID
		status string
	}
	for _, tc := range []struct {
		name  string
		msgs  []domain.Message
		steps []step
	}{
		{"mentions walk and wrap", []domain.Message{mention("$1", 1), msgAt("$2", "", 2), mention("$3", 3), msgAt("$4", "", 4)}, []step{
			{"M", "$3", ""}, {"M", "$1", ""}, {"M", "$3", "continuing from the newest"}, {"m", "$1", ""},
		}},
		{"next from the newest wraps", []domain.Message{mention("$1", 1), msgAt("$2", "", 2)}, []step{
			{"m", "$1", "continuing from the oldest"},
		}},
		{"no mentions says so", []domain.Message{msgAt("$1", "", 1), msgAt("$2", "", 2)}, []step{
			{"m", "$2", "no mentions in this room"},
		}},
		{"lone match under the cursor is a wrap", []domain.Message{msgAt("$1", "", 1), mention("$2", 2)}, []step{
			{"m", "$2", "continuing from"},
		}},
		{"attachments", []domain.Message{attached("$1", 1), msgAt("$2", "", 2), attached("$3", 3), msgAt("$4", "", 4)}, []step{
			{"F", "$3", ""}, {"F", "$1", ""}, {"f", "$3", ""},
		}},
		{"no attachments says so", []domain.Message{msgAt("$1", "", 1)}, []step{
			{"f", "$1", "no attachments in this room"},
		}},
	} {
		m := loadPage(t, sized(t, withRooms(t, newModel())), tc.msgs)
		m.focus = paneTimeline
		for i, st := range tc.steps {
			m, _ = press(t, m, keyText(st.key))
			if got := m.selectedID(); got != st.want {
				t.Errorf("%s: step %d %s = %q, want %q", tc.name, i, st.key, got, st.want)
			}
			if st.status != "" && !strings.Contains(m.st.event, st.status) {
				t.Errorf("%s: step %d status = %q, want %q", tc.name, i, m.st.event, st.status)
			}
		}
	}
}

func TestMediaChipRenders(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = loadPage(t, m, []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "cat.jpg", Timestamp: at(1),
		Media: &domain.Media{Type: domain.MediaImage, Name: "cat.jpg", Width: 800, Height: 600, Size: 12345},
	}})
	rows := strings.Join(m.layoutRows(), "\n")
	if !strings.Contains(rows, "🖼") || !strings.Contains(rows, "cat.jpg") || !strings.Contains(rows, "800×600") {
		t.Errorf("media chip (icon · name · dims) missing:\n%s", rows)
	}
}

// A captioned attachment draws the words, then the chip beneath them.
func TestCaptionedAttachmentDrawsWordsThenChip(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = loadPage(t, m, []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "look at this sunset", Timestamp: at(1),
		Media: &domain.Media{Type: domain.MediaImage, Name: "IMG_1.jpg", Width: 800, Height: 600},
	}})

	rows := m.layoutRows()
	words, chip := -1, -1
	for i, r := range rows {
		if strings.Contains(r, "look at this sunset") {
			words = i
		}
		if strings.Contains(r, "IMG_1.jpg") {
			chip = i
		}
	}
	joined := strings.Join(rows, "\n")
	if words < 0 {
		t.Errorf("the caption never rendered:\n%s", joined)
	}
	if chip < 0 {
		t.Errorf("the attachment lost its chip:\n%s", joined)
	}
	if words >= 0 && chip >= 0 && chip <= words {
		t.Errorf("the chip should hang beneath the words (words at %d, chip at %d):\n%s", words, chip, joined)
	}
}

// A body that only repeats the file name is not a caption: one chip.
func TestUncaptionedAttachmentDrawsOneChip(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = loadPage(t, m, []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@a:x", Body: "cat.jpg", Timestamp: at(1),
		Media: &domain.Media{Type: domain.MediaImage, Name: "cat.jpg", Width: 800, Height: 600},
	}})

	n := 0
	for _, r := range m.layoutRows() {
		if strings.Contains(r, "cat.jpg") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want one chip row, got %d:\n%s", n, strings.Join(m.layoutRows(), "\n"))
	}
}

func TestInlineModeTriggersLoadAndRenders(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, New(context.Background(), apitest.Nop{}, config.Display{Media: config.Media{Mode: "inline"}})))
	m.focus = paneTimeline // pictures are only fetched once a room is open
	m = loadPage(t, m, []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@a:x", Timestamp: at(1),
		Media: &domain.Media{Type: domain.MediaImage, Name: "cat.jpg", Width: 4, Height: 4},
	}})
	// Inline mode marks the image loading after the timeline lands.
	if !m.pics.imageLoading["$1"] {
		t.Fatal("inline mode should start loading the image")
	}
	// When the load finishes, its rows render beneath the chip.
	m = update(t, m, imageLoadedMsg{eventID: "$1", rows: []string{"\x1b[48;2;1;2;3m▄row1", "\x1b[48;2;1;2;3m▄row2"}})
	if m.pics.imageLoading["$1"] {
		t.Error("imageLoading should clear once the load returns")
	}
	rows := strings.Join(m.layoutRows(), "\n")
	if !strings.Contains(rows, "▄row1") || !strings.Contains(rows, "▄row2") {
		t.Errorf("inline image rows not rendered:\n%s", rows)
	}
}

func TestPlaceholderModeDoesNotLoad(t *testing.T) {
	t.Parallel()

	disp := config.Display{Media: config.Media{Mode: "placeholder"}}
	m := sized(t, withRooms(t, New(context.Background(), apitest.Nop{}, disp)))
	m = loadPage(t, m, []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@a:x", Timestamp: at(1),
		Media: &domain.Media{Type: domain.MediaImage, Name: "cat.jpg", Width: 4, Height: 4},
	}})
	if len(m.pics.imageLoading) != 0 {
		t.Errorf("placeholder mode should not load images: %+v", m.pics.imageLoading)
	}
}

// With nothing configured, attachments show a chip and nothing is fetched.
func TestDefaultModeShowsAChip(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.focus = paneTimeline
	m = loadPage(t, m, []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@a:x", Timestamp: at(1),
		Media: &domain.Media{Type: domain.MediaImage, Name: "cat.jpg", Width: 4, Height: 4},
	}})
	if m.pics.graphics != graphicsNone {
		t.Errorf("graphics = %v with nothing configured, want a chip and no drawing", m.pics.graphics)
	}
	if len(m.pics.imageLoading) != 0 {
		t.Errorf("a client showing chips fetched %d picture(s)", len(m.pics.imageLoading))
	}
	if rows := strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "cat.jpg") {
		t.Errorf("the attachment is not named anywhere:\n%s", rows)
	}
}

func TestMentionRendersResolvedName(t *testing.T) {
	t.Parallel()

	// An identity alias: mentions of @alice:x collapse to "Ally".
	disp := config.Display{Identities: []config.Identity{{Alias: "Ally", MXIDs: []string{"@alice:x"}}}}
	m := sized(t, withRooms(t, New(context.Background(), apitest.Nop{}, disp)))
	m = loadPage(t, m, []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@bob:x", Body: "hey Alice Smith, hi", Timestamp: at(1),
		Mentions: []domain.Mention{{UserID: "@alice:x", Name: "Alice Smith"}},
	}})
	rows := strings.Join(m.layoutRows(), "\n")
	if !strings.Contains(rows, "Ally") {
		t.Errorf("mention should resolve to the alias 'Ally':\n%s", rows)
	}
	if strings.Contains(rows, "Alice Smith") {
		t.Errorf("raw pill name should be replaced by the resolved name:\n%s", rows)
	}
}

func TestResolveMentionsColorInRoomVsAccent(t *testing.T) {
	t.Parallel()

	m := newModel() // no identities, no name rules → names pass through unchanged
	inRoom := lipgloss.Color("#ff0000")
	colors := map[string]color.Color{"@alice:x": inRoom} // alice has posted → in room
	body, spans := m.resolveMentions("hi Alice and Ghost", []domain.Mention{
		{UserID: "@alice:x", Name: "Alice"},
		{UserID: "@ghost:x", Name: "Ghost"},
	}, colors, "!a:x")
	if body != "hi Alice and Ghost" {
		t.Errorf("body changed unexpectedly: %q", body)
	}
	var aliceC, ghostC color.Color
	for _, s := range spans {
		switch s.name {
		case "Alice":
			aliceC = s.c
		case "Ghost":
			ghostC = s.c
		}
	}
	if aliceC != inRoom {
		t.Errorf("in-room mention should use their hue, got %v", aliceC)
	}
	if ghostC != m.theme.Palette.Accent {
		t.Errorf("unknown mention should use the accent, got %v want %v", ghostC, m.theme.Palette.Accent)
	}
}

func TestErrorsReachTheStatusLine(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		m    Model
		msg  tea.Msg
		want string
	}{
		{"rooms", newModel(), roomsMsg{err: context.Canceled}, "error:"},
		{"history", withRooms(t, newModel()), timelineMsg{roomID: "!a:x", err: context.Canceled}, "history error:"},
		{"send", newModel(), sentMsg{err: context.Canceled}, "send failed:"},
		{"sync", newModel(), syncEndedMsg{err: context.Canceled}, "sync stopped"},
	} {
		if got := update(t, tc.m, tc.msg).status(); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: status = %q, want prefix %q", tc.name, got, tc.want)
		}
	}
	if got := update(t, newModel(), sentMsg{}).status(); got != "" {
		t.Errorf("status after an ok send = %q, want empty", got)
	}
}

func TestQuitKeys(t *testing.T) {
	t.Parallel()

	if _, cmd := press(t, newModel(), keyText("q")); cmd == nil {
		t.Error("q in rail should quit")
	}
	// interrupt has no key by default: ctrl+c is the terminal's copy key.
	if act := newModel().keys.lookup("ctrl+c", scopeGlobal); act == actInterrupt {
		t.Error("ctrl+c interrupts by default; interrupt should have no default key")
	}
}

func TestViewFrame(t *testing.T) {
	t.Parallel()

	// Not ready yet → starting placeholder.
	if got := newModel().View().Content; !strings.Contains(got, "starting") {
		t.Errorf("unsized view = %q, want starting placeholder", got)
	}

	// Too small → resize hint.
	small := update(t, newModel(), tea.WindowSizeMsg{Width: 20, Height: 8})
	if got := small.View().Content; !strings.Contains(got, "too small") {
		t.Errorf("tiny view = %q, want too-small hint", got)
	}

	// Sized + rooms → three-pane frame with rail title and room names.
	m := sized(t, withRooms(t, newModel()))
	view := m.View()
	if !view.AltScreen {
		t.Error("view should use the alt screen")
	}
	for _, want := range []string{"SPACES", "Alpha", "Bravo"} {
		if !strings.Contains(view.Content, want) {
			t.Errorf("frame view missing %q", want)
		}
	}
}

func TestTimelineScrolls(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // height 30 → msgAreaRows 24
	m.focus = paneTimeline
	m = m.setMessages(manyMessages(50))
	rows := m.msgAreaRows()
	maxScroll := len(m.layoutRows()) - rows // rows, not messages (date divider included)

	m, _ = press(t, m, keyCode(tea.KeyPgUp))
	if m.timeline.scroll != rows {
		t.Fatalf("scroll after pgup = %d, want one page (%d)", m.timeline.scroll, rows)
	}
	m, _ = press(t, m, keyCode(tea.KeyPgUp)) // clamps at the oldest loaded
	if m.timeline.scroll != maxScroll {
		t.Fatalf("scroll clamped = %d, want %d", m.timeline.scroll, maxScroll)
	}
	m, _ = press(t, m, keyCode(tea.KeyEnd))
	if m.timeline.scroll != 0 {
		t.Errorf("end should jump to the latest, scroll = %d", m.timeline.scroll)
	}
}

func TestScrollAnchorsOnIncoming(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.focus = paneTimeline
	m = m.setMessages(manyMessages(50))
	m.timeline.scroll = 10

	// A new live message shifts the bottom; the scrolled view stays anchored.
	m = update(t, m, incomingMsg{message: domain.Message{ID: "$new", RoomID: "!a:x", Timestamp: at(1000)}})
	if m.timeline.scroll != 11 {
		t.Errorf("scroll after incoming = %d, want 11 (anchored)", m.timeline.scroll)
	}
}

func TestMessageWrapsToWidth(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", Body: strings.Repeat("word ", 80), Timestamp: at(1)},
	})
	rows := m.layoutRows()

	body := 0
	width := m.contentWidth()
	for _, r := range rows {
		if !strings.Contains(r, "─") {
			body++
		}
		if w := ansi.StringWidth(r); w > width {
			t.Errorf("row exceeds content width %d: %d cells in %q", width, w, r)
		}
	}
	if body < 2 {
		t.Errorf("a long message should wrap onto multiple rows, got %d body rows", body)
	}
}

func TestDateDividers(t *testing.T) {
	t.Parallel()

	countDividers := func(rows []string) (dividers, total int) {
		for _, r := range rows {
			if strings.Contains(r, "─") {
				dividers++
			}
		}
		return dividers, len(rows)
	}

	day1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 1, 2, 10, 0, 0, 0, time.Local)

	m := sized(t, withRooms(t, newModel()))

	// Two messages on different days → a divider before each day.
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", Body: "first", Timestamp: day1},
		{ID: "$2", RoomID: "!a:x", Sender: "@bob:x", Body: "second", Timestamp: day2},
	})
	if d, total := countDividers(m.layoutRows()); d != 2 || total != 4 {
		t.Errorf("cross-day: dividers=%d total=%d, want 2 and 4", d, total)
	}

	// Two messages on the same day → a single divider.
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", Body: "first", Timestamp: day1},
		{ID: "$2", RoomID: "!a:x", Sender: "@bob:x", Body: "second", Timestamp: day1.Add(time.Hour)},
	})
	if d, total := countDividers(m.layoutRows()); d != 1 || total != 3 {
		t.Errorf("same-day: dividers=%d total=%d, want 1 and 3", d, total)
	}
}

func TestNameProcessing(t *testing.T) {
	t.Parallel()

	// Name rules come from the room's own space: !a:x is in Work, !b:x in none.
	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	m.prefs.display = config.Display{
		MaxNameLength: 8,
		SpaceRules:    []config.SpaceRule{{Space: "Work", FirstNameOnly: true}},
	}
	inWork := domain.Message{RoomID: "!a:x", Sender: "@x:x", SenderName: "Rowan Blackwood"}

	// First-name-only: full display name collapses to its first token.
	if got := m.processedName(inWork); got != "Rowan" {
		t.Errorf("first-name-only = %q, want Rowan", got)
	}
	// A rule on the rail group the cursor is on does not apply.
	railKey := m.rail.key()
	onTheRailGroup := m
	onTheRailGroup.prefs.display.SpaceRules = []config.SpaceRule{{Space: railKey, FirstNameOnly: true}}
	if got := onTheRailGroup.processedName(inWork); got != "Rowan B…" {
		t.Errorf("a rule on the rail group %q shortened %q — only the room's space may", railKey, got)
	}
	// A room in no space takes no space rule.
	outside := domain.Message{RoomID: "!b:x", Sender: "@x:x", SenderName: "Rowan Blackwood"}
	if got := m.processedName(outside); got != "Rowan B…" {
		t.Errorf("no space = %q, want the uncapped name with only the length cap", got)
	}
	// Max length caps the width (ellipsis included).
	got := m.processedName(domain.Message{RoomID: "!a:x", Sender: "@y:y", SenderName: "Wilhelmina Ashcroft"})
	if w := ansi.StringWidth(got); w != 8 {
		t.Errorf("truncated width = %d (%q), want 8", w, got)
	}
	// An alias supplies the name and the space rule still shapes it.
	m.prefs.identities = map[string]resolvedIdentity{"@p:x": {alias: "Dana Levi"}}
	if got := m.processedName(domain.Message{RoomID: "!a:x", Sender: "@p:x", SenderName: "+972500000"}); got != "Dana" {
		t.Errorf("aliased name = %q, want the space rule applied to it too", got)
	}
	// The same answer from the pill path, which is the point of their sharing one.
	if got := m.processedMentionName("@p:x", "+972500000", "!a:x"); got != "Dana" {
		t.Errorf("aliased mention = %q, want Dana", got)
	}

	// Without a matching rule, the name is left intact — alias or not.
	m.prefs.display.SpaceRules = nil
	m.prefs.display.MaxNameLength = 0
	if got := m.processedName(inWork); got != "Rowan Blackwood" {
		t.Errorf("no rule = %q, want full name", got)
	}
	if got := m.processedName(domain.Message{RoomID: "!a:x", Sender: "@p:x", SenderName: "+972500000"}); got != "Dana Levi" {
		t.Errorf("aliased name with no rule = %q, want it whole", got)
	}
}

func TestRoomName(t *testing.T) {
	t.Parallel()

	single := domain.Room{ID: "!dm:x", Name: "Rowan Blackwood", Members: []string{"Rowan Blackwood"}}
	comma := domain.Room{ID: "!c:x", Name: "Rowan Blackwood, Piper Nightingale", Members: []string{"Rowan Blackwood", "Piper Nightingale"}}
	andJoined := domain.Room{ID: "!a:x", Name: "Piper Nightingale and Quinn Foster", Members: []string{"Piper Nightingale", "Quinn Foster"}}
	crowd := domain.Room{ID: "!cr:x", Name: "Ada Zephyr, Bo Quill and 3 others", Members: []string{"Ada Zephyr", "Bo Quill"}}
	// A real title whose members are unrelated people — must be left alone.
	titled := domain.Room{ID: "!t:x", Name: "Beer & Escape", Members: []string{"Rowan Blackwood", "Piper Nightingale"}}

	// A configured alias wins verbatim, over the space name rules.
	m := New(context.Background(), apitest.Nop{}, config.Display{
		Names:         []config.DisplayName{{Target: "!dm:x", Name: "Mom"}},
		SpaceRules:    []config.SpaceRule{{Space: "home", FirstNameOnly: true}},
		MaxNameLength: 8,
	})
	m = sized(t, withRooms(t, m)) // current rail group key is "home"
	if got := m.roomName(single); got != "Mom" {
		t.Errorf("aliased room = %q, want Mom", got)
	}

	// A first-name-only space shortens people-named rooms and leaves titles alone; the
	// rule names the space the rooms are in.
	m = New(context.Background(), apitest.Nop{}, config.Display{
		SpaceRules: []config.SpaceRule{{Space: "Work", FirstNameOnly: true}},
	})
	m = sized(t, withRooms(t, m))
	m = update(t, m, spacesMsg{spaces: []domain.Space{{
		ID: "!w:x", Name: "Work",
		Children: []domain.RoomID{"!dm:x", "!c:x", "!a:x", "!cr:x", "!t:x"},
	}}})
	for _, tc := range []struct {
		in   domain.Room
		want string
	}{
		{single, "Rowan"},
		{comma, "Rowan, Piper"},
		{andJoined, "Piper and Quinn"},
		{crowd, "Ada, Bo and 3 others"},
		{titled, "Beer & Escape"}, // members don't appear in the title → untouched
	} {
		if got := m.roomName(tc.in); got != tc.want {
			t.Errorf("first-name room %q = %q, want %q", tc.in.Name, got, tc.want)
		}
	}

	// No rule for the current space: names are left in full.
	m = sized(t, withRooms(t, newModel()))
	if got := m.roomName(comma); got != "Rowan Blackwood, Piper Nightingale" {
		t.Errorf("no rule = %q, want full name", got)
	}

	// The length cap does not truncate a multi-person room name.
	trio := domain.Room{
		ID:      "!trio:x",
		Name:    "Rowan Blackwood, Piper Nightingale, Quinn Foster",
		Members: []string{"Rowan Blackwood", "Piper Nightingale", "Quinn Foster"},
	}
	m = New(context.Background(), apitest.Nop{}, config.Display{
		SpaceRules:    []config.SpaceRule{{Space: "Work", FirstNameOnly: true}},
		MaxNameLength: 8,
	})
	m = sized(t, withRooms(t, m))
	m = update(t, m, spacesMsg{spaces: []domain.Space{{
		ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!trio:x"},
	}}})
	if got := m.roomName(trio); got != "Rowan, Piper, Quinn" {
		t.Errorf("trio = %q, want full uncapped Rowan, Piper, Quinn", got)
	}

	off := false
	m = New(context.Background(), apitest.Nop{}, config.Display{
		SpaceRules:    []config.SpaceRule{{Space: "home", FirstNameOnly: true}},
		RoomNameRules: &off,
	})
	m = sized(t, withRooms(t, m))
	if got := m.roomName(comma); got != "Rowan Blackwood, Piper Nightingale" {
		t.Errorf("rules-off room = %q, want unchanged full name", got)
	}

	// RTL names stay logical (isolated) in the model and are reordered where drawn.
	rtl := domain.Room{ID: "!rtl:x", Name: "אב 12 גד"}
	if got := m.roomName(rtl); got != isolate(rtl.Name) {
		t.Errorf("RTL room name = %q, want the logical name isolated", got)
	}
	want := reorder("אב 12 גד", paragraphDir("אב 12 גד"))
	if got := displayTitle(m.roomName(rtl)); got != want {
		t.Errorf("drawn RTL room name = %q, want reordered %q", got, want)
	}
}

func TestNameColumnAligns(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@a:x", SenderName: "Al", Body: "AAA", Timestamp: at(1)},
		{ID: "$2", RoomID: "!a:x", Sender: "@b:x", SenderName: "Bartholomew", Body: "BBB", Timestamp: at(2)},
	})
	colA, colB := -1, -1
	for _, r := range m.layoutRows() {
		p := ansi.Strip(r)
		if i := strings.Index(p, "AAA"); i >= 0 {
			colA = i
		}
		if i := strings.Index(p, "BBB"); i >= 0 {
			colB = i
		}
	}
	if colA < 0 || colB < 0 {
		t.Fatalf("bodies not found: colA=%d colB=%d", colA, colB)
	}
	if colA != colB {
		t.Errorf("bodies not aligned: short-name body at %d, long-name body at %d", colA, colB)
	}
}

func TestBlankMessageRowsRemoved(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@a:x", SenderName: "Al", Body: "line1\n\n\nline2", Timestamp: at(1)},
	})
	for _, r := range m.layoutRows() {
		if strings.Contains(r, "─") {
			continue // date divider
		}
		if strings.TrimSpace(ansi.Strip(r)) == "" {
			t.Errorf("blank message row rendered: %q", r)
		}
	}
}

func TestParagraphDirAnyRTL(t *testing.T) {
	t.Parallel()

	if got := paragraphDir("hello world"); got != bidi.LeftToRight {
		t.Errorf("LTR text dir = %v, want LeftToRight", got)
	}
	if got := paragraphDir("שלום עולם"); got != bidi.RightToLeft {
		t.Errorf("RTL text dir = %v, want RightToLeft", got)
	}
	// Any RTL character makes the whole row RTL, even a mostly-Latin line.
	if got := paragraphDir("mostly english with one שלום word"); got != bidi.RightToLeft {
		t.Errorf("any-RTL text dir = %v, want RightToLeft", got)
	}
}

func TestReorderRTLKeepsSegmentOrderAroundNumber(t *testing.T) {
	t.Parallel()

	// The number must not scramble the order of the two Hebrew segments.
	got := reorder("אב 12 גד", bidi.RightToLeft)
	first := strings.Index(got, "בא") // "אב" reversed for display
	last := strings.Index(got, "דג")  // "גד" reversed for display
	if first < 0 || last < 0 {
		t.Fatalf("Hebrew runs missing from %q", got)
	}
	if last >= first {
		t.Errorf("RTL segments swapped around the number: %q", got)
	}
	if !strings.Contains(got, "12") {
		t.Errorf("embedded number mangled: %q", got)
	}
}

func TestSenderColorsDistinct(t *testing.T) {
	t.Parallel()

	m := newModel()
	m = m.setMessages([]domain.Message{
		{Sender: "@a:x"}, {Sender: "@b:x"}, {Sender: "@a:x"}, {Sender: "@c:x"},
	})
	colors := m.senderColorMap()
	if len(colors) != 3 {
		t.Fatalf("distinct senders = %d, want 3", len(colors))
	}
	seen := make(map[uint64]bool)
	for _, c := range colors {
		if seen[colorKey(c)] {
			t.Errorf("two senders share color %v", c)
		}
		seen[colorKey(c)] = true
	}
}

func colorKey(c color.Color) uint64 {
	r, g, b, _ := c.RGBA()
	return uint64(r)<<32 | uint64(g)<<16 | uint64(b)
}

func TestIdentityPinsColorAndAlias(t *testing.T) {
	t.Parallel()

	m := newModel()
	m.prefs.display = config.Display{Identities: []config.Identity{
		{Alias: "Me", Color: "#66ccff", MXIDs: []string{"@a:x", "@a2:x"}},
	}}
	m.prefs.identities = buildIdentities(m.prefs.display.Identities)

	// The alias is shown for every merged account, bypassing name rules.
	if got := m.processedName(domain.Message{Sender: "@a:x", SenderName: "Alpha"}); got != "Me" {
		t.Errorf("alias = %q, want Me", got)
	}
	if got := m.processedName(domain.Message{Sender: "@a2:x", SenderName: "Alpha Alt"}); got != "Me" {
		t.Errorf("merged alias = %q, want Me", got)
	}

	m = m.setMessages([]domain.Message{{Sender: "@a:x"}, {Sender: "@a2:x"}, {Sender: "@b:x"}})
	colors := m.senderColorMap()
	if colorKey(colors["@a:x"]) != colorKey(colors["@a2:x"]) {
		t.Error("merged accounts should share one color")
	}
	pinned, _ := theme.ParseColor("#66ccff")
	if colorKey(colors["@a:x"]) != colorKey(pinned) {
		t.Error("merged identity should use its pinned color")
	}
	if colorKey(colors["@b:x"]) == colorKey(pinned) {
		t.Error("auto-assigned user should avoid the pinned color")
	}
}

func TestReorderMirrorsBrackets(t *testing.T) {
	t.Parallel()

	// Brackets mirror in an RTL run (Unicode BiDi rule L4).
	if got := reorder("א(ב)", bidi.RightToLeft); got != "(ב)א" {
		t.Errorf("bracket mirroring: got %q, want %q", got, "(ב)א")
	}
}

func TestRTLMessageRightAligned(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = m.setMessages([]domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@a:x", SenderName: "Al", Body: "שלום עולם", Timestamp: at(1)},
	})
	width := m.contentWidth()
	for _, r := range m.layoutRows() {
		if strings.Contains(r, "─") || strings.TrimSpace(ansi.Strip(r)) == "" {
			continue
		}
		// A right-aligned RTL row fills the content width (prefix + left-pad + body).
		if w := ansi.StringWidth(r); w < width-1 {
			t.Errorf("RTL row not flush right: width %d, content %d: %q", w, width, ansi.Strip(r))
		}
	}
}

func TestSenderLabel(t *testing.T) {
	t.Parallel()

	if got := senderLabel(domain.Message{Sender: "@alice:x", SenderName: "Alice Smith"}); got != "Alice Smith" {
		t.Errorf("label = %q, want display name", got)
	}
	if got := senderLabel(domain.Message{Sender: "@alice:x"}); got != "alice" {
		t.Errorf("label = %q, want localpart fallback", got)
	}
}

func TestClampCollapsesNewlines(t *testing.T) {
	t.Parallel()

	if got := clamp("line one\nline two\r\nline three", 60); strings.Contains(got, "\n") {
		t.Errorf("clamp left a newline in %q", got)
	}
}

func TestVisualOrderRTL(t *testing.T) {
	t.Parallel()

	if got := visualOrder("hello world"); got != "hello world" {
		t.Errorf("LTR text should be unchanged, got %q", got)
	}
	// A pure right-to-left run (Hebrew alef-bet-gimel) is reversed for display.
	if got := visualOrder("אבג"); got != "גבא" {
		t.Errorf("RTL reorder = %q, want reversed", got)
	}
}

func TestUnknownMessageIsNoop(t *testing.T) {
	t.Parallel()

	type customMsg struct{}
	next, cmd := asModel(newModel().Update(customMsg{}))
	if cmd != nil || next.status() != "loading rooms…" {
		t.Error("unknown message should not change the model")
	}
}

func TestShortSender(t *testing.T) {
	t.Parallel()

	cases := map[string]string{"@alice:example.org": "alice", "bob": "bob", "": ""}
	for in, want := range cases {
		if got := shortSender(in); got != want {
			t.Errorf("shortSender(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnreadUpdateBadgesRoom(t *testing.T) {
	t.Parallel()

	// Seed a room list, then stream an unread update for it.
	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!a:x", Notifications: 3}})
	if got := m.unread["!a:x"].Notifications; got != 3 {
		t.Fatalf("unread notifications = %d, want 3", got)
	}

	// Only unread Alpha carries the badge, right-aligned against the pane edge.
	rooms := ansi.Strip(m.renderRooms(roomsWidth, 20))
	alpha, ok := lineWith(rooms, "Alpha")
	if !ok {
		t.Fatalf("no row for Alpha:\n%s", rooms)
	}
	if !strings.Contains(alpha, "●3") {
		t.Errorf("room list missing unread badge for Alpha:\n%s", rooms)
	}
	// Columns, not bytes: "●" is three bytes.
	if before, _, ok0 := strings.Cut(alpha, "●3"); ok0 && ansi.StringWidth(before) != roomsWidth-1-ansi.StringWidth("●3") {
		t.Errorf("the badge is not against the right edge:\n%q", alpha)
	}
	bravo, ok := lineWith(rooms, "Bravo")
	if !ok {
		t.Fatalf("no row for Bravo:\n%s", rooms)
	}
	if strings.Contains(bravo, "●") {
		t.Errorf("read room Bravo should carry no badge:\n%s", rooms)
	}
}

// Each rail group's badge sums its rooms' unread counts.
func TestRailBadgesSumTheGroupsRooms(t *testing.T) {
	t.Parallel()

	m := update(t, newModel(), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo", IsDirect: true},
		{ID: "!c:x", Name: "Cyan"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x", "!b:x"}},
		{ID: "!q:x", Name: "Quiet", Children: []domain.RoomID{"!c:x"}},
	}})
	m = update(t, m, unreadMsg{list: []domain.Unread{
		{RoomID: "!a:x", Notifications: 3},
		{RoomID: "!b:x", Notifications: 2},
	}})
	m = sized(t, m)

	rail := ansi.Strip(m.renderRail(20))
	for _, want := range []string{"Work", "●5", "●2", "Quiet"} {
		if !strings.Contains(rail, want) {
			t.Errorf("rail missing %q:\n%s", want, rail)
		}
	}
	for _, tc := range []struct {
		key           string
		notifications int
	}{
		{"home", 5}, {"dms", 2}, {"unread", 5}, {"Work", 5}, {"Quiet", 0},
	} {
		g, ok := findGroup(m.rail.groups, tc.key)
		if !ok {
			t.Fatalf("no %s group in the rail", tc.key)
		}
		if got, _ := m.groupUnread(g); got != tc.notifications {
			t.Errorf("%s: summed %d unread, want %d", tc.key, got, tc.notifications)
		}
	}
	quiet, _ := findGroup(m.rail.groups, "Quiet")
	if badge, _ := m.groupBadge(quiet); badge != "" {
		t.Errorf("a group with nothing unread should carry no badge, got %q", badge)
	}
}

// A highlight inside a space tints the group's badge.
func TestRailBadgeTintsForAHighlight(t *testing.T) {
	t.Parallel()

	m := update(t, newModel(), roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	m = update(t, m, unreadMsg{list: []domain.Unread{{RoomID: "!a:x", Notifications: 4, Highlights: 1}}})

	g, ok := findGroup(m.rail.groups, "Work")
	if !ok {
		t.Fatal("no Work group in the rail")
	}
	if _, highlights := m.groupUnread(g); highlights != 1 {
		t.Errorf("summed %d highlights, want 1", highlights)
	}
	badge, highlight := m.groupBadge(g)
	if badge != "●4" || !highlight {
		t.Errorf("badge = (%q, %t), want (\"●4\", true) — the highlight tint", badge, highlight)
	}
}

// A long space name is clipped before the badge is.
func TestRailBadgeSurvivesALongGroupName(t *testing.T) {
	t.Parallel()

	m := update(t, newModel(), roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Infrastructure and Platform", Children: []domain.RoomID{"!a:x"}},
	}})
	m = update(t, m, unreadMsg{list: []domain.Unread{{RoomID: "!a:x", Notifications: 128}}})
	m = sized(t, m)

	rail := ansi.Strip(m.renderRail(20))
	if !strings.Contains(rail, "●128") {
		t.Errorf("the count was clipped away by a long name:\n%s", rail)
	}
	for line := range strings.SplitSeq(rail, "\n") {
		if w := ansi.StringWidth(line); w > railWidth {
			t.Errorf("rail row is %d wide, past the pane's %d:\n%q", w, railWidth, line)
		}
	}
}

// enterGroup selects the rail group with key as a rail keypress would.
func enterGroup(t *testing.T, m Model, key string) Model {
	t.Helper()
	m.rail.cursor = indexOfGroup(m.rail.groups, key)
	next, _ := m.selectGroup()
	return next
}

func TestUnreadGroupFilters(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!a:x", Notifications: 1}})
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!b:x", Notifications: 2}})
	m = enterGroup(t, m, "unread")
	if fr := m.filteredRooms(); len(fr) != 2 {
		t.Fatalf("Unread group = %+v, want both rooms", fr)
	}
	if m.openRoom != "!a:x" || m.roomCursor() != 0 {
		t.Fatalf("open=%q cursor=%d, want Alpha at 0", m.openRoom, m.roomCursor())
	}

	// Reading the open room keeps it listed and selected in Unread.
	m = update(t, m, unreadUpdateMsg{u: domain.Unread{RoomID: "!a:x", Notifications: 0}})
	fr := m.filteredRooms()
	if len(fr) != 2 || fr[0].ID != "!a:x" {
		t.Fatalf("after reading open room, Unread group = %+v, want Alpha still pinned first", fr)
	}
	if r, _ := m.currentRoom(); r.ID != "!a:x" || m.roomCursor() != 0 {
		t.Errorf("shown/selected drifted: open=%q cursor=%d, want Alpha at 0", r.ID, m.roomCursor())
	}

	// Moving off it drops the read room; the highlight follows Bravo.
	m.focus = paneRooms
	m, _ = press(t, m, keyCode(tea.KeyDown))
	if fr := m.filteredRooms(); len(fr) != 1 || fr[0].ID != "!b:x" {
		t.Fatalf("after moving off read Alpha, Unread group = %+v, want only Bravo", fr)
	}
	if r, _ := m.currentRoom(); r.ID != "!b:x" || m.roomCursor() != 0 {
		t.Errorf("open=%q cursor=%d, want Bravo at 0", r.ID, m.roomCursor())
	}
}

// recordingBackend records MarkRead calls.
type recordingBackend struct {
	apitest.Nop
	reads     int
	readRoom  domain.RoomID
	readEvent domain.EventID
}

func (r *recordingBackend) MarkRead(_ context.Context, room domain.RoomID, ev domain.EventID, _ bool) error {
	r.reads, r.readRoom, r.readEvent = r.reads+1, room, ev
	return nil
}

func TestMarkReadDedupes(t *testing.T) {
	t.Parallel()

	rb := &recordingBackend{}
	m := update(t, New(context.Background(), rb, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m.unread["!a:x"] = domain.Unread{RoomID: "!a:x", Notifications: 3}
	m = m.setMessages([]domain.Message{{ID: "$e1", RoomID: "!a:x"}})

	// First mark sends a receipt for the newest event and records the target.
	m, cmd := m.markRead()
	if cmd == nil {
		t.Fatal("markRead returned no command for an unmarked newest event")
	}
	// While it is in flight, it is not sent again.
	if _, again := m.markRead(); again != nil {
		t.Error("markRead re-sent a receipt still in flight")
	}
	sent, ok := msgOf[receiptSentMsg](t, cmd)
	if !ok {
		t.Fatal("the receipt's outcome was not routed back")
	}
	if rb.reads != 1 || rb.readRoom != "!a:x" || rb.readEvent != "$e1" {
		t.Fatalf("MarkRead recorded reads=%d room=%q event=%q, want 1/!a:x/$e1", rb.reads, rb.readRoom, rb.readEvent)
	}
	m = update(t, m, sent)
	if m.receipts.marked != "$e1" {
		t.Errorf("receipts.marked = %q, want $e1", m.receipts.marked)
	}

	// Re-marking the same newest event is a no-op (dedupe).
	if _, cmd := m.markRead(); cmd != nil {
		t.Error("markRead re-sent a receipt for an already-marked event")
	}

	// A newer message re-arms the mark.
	m = m.setMessages(append(m.timeline.messages, domain.Message{ID: "$e2", RoomID: "!a:x"}))
	if _, cmd := m.markRead(); cmd == nil {
		t.Error("markRead did not re-send after a newer message arrived")
	}
}

// A reply's quote sits on the sender's row, one line only.
func TestReplyQuoteSitsBesideTheSender(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = loadPage(t, m, []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@dn:x", SenderName: "Dan", Body: "первая строка\nвторая строка", Timestamp: at(1)},
		{ID: "$1b", RoomID: "!a:x", Sender: "@dn:x", SenderName: "Dan", Body: "and one more", Timestamp: at(2)},
		{ID: "$2", RoomID: "!a:x", Sender: "@w:x", SenderName: "Wes", Body: "answering", ReplyTo: "$1", Timestamp: at(3)},
	})

	rows := m.layoutRows()
	var quoted, senderRow int
	for i, row := range rows {
		plain := ansi.Strip(row)
		if strings.Contains(plain, "↪") {
			quoted++
			if !strings.Contains(plain, "Wes") {
				t.Errorf("the quote is on a row of its own:\n%q", plain)
			}
		}
		if strings.Contains(plain, "Wes") {
			senderRow = i
		}
	}
	if quoted != 1 {
		t.Errorf("%d quote rows, want exactly one — a multi-line target must still be one line", quoted)
	}
	// The body follows the quote, indented under it rather than beside the name.
	if body := ansi.Strip(rows[senderRow+1]); !strings.Contains(body, "answering") {
		t.Errorf("row after the quote = %q, want the reply's own text", body)
	}
}

// The reply quote is drawn in visual order.
func TestReplyQuoteIsReordered(t *testing.T) {
	t.Parallel()

	const hebrew = "שלום, מה קורה?"
	m := sized(t, withRooms(t, newModel()))
	m = loadPage(t, m, []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@n:x", SenderName: "Noa", Body: hebrew, Timestamp: at(1)},
		// In between, so the quote is drawn at all: one of the row above is dropped.
		{ID: "$1b", RoomID: "!a:x", Sender: "@n:x", SenderName: "Noa", Body: "עוד הודעה", Timestamp: at(2)},
		{ID: "$2", RoomID: "!a:x", Sender: "@w:x", SenderName: "Wes", Body: "ok", ReplyTo: "$1", Timestamp: at(3)},
	})

	quote := ""
	for _, row := range m.layoutRows() {
		if plain := ansi.Strip(row); strings.Contains(plain, "↪") {
			quote = plain
		}
	}
	if quote == "" {
		t.Fatal("no quote row")
	}
	if strings.Contains(quote, hebrew) {
		t.Errorf("quote is in logical order (backwards on screen):\n%q", quote)
	}
	if want := reorder(hebrew, bidi.RightToLeft); !strings.Contains(quote, want) {
		t.Errorf("quote = %q, want it to contain the reordered text %q", quote, want)
	}
}

// RTL composer text is reordered with the caret at its visual left.
func TestComposerReordersAndPlacesTheCaret(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.focus, m.compose.insertMode = paneTimeline, true

	m.compose.input = "בסדר גמור"
	rtl := ansi.Strip(editedLine(m.editorFor(fieldComposer), 40, true))
	if strings.Contains(rtl, m.compose.input) {
		t.Errorf("composer shows the logical string %q", rtl)
	}
	if !strings.HasPrefix(rtl, caretMark) {
		t.Errorf("composer = %q, want the caret at the visual left for RTL", rtl)
	}

	m.compose.input = "hello"
	ltr := ansi.Strip(editedLine(m.editorFor(fieldComposer), 40, true))
	if ltr != "hello"+caretMark {
		t.Errorf("composer = %q, want the caret after the text", ltr)
	}
	if plain := ansi.Strip(editedLine(m.editorFor(fieldComposer), 40, false)); plain != "hello" {
		t.Errorf("normal mode = %q, want no caret", plain)
	}
}

// receiptRefuser refuses the first receipts, then accepts.
type receiptRefuser struct {
	apitest.Nop
	refuse int
	reads  []domain.EventID
}

func (r *receiptRefuser) MarkRead(_ context.Context, _ domain.RoomID, ev domain.EventID, _ bool) error {
	r.reads = append(r.reads, ev)
	if len(r.reads) <= r.refuse {
		return errors.New("homeserver said no")
	}
	return nil
}

// A refused receipt is said, and sent again on the next trigger rather than taken
// as done.
func TestAFailedReceiptIsSaidAndRetried(t *testing.T) {
	t.Parallel()

	rb := &receiptRefuser{refuse: 1}
	m := update(t, New(context.Background(), rb, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = m.setMessages([]domain.Message{{ID: "$e1", RoomID: "!a:x"}})

	m, cmd := m.markRead()
	if cmd == nil {
		t.Fatal("markRead returned no command for an unmarked newest event")
	}
	failed, ok := msgOf[receiptSentMsg](t, cmd)
	if !ok || failed.err == nil {
		t.Fatalf("outcome = %+v, %v; want the refusal routed back", failed, ok)
	}
	m = update(t, m, failed)
	if m.receipts.marked == "$e1" {
		t.Error("a refused receipt was recorded as sent")
	}
	if !strings.Contains(m.st.event, "read receipt failed") {
		t.Errorf("status = %q, want the failure said", m.st.event)
	}

	// The next trigger retries the same event.
	m, cmd = m.markRead()
	if cmd == nil {
		t.Fatal("markRead did not retry after a refusal")
	}
	landed, _ := msgOf[receiptSentMsg](t, cmd)
	m = update(t, m, landed)
	if m.receipts.marked != "$e1" || len(rb.reads) != 2 {
		t.Errorf("marked = %q after %d receipts, want $e1 after 2", m.receipts.marked, len(rb.reads))
	}
}

// The helpers that tell commands apart (isTimer, isBatch, isListener) match closures by
// name, and those names belong to bubbletea and to how the compiler inlines. If a
// rebase or a build mode renames them, this fails, rather than "no timer was armed"
// passing for want of recognizing one.
func TestTheCommandMatchersKnowTheirCommands(t *testing.T) {
	t.Parallel()
	noop := func() tea.Msg { return nil }
	ch := make(chan int)
	for name, c := range map[string]struct {
		cmd  tea.Cmd
		test func(tea.Cmd) bool
	}{
		"tea.Tick is a timer":             {tea.Tick(time.Hour, func(time.Time) tea.Msg { return nil }), isTimer},
		"a package timer is a timer":      {draftTickCmd(1), isTimer},
		"tea.Batch is a batch":            {tea.Batch(noop, noop), isBatch},
		"listen is a listener":            {listen(context.Background(), ch, func(int) tea.Msg { return nil }), isListener},
		"a plain command is none of them": {noop, func(c tea.Cmd) bool { return !isTimer(c) && !isBatch(c) && !isListener(c) }},
	} {
		if !c.test(c.cmd) {
			t.Errorf("%s: not recognized (%s)", name, cmdFunc(c.cmd))
		}
	}
}
