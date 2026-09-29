package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// jumping builds a client with two spaces that hold different rooms, so a jump has
// somewhere to jump *from* and the rail has to move to follow it.
func jumping(t *testing.T) Model {
	t.Helper()
	m := update(t, New(context.Background(), apitest.Nop{}, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!dana:x", Name: "Dana Levi", IsDirect: true},
		{ID: "!ops:x", Name: "Ops"},
	}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
		{ID: "!i:x", Name: "Infra", Children: []domain.RoomID{"!ops:x"}},
	}})
	return sized(t, m.clearStatus())
}

func jumpKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl} }

// value of the row under the cursor, for asserting what a filter narrowed to.
func pickedValue(t *testing.T, m Model) string {
	t.Helper()
	item, ok := m.picker.selected()
	if !ok {
		t.Fatal("the picker has no row under its cursor")
	}
	return item.value
}

// typeFilter types into the open picker's filter.
func typeFilter(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		next, _ := press(t, m, keyText(string(r)))
		m = next
	}
	return m
}

// The switcher lists the places that exist: every room, then every rail group.
func TestJumpListsRoomsAndSpaces(t *testing.T) {
	t.Parallel()

	m, _ := press(t, jumping(t), jumpKey())
	if m.picker.kind != pickerJump {
		t.Fatalf("ctrl+k opened picker %d, want the switcher", m.picker.kind)
	}
	var rooms, spaces int
	for _, item := range m.picker.all {
		if strings.HasPrefix(item.value, "room:") {
			rooms++
		}
		if strings.HasPrefix(item.value, "group:") {
			spaces++
		}
	}
	if rooms != 3 {
		t.Errorf("listed %d rooms, want all three", rooms)
	}
	// All, DMs, Unread, Work, Infra.
	if spaces != 5 {
		t.Errorf("listed %d groups, want the whole rail", spaces)
	}
	// It filters from the first keystroke: choosing a row is the only thing it does, so
	// there is no letter action for typing to collide with.
	if m.picker.mode != pickerFilter {
		t.Error("the switcher should start in filter mode")
	}
}

// Typing a person's name reaches their conversation, and the rail follows: the
// highlight is derived from the *filtered* room list, so opening a room the current
// group excludes would leave the timeline in one room and the list pointing nowhere.
func TestJumpMovesTheRailToHoldTheRoom(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	// Standing in Work, which holds Alpha and nothing else.
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	next, _ := press(t, m, jumpKey())
	next = typeFilter(t, next, "ops")
	if got := pickedValue(t, next); got != "room:!ops:x" {
		t.Fatalf("filter landed on %q, want the Ops room", got)
	}
	after, _ := press(t, next, tea.KeyPressMsg{Code: tea.KeyEnter})

	if after.openRoom != "!ops:x" {
		t.Errorf("open room = %q, want !ops:x", after.openRoom)
	}
	if got := after.rail.groups[after.rail.cursor].key; got != "Infra" {
		t.Errorf("rail is on %q, want the space that holds the room", got)
	}
	if idx := indexOfRoom(after.filteredRooms(), "!ops:x"); idx < 0 {
		t.Error("the room list does not contain the room that was opened")
	}
	if after.focus != paneTimeline {
		t.Error("jumping to a room should land in its timeline")
	}
	if after.picker.active() {
		t.Error("the switcher stayed open after choosing")
	}
}

// Jumping between rooms of one space leaves the rail where it is: the group already
// holds the room, and moving it out from under you would be gratuitous.
func TestJumpStaysInTheGroupThatAlreadyHolds(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m.rail.cursor = indexOfGroup(m.rail.groups, "dms")
	next, _ := press(t, m, jumpKey())
	next = typeFilter(t, next, "dana")
	after, _ := press(t, next, tea.KeyPressMsg{Code: tea.KeyEnter})

	if after.openRoom != "!dana:x" {
		t.Fatalf("open room = %q, want the DM", after.openRoom)
	}
	if got := after.rail.groups[after.rail.cursor].key; got != "dms" {
		t.Errorf("rail moved to %q, want to stay in DMs", got)
	}
}

// A space row moves the rail and opens what is in it, which is what selecting a group
// in the rail already does.
func TestJumpToASpace(t *testing.T) {
	t.Parallel()

	m, _ := press(t, jumping(t), jumpKey())
	m = typeFilter(t, m, "infra")
	if got := pickedValue(t, m); got != "group:Infra" {
		t.Fatalf("filter landed on %q, want the Infra space", got)
	}
	after, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := after.rail.groups[after.rail.cursor].key; got != "Infra" {
		t.Errorf("rail is on %q, want Infra", got)
	}
	if after.openRoom != "!ops:x" {
		t.Errorf("open room = %q, want the space's first room", after.openRoom)
	}
	if after.focus != paneRooms {
		t.Error("jumping to a space should leave the keyboard in its room list")
	}
}

// Unread rooms come first with nothing typed: before you have said what you are looking
// for, "where am I needed" is the only ordering that means anything.
func TestJumpPutsUnreadRoomsFirst(t *testing.T) {
	t.Parallel()

	m := update(t, jumping(t), unreadMsg{list: []domain.Unread{
		{RoomID: "!ops:x", Notifications: 2, Messages: 2, Counted: true},
	}})
	m, _ = press(t, m, jumpKey())
	first := m.picker.all[0]
	if first.value != "room:!ops:x" {
		t.Errorf("first row = %q, want the unread room", first.value)
	}
	if !strings.Contains(first.detail, "unread") {
		t.Errorf("first row detail = %q, want it to say how much is unread", first.detail)
	}
}

// The switcher is reachable mid-sentence, and what was typed is still there afterwards:
// deciding to answer somebody else is exactly when it is wanted.
func TestJumpWorksWhileTypingAndKeepsTheDraft(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	next, _ := m.selectRoom(m.rooms.all[0])
	m = next
	m.focus, m.compose.insertMode = paneTimeline, true
	m = m.store(fieldComposer, editor{text: "half a sentence", at: len("half a sentence")})

	opened, _ := press(t, m, jumpKey())
	if opened.picker.kind != pickerJump {
		t.Fatal("ctrl+k did not open the switcher from insert mode")
	}
	// Insert mode is kept, so esc out of the switcher is back in the sentence rather
	// than one key away from it.
	if !opened.compose.insertMode {
		t.Error("backing out of the switcher should return to the message being written")
	}
	typed := typeFilter(t, opened, "ops")
	if got := typed.editorFor(fieldComposer).text; got != "half a sentence" {
		t.Errorf("filtering typed into the composer: %q", got)
	}
	if typed.picker.filter != "ops" {
		t.Errorf("picker filter = %q, want the letters to have narrowed the list", typed.picker.filter)
	}
	if got := opened.editorFor(fieldComposer).text; got != "half a sentence" {
		t.Errorf("composer = %q, want the draft untouched", got)
	}
}

// A row whose room has gone — left or archived while the chooser was open — says so
// rather than opening whatever moved into its place.
func TestJumpToAVanishedRoomSaysSo(t *testing.T) {
	t.Parallel()

	m, _ := press(t, jumping(t), jumpKey())
	after, _ := m.goToRoom("!nosuch:x")
	if after.picker.active() {
		t.Error("the switcher stayed open")
	}
	if !strings.Contains(after.status(), "no longer") {
		t.Errorf("status = %q, want it to say the room is gone", after.status())
	}
}

// jumpBound is the client with two spaces plus a chord for each kind of place.
func jumpBound(t *testing.T) Model {
	t.Helper()
	keys := config.DefaultKeys()
	keys.Jump = config.Jumps{
		{Chord: "g w", Target: "space:Work"},
		{Chord: "g d", Target: "room:!dana:x"},
		{Chord: "g o", Target: "room:!ops:x"},
	}
	return jumping(t).WithKeys(keys)
}

// A sequence bound to a place goes there — the whole point being that it costs no
// typing and no list.
func TestJumpChordGoesToItsPlace(t *testing.T) {
	t.Parallel()

	m := jumpBound(t)
	m.focus = paneRooms
	m, _ = press(t, m, keyText("g"))
	if m.chordPending() == "" {
		t.Fatal("the first step of a jump sequence did not wait for the second")
	}
	after, _ := press(t, m, keyText("d"))
	if after.openRoom != "!dana:x" {
		t.Errorf("open room = %q, want the DM the chord names", after.openRoom)
	}

	// And a space chord moves the rail.
	space := jumpBound(t)
	space, _ = press(t, space, keyText("g"))
	space, _ = press(t, space, keyText("w"))
	if got := space.rail.groups[space.rail.cursor].key; got != "Work" {
		t.Errorf("rail is on %q, want Work", got)
	}
}

// A room is named by its ID, so a room whose *name* looks like the target cannot
// intercept it — which is the whole reason name matching was dropped.
func TestChordNamesARoomByIDNotByName(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Jump = config.Jumps{
		{Chord: "g d", Target: "room:!slack-dana:x"},
	}
	m := namesakes(t).WithKeys(keys)
	m.focus = paneRooms
	m, _ = press(t, m, keyText("g"))
	after, _ := press(t, m, keyText("d"))
	if after.openRoom != "!slack-dana:x" {
		t.Errorf("open room = %q, want the room the binding names", after.openRoom)
	}
}

// A room target must be an ID: a display name is a config issue.
func TestRoomTargetMustBeAnID(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Jump = config.Jumps{{Chord: "g d", Target: "room:Dana Levi"}}
	km := newKeymap(keys)
	if len(km.jumps) != 0 {
		t.Errorf("bound %+v, want nothing: a name is not a room", km.jumps)
	}
	if !strings.Contains(strings.Join(km.issues, " "), "room:<!id:server>") {
		t.Errorf("issues = %v, want them to say what a room target looks like", km.issues)
	}
}

// A target that resolves to nothing says so.
func TestJumpChordToNowhereSaysSo(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Jump = config.Jumps{
		{Chord: "g z", Target: "space:Nowhere"},
	}
	m := jumping(t).WithKeys(keys)
	m, _ = press(t, m, keyText("g"))
	after, _ := press(t, m, keyText("z"))
	if !strings.Contains(after.status(), "Nowhere") {
		t.Errorf("status = %q, want it to name the space it could not find", after.status())
	}
}

// Jump sequences are suspended while typing, like every other chord: holding a step
// back to see what follows it would drop a character from a message.
func TestJumpChordsDoNotFireWhileTyping(t *testing.T) {
	t.Parallel()

	m := jumpBound(t)
	next, _ := m.selectRoom(m.rooms.all[0])
	m = next
	m.focus, m.compose.insertMode = paneTimeline, true
	m, _ = press(t, m, keyText("g"))
	m, _ = press(t, m, keyText("w"))
	if got := m.editorFor(fieldComposer).text; got != "gw" {
		t.Errorf("composer = %q, want both letters typed", got)
	}
	if m.openRoom != "!a:x" {
		t.Errorf("typing gw moved to %q", m.openRoom)
	}
}

// A sequence already bound to a command is refused rather than allowed to shadow it:
// the action tables are what every handler and the help overlay are written against.
func TestJumpCannotStealABoundKey(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Jump = config.Jumps{
		{Chord: "gg", Target: "space:Work"},
	}
	km := newKeymap(keys)
	if _, ok := km.jumpFor("g g"); ok {
		t.Error("a jump took gg from nav.select_oldest")
	}
	if !strings.Contains(strings.Join(km.issues, " "), "already bound") {
		t.Errorf("issues = %v, want one naming the clash", km.issues)
	}
}

// An unreadable target is an issue, not a key that quietly does nothing.
func TestJumpIssuesForABadTarget(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Jump = config.Jumps{
		{Chord: "g 1", Target: "Work"},
		{Chord: "g 2", Target: "planet:Mars"},
		{Chord: "g 3", Target: "space:"},
		{Chord: "g 4", Target: "space:Work"},
		{Chord: "escape x", Target: "space:Work"},
	}
	km := newKeymap(keys)
	if len(km.jumps) != 1 {
		t.Errorf("bound %d jumps, want only the readable one: %+v", len(km.jumps), km.jumps)
	}
	if _, ok := km.jumpFor("g 4"); !ok {
		t.Error("the readable binding did not survive its neighbors")
	}
	if len(km.issues) < 4 {
		t.Errorf("issues = %v, want one per bad binding", km.issues)
	}
}

// The overlay lists them, resolved to where they actually lead.
func TestHelpShowsJumpBindings(t *testing.T) {
	t.Parallel()

	m := jumpBound(t)
	help := strings.Join(m.helpLines(), "\n")
	if !strings.Contains(help, "g w") || !strings.Contains(help, "space Work") {
		t.Errorf("help does not list the space chord:\n%s", help)
	}
	// A room binding is an ID in the file and a name in the overlay, which is what
	// makes the ID bearable: nobody has to read one to know where a key goes.
	if !strings.Contains(help, "Dana Levi") {
		t.Error("help does not resolve the room chord to the room's name")
	}

	// And it says when one leads nowhere, which is what somebody comes to the overlay
	// to find out.
	keys := config.DefaultKeys()
	keys.Jump = config.Jumps{
		{Chord: "g z", Target: "room:!gone:x"},
	}
	gone := strings.Join(jumping(t).WithKeys(keys).helpLines(), "\n")
	if !strings.Contains(gone, "nothing here matches") {
		t.Errorf("help does not flag a target that resolves to nothing:\n%s", gone)
	}
}

// namesakes builds the case a display name cannot survive: one person reachable over
// three networks, so three DMs carry one name.
func namesakes(t *testing.T) Model {
	t.Helper()
	m := update(t, New(context.Background(), apitest.Nop{}, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!wa-dana:x", Name: "Dana Levi", IsDirect: true},
		{ID: "!dana:x", Name: "Dana Levi", IsDirect: true},
		{ID: "!slack-dana:x", Name: "Dana Levi", IsDirect: true},
	}})
	return sized(t, m.clearStatus())
}

// The case that drove the design: one person over three bridges is three rooms with one
// display name.
func TestNamesakesAreToldApartByID(t *testing.T) {
	t.Parallel()

	m := namesakes(t).WithConfigFile(filepath.Join(t.TempDir(), "config.toml"), config.Config{})
	m.focus = paneRooms
	rooms := m.filteredRooms()
	if len(rooms) != 3 {
		t.Fatalf("expected three namesakes, got %d", len(rooms))
	}
	// Onto the second of the three, whichever the list put there.
	want := rooms[1].ID
	next, _ := m.selectRoom(rooms[1])
	m = next

	m, _ = press(t, m, keyText("B"))
	if m.prompt.kind != promptJumpBind {
		t.Fatalf("s opened prompt %d, want the binding prompt", m.prompt.kind)
	}
	m = typePrompt(t, m, "g d")
	after, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	target, ok := after.keys.jumpFor("g d")
	if !ok {
		t.Fatalf("g d was not bound: %+v", after.keys.jumps)
	}
	if target.Kind != domain.JumpRoom || target.Name != string(want) {
		t.Errorf("bound %+v, want the ID of the room under the cursor (%s)", target, want)
	}

	// And it is on its way to the file, so it survives a restart.
	saved := deliver(t, after, cmd)
	written, err := os.ReadFile(saved.conf.path)
	if err != nil {
		t.Fatalf("read back the config: %v", err)
	}
	// A block per binding, which is the shape that makes a file of twelve of them
	// readable — the map it replaced wrote them all on one line.
	if !strings.Contains(string(written), "[[keys.jump]]\nchord = \"g d\"\ntarget = \"room:"+string(want)+"\"") {
		t.Errorf("the config does not carry the binding:\n%s", written)
	}
}

// The rail's half of the gesture: a space is recorded by name, which stays readable in
// the file and survives a rail rename, since the group's key is matched too.
func TestBindASpaceFromTheRail(t *testing.T) {
	t.Parallel()

	m := jumping(t).WithConfigFile(filepath.Join(t.TempDir(), "config.toml"), config.Config{})
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "Infra")
	m, _ = press(t, m, keyText("B"))
	if m.prompt.kind != promptJumpBind {
		t.Fatalf("s in the rail opened prompt %d, want the binding prompt", m.prompt.kind)
	}
	m = typePrompt(t, m, "g i")
	after, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	target, ok := after.keys.jumpFor("g i")
	if !ok {
		t.Fatalf("g i was not bound: %+v", after.keys.jumps)
	}
	if target.Kind != domain.JumpSpace || target.Name != "Infra" {
		t.Errorf("bound %+v, want space:Infra", target)
	}
	// The key works from the moment it is written: applyConfig rebuilds the keymap in
	// the same frame that writes the file.
	moved := after.clearStatus()
	moved.rail.cursor = indexOfGroup(moved.rail.groups, "Work")
	moved, _ = press(t, moved, keyText("g"))
	jumped, _ := press(t, moved, keyText("i"))
	if got := jumped.rail.groups[jumped.rail.cursor].key; got != "Infra" {
		t.Errorf("the new binding took us to %q", got)
	}
}

// The prompt opens prefilled with whatever already points at the place, and an empty
// sequence removes it — the only spelling of "not any more" a one-line prompt has.
func TestBindPromptPrefillsTheExistingSequence(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Keys: config.DefaultKeys()}
	cfg.Keys.Jump = config.Jumps{{Chord: "g d", Target: "room:!dana:x"}}
	m := namesakes(t).WithKeys(cfg.Keys).WithConfigFile(filepath.Join(t.TempDir(), "config.toml"), cfg)
	m.focus = paneRooms
	next, _ := m.selectRoom(roomByName(t, m, "!dana:x"))
	m = next

	m, _ = press(t, m, keyText("B"))
	// Spelled the way the help overlay spells it — "gd", not "g d" — since that is what
	// the user reads everywhere else, and it is accepted back either way.
	if got := m.editorFor(fieldPrompt).text; got != "gd" {
		t.Errorf("prompt = %q, want the sequence already bound to that room", got)
	}

	cleared, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	cleared, _ = press(t, cleared, tea.KeyPressMsg{Code: tea.KeyBackspace})
	after, _ := press(t, cleared, tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, still := after.keys.jumpFor("g d"); still {
		t.Error("an empty sequence did not unbind the place")
	}
	if !strings.Contains(after.status(), "unbound") {
		t.Errorf("status = %q, want it to say what happened", after.status())
	}
}

// A sequence that collides is refused with the reason, and nothing is written.
func TestBindRefusesACollidingSequence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		typed, want string
	}{
		{"gg", "already bound to"}, // nav.select_oldest
		{"g", "start of a longer"}, // the gg chord would become unreachable
		{"q x", "on its own"},      // q quits before x arrives
	} {
		t.Run(tc.typed, func(t *testing.T) {
			m := jumping(t).WithConfigFile(filepath.Join(t.TempDir(), "config.toml"), config.Config{})
			m.focus = paneRail
			m.rail.cursor = indexOfGroup(m.rail.groups, "Infra")
			m, _ = press(t, m, keyText("B"))
			m = typePrompt(t, m, tc.typed)
			after, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

			if len(after.keys.jumps) != 0 {
				t.Errorf("bound %+v anyway", after.keys.jumps)
			}
			if !strings.Contains(after.status(), tc.want) {
				t.Errorf("status = %q, want it to explain %q", after.status(), tc.want)
			}
		})
	}
}

// A thread row has no shortcut of its own: a jump opens a room, and binding the room
// around the thread would answer a question nobody asked.
func TestThreadRowHasNoShortcut(t *testing.T) {
	t.Parallel()

	m := update(t, jumping(t), unreadUpdateMsg{u: domain.Unread{
		RoomID: "!a:x", Counted: true, Messages: 2,
		Threads: []domain.ThreadUnread{{Root: "$root", Unread: 2, Latest: "$r2", Title: "ship it"}},
	}})
	m.focus = paneRooms
	next, _ := m.selectRoom(roomByName(t, m, "!a:x"))
	m = next
	next, _ = m.stepRow(1)
	m = next
	row, ok := m.selectedRow()
	if !ok || !row.isThread() {
		t.Fatalf("the cursor is not on a thread row: %+v", row)
	}

	after, _ := press(t, m, keyText("B"))
	if after.prompt.active() {
		t.Error("a thread row opened the binding prompt")
	}
	if !strings.Contains(after.status(), "thread") {
		t.Errorf("status = %q, want it to say why", after.status())
	}
}

// jumped goes somewhere through the switcher, the way a person does: ctrl+k, type
// enough of the name, enter.
func jumped(t *testing.T, m Model, name string) Model {
	t.Helper()
	opened, _ := press(t, m, jumpKey())
	filtered := typeFilter(t, opened, name)
	chosen, _ := press(t, filtered, tea.KeyPressMsg{Code: tea.KeyEnter})
	return chosen
}

// ctrl+o goes back to the room you jumped away from and alt+o returns — the pair that
// answers "two conversations at once" without splitting the screen.
func TestBackAndForwardFollowJumps(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m = jumped(t, m, "Alpha")
	m = jumped(t, m, "Ops")
	if m.openRoom != "!ops:x" {
		t.Fatalf("open room = %s, want !ops:x", m.openRoom)
	}

	back, _ := press(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if back.openRoom != "!a:x" {
		t.Fatalf("ctrl+o opened %s, want the room jumped from (!a:x)", back.openRoom)
	}
	// The room list follows: the rail moved to a group that holds it, or the highlight
	// and the timeline would disagree.
	if _, ok := back.currentRoom(); !ok {
		t.Error("after going back the room list has no row for the open room")
	}

	// ctrl+i is the vim key and the documented one; alt+o is the fallback for a
	// terminal that cannot tell ctrl+i from tab.
	for _, forward := range []tea.KeyPressMsg{
		{Code: 'i', Mod: tea.ModCtrl},
		{Code: 'o', Mod: tea.ModAlt},
	} {
		fwd, _ := press(t, back, forward)
		if fwd.openRoom != "!ops:x" {
			t.Fatalf("%s opened %s, want !ops:x", forward.String(), fwd.openRoom)
		}
		// And back again — the round trip must not have consumed the entry.
		again, _ := press(t, fwd, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		if again.openRoom != "!a:x" {
			t.Errorf("ctrl+o after %s opened %s, want !a:x", forward.String(), again.openRoom)
		}
	}
	// tab still moves between panes: the two keys are one byte apart in history and
	// this is the assertion that says they are not one binding here.
	tabbed, _ := press(t, back, tea.KeyPressMsg{Code: tea.KeyTab})
	if tabbed.openRoom != back.openRoom {
		t.Errorf("tab moved the open room to %s, want it left alone", tabbed.openRoom)
	}
}

// Walking the room list is not a jump.
func TestWalkingTheListRecordsNothing(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m.focus = paneRooms
	for range 2 {
		next, _ := press(t, m, keyText("j"))
		m = next
	}
	if len(m.jumps.back) != 0 {
		t.Errorf("walking the list recorded %v, want nothing", m.jumps.back)
	}
	nothing, _ := press(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !strings.Contains(nothing.status(), "nothing to go back to") {
		t.Errorf("ctrl+o with nowhere to go said %q", nothing.status())
	}
}

// A room that has gone away since you left it is skipped rather than reported: the key
// is for getting back to a conversation, and stopping on a dead entry makes the second
// press look as broken as the first.
func TestBackSkipsARoomThatIsGone(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m = jumped(t, m, "Alpha")
	m = jumped(t, m, "Dana")
	m = jumped(t, m, "Ops")
	// Alpha and Dana are both behind us; Dana leaves the list.
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!ops:x", Name: "Ops"},
	}})
	back, _ := press(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if back.openRoom != "!a:x" {
		t.Errorf("ctrl+o opened %s, want !a:x — the gone room should have been stepped over", back.openRoom)
	}
}

// openedFromList is the ordinary way a room gets opened: the cursor walks onto it
// (which is all selectRoom is) and enter opens it.
func openedFromList(t *testing.T, m Model, id domain.RoomID) Model {
	t.Helper()
	room, listed := m.rooms.byID(id)
	if !listed {
		t.Fatalf("%s is not in the room list", id)
	}
	next, _ := m.selectRoom(room)
	next.focus, next.compose.insertMode = paneRooms, false
	opened, _ := press(t, next, tea.KeyPressMsg{Code: tea.KeyEnter})
	return opened
}

// Opening rooms from the list — enter, three times — has to be what ctrl+o walks back
// through.
func TestBackWalksRoomsOpenedFromTheList(t *testing.T) {
	t.Parallel()

	m := jumping(t)
	m = openedFromList(t, m, "!a:x")
	m = openedFromList(t, m, "!dana:x")
	m = openedFromList(t, m, "!ops:x")

	back, _ := press(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if back.openRoom != "!dana:x" {
		t.Fatalf("first ctrl+o opened %s, want !dana:x", back.openRoom)
	}
	again, _ := press(t, back, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if again.openRoom != "!a:x" {
		t.Fatalf("second ctrl+o opened %s, want !a:x", again.openRoom)
	}
	// The first room opened is the end of the road: there was nowhere before it.
	end, _ := press(t, again, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !strings.Contains(end.status(), "nothing to go back to") {
		t.Errorf("a third ctrl+o said %q, want it to stop at the first room opened", end.status())
	}
	// And forward retraces exactly the same steps.
	fwd, _ := press(t, again, tea.KeyPressMsg{Code: 'i', Mod: tea.ModCtrl})
	if fwd.openRoom != "!dana:x" {
		t.Errorf("ctrl+i opened %s, want !dana:x", fwd.openRoom)
	}
}

// An overlay is not a place.
func TestOverlaysAreNotPlaces(t *testing.T) {
	t.Parallel()

	m := jumped(t, jumping(t), "Alpha")
	m = jumped(t, m, "Ops")
	before := len(m.jumps.back)

	for _, open := range []tea.KeyPressMsg{
		keyText("/"),           // search this room
		{Code: '@', Text: "@"}, // the mentions list
		jumpKey(),              // the switcher
	} {
		shown, _ := press(t, m, open)
		if len(shown.jumps.back) != before {
			t.Errorf("%s recorded a place: back = %v", open.String(), shown.jumps.back)
		}
		closed, _ := press(t, shown, tea.KeyPressMsg{Code: tea.KeyEscape})
		if len(closed.jumps.back) != before || closed.jumps.anchor != m.jumps.anchor {
			t.Errorf("%s then esc left back=%v anchor=%s, want them untouched",
				open.String(), closed.jumps.back, closed.jumps.anchor)
		}
	}
}
