package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// laidOut returns a model with two spaces, two rooms and a config file to write to.
func laidOut(t *testing.T, display config.Display) (Model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	base := config.Config{Homeserver: "https://x", User: "@me:x", Display: display, Tags: starter(t).Tags}
	base.Keys.FillDefaults()
	if err := config.Save(path, base); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	m := update(t, New(t.Context(), apitest.Nop{}, display).WithConfigFile("", base),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}, {ID: "!b:x", Name: "Bravo"}}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
		{ID: "!f:x", Name: "Friends", Children: []domain.RoomID{"!b:x"}},
	}})
	m = sized(t, m).WithConfigFile(path, base)
	m = m.clearStatus()
	return m, path
}

// railKeys is the rail's current order.
func railKeys(m Model) []string {
	out := make([]string, 0, len(m.rail.groups))
	for _, g := range m.rail.groups {
		out = append(out, g.key)
	}
	return out
}

// A room's displayed name is keyed on its ID, which the UI shows nowhere — so this is
// the only practical way to set one.
func TestRenameRoom(t *testing.T) {
	t.Parallel()

	m, path := laidOut(t, config.Display{})
	m.focus = paneRooms
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next

	m, _ = press(t, m, keyText("a"))
	if m.prompt.kind != promptRoomName {
		t.Fatalf("R should open the rename prompt, got %v", m.prompt.kind)
	}
	// Prefilled: you are adjusting a name, not inventing one.
	if m.prompt.input != "Alpha" {
		t.Errorf("prompt input = %q, want the current name", m.prompt.input)
	}
	for range len(m.prompt.input) {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "Team Alpha" {
		m, _ = press(t, m, keyText(string(r)))
	}
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting should save")
	}
	runCmd(t, cmd)

	if len(m.prefs.display.Names) != 1 || m.prefs.display.Names[0].Name != "Team Alpha" {
		t.Fatalf("names = %+v", m.prefs.display.Names)
	}
	// Applied in place, not just written.
	room, _ := m.roomByID("!a:x")
	if got := m.roomName(room); got != "Team Alpha" {
		t.Errorf("roomName = %q, want the new name immediately", got)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Display.Names) != 1 {
		t.Error("the rename did not persist")
	}
}

// An empty name clears the override rather than setting a blank one: a room with no
// name renders as its ID, which is never what typing nothing meant.
func TestRenameRoomEmptyClears(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{
		Names: []config.DisplayName{{Target: "!a:x", Name: "Old Name"}},
	})
	m.focus = paneRooms
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next

	m, _ = press(t, m, keyText("a"))
	if m.prompt.input != "Old Name" {
		t.Errorf("prompt should be prefilled with the override, got %q", m.prompt.input)
	}
	for range len(m.prompt.input) {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		runCmd(t, cmd)
	}
	if len(m.prefs.display.Names) != 0 {
		t.Errorf("names = %+v, want the entry removed rather than blanked", m.prefs.display.Names)
	}
	room, _ := m.roomByID("!a:x")
	if got := m.roomName(room); got != "Alpha" {
		t.Errorf("roomName = %q, want the room's own name back", got)
	}
	if !strings.Contains(m.status(), "cleared") {
		t.Errorf("status = %q, should say the override was cleared", m.status())
	}
}

// Naming one thing leaves the others alone, replaces rather than duplicates, and clears
// rather than blanking.
func TestSetName(t *testing.T) {
	t.Parallel()

	start := []config.DisplayName{
		{Target: "!a:x", Name: "A"},
		{Target: config.NameTargetSpace + "Work", Name: "B"},
	}
	replaced := config.SetName(start, "!a:x", "A2")
	if len(replaced) != 2 {
		t.Fatalf("replacing gave %+v, want two entries", replaced)
	}
	got := config.Display{Names: replaced}
	if got.NameFor("!a:x") != "A2" || got.NameFor(config.NameTargetSpace+"Work") != "B" {
		t.Errorf("names = %+v", replaced)
	}
	if added := config.SetName(start, config.NameTargetThread+"$t:x", "C"); len(added) != 3 {
		t.Errorf("adding gave %+v, want three", added)
	}
	cleared := config.SetName(start, "!a:x", "")
	if len(cleared) != 1 || cleared[0].Target != config.NameTargetSpace+"Work" {
		t.Errorf("clearing gave %+v, want only the other entry", cleared)
	}
	// The input is never modified — these are config values shared with the model.
	if len(start) != 2 || start[0].Name != "A" {
		t.Errorf("the input was mutated: %+v", start)
	}
}

// Moving a group carries the cursor with it.
func TestMoveGroupCarriesTheCursor(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{})
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")

	m, _ = press(t, m, keyCode('K'))
	if got := railKeys(m); strings.Join(got, ",") != "tag:DMs,tag:Unread,Work,tag:All,Friends" {
		t.Fatalf("after one move: %v", got)
	}
	if m.rail.groups[m.rail.cursor].key != "Work" {
		t.Fatalf("cursor is on %q, want the moved group", m.rail.groups[m.rail.cursor].key)
	}
	// A second press moves the same group again rather than undoing the first.
	m, _ = press(t, m, keyCode('K'))
	if got := railKeys(m); strings.Join(got, ",") != "tag:DMs,Work,tag:Unread,tag:All,Friends" {
		t.Errorf("after two moves: %v", got)
	}
	// And down again.
	m, _ = press(t, m, keyCode('J'))
	if got := railKeys(m); strings.Join(got, ",") != "tag:DMs,tag:Unread,Work,tag:All,Friends" {
		t.Errorf("after moving back down: %v", got)
	}
}

// The move clamps at both ends rather than wrapping — a group that jumped from the top
// to the bottom would be a surprise, not a shortcut.
func TestMoveGroupClamps(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{})
	m.focus = paneRail
	before := strings.Join(railKeys(m), ",")

	m.rail.cursor = 0
	m, cmd := press(t, m, keyCode('K'))
	if cmd != nil {
		t.Error("moving the first group up should do nothing")
	}
	m.rail.cursor = len(m.rail.groups) - 1
	m, cmd = press(t, m, keyCode('J'))
	if cmd != nil {
		t.Error("moving the last group down should do nothing")
	}
	if got := strings.Join(railKeys(m), ","); got != before {
		t.Errorf("rail changed: %s -> %s", before, got)
	}
}

// The first move writes the whole order out, because "some keys listed and the rest
// following" cannot express "this one is third".
func TestMoveGroupMaterializesTheOrder(t *testing.T) {
	t.Parallel()

	m, path := laidOut(t, config.Display{})
	if len(m.prefs.display.Rail.Order) != 0 {
		t.Fatal("the fixture should start with no explicit order")
	}
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "Friends")
	m, cmd := press(t, m, keyCode('K'))
	if cmd == nil {
		t.Fatal("a move should save")
	}
	runCmd(t, cmd)

	order := m.prefs.display.Rail.Order
	if len(order) != len(m.rail.groups) {
		t.Errorf("order = %v, want every group listed", order)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Display.Rail.Order) != len(order) {
		t.Error("the order did not persist")
	}
}

// A separator is a divider, not something a group takes turns with: moving past one
// carries the group over it and the divider stays where it was.
func TestMoveGroupPreservesSeparators(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{Rail: config.Rail{
		Order: []string{homeGroupKey, dmsGroupKey, "-", "Work", "Friends"},
	}})
	m.focus = paneRail
	sepAfterKey := func(mm Model) string {
		for _, g := range mm.rail.groups {
			if g.sepAfter {
				return g.key
			}
		}
		return ""
	}
	if sepAfterKey(m) != dmsGroupKey {
		t.Fatalf("fixture: separator after %q", sepAfterKey(m))
	}
	// The fixture's order lists four groups, so the unlisted `unread` follows them.
	if got := strings.Join(railKeys(m), ","); got != "tag:All,tag:DMs,Work,Friends,tag:Unread" {
		t.Fatalf("fixture rail = %s", got)
	}
	// Move Work above the separator.
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	m, _ = press(t, m, keyCode('K'))
	if got := strings.Join(railKeys(m), ","); got != "tag:All,Work,tag:DMs,Friends,tag:Unread" {
		t.Errorf("order = %s", got)
	}
	// The divider is still after dms, not attached to what moved.
	if got := sepAfterKey(m); got != dmsGroupKey {
		t.Errorf("separator now after %q, want it to stay after dms", got)
	}
}

// Hiding must be undoable from inside the app: a rail edit you can only reverse by
// finding the config file is a trap, not a setting.
func TestHideAndShowGroup(t *testing.T) {
	t.Parallel()

	m, path := laidOut(t, config.Display{})
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, dmsGroupKey)

	m, cmd := press(t, m, keyCode('H'))
	if cmd == nil {
		t.Fatal("hiding should save")
	}
	runCmd(t, cmd)
	if indexOfGroupExact(m.rail.groups, dmsGroupKey) >= 0 {
		t.Errorf("dms is still in the rail: %v", railKeys(m))
	}
	// The message says how to undo it, because otherwise nothing does.
	if !strings.Contains(m.status(), "bring it back") {
		t.Errorf("status = %q, should say hiding is reversible", m.status())
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Display.Rail.Hidden) != 1 {
		t.Error("the hide did not persist")
	}

	// Bring it back.
	m, _ = press(t, m, keyCode('S'))
	if m.picker.kind != pickerHidden {
		t.Fatalf("S should offer the hidden groups, got %v", m.picker.kind)
	}
	if len(m.picker.items) != 1 || m.picker.items[0].value != dmsGroupKey {
		t.Errorf("picker items = %+v", m.picker.items)
	}
	// The label is shown, not just the key — a hidden group is not in m.groups, so its
	// label has to come from elsewhere.
	if m.picker.items[0].label != "DMs" {
		t.Errorf("label = %q, want the group's displayed name", m.picker.items[0].label)
	}
	m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		runCmd(t, cmd)
	}
	if indexOfGroupExact(m.rail.groups, dmsGroupKey) < 0 {
		t.Errorf("dms did not come back: %v", railKeys(m))
	}
	if len(m.prefs.display.Rail.Hidden) != 0 {
		t.Errorf("hidden = %v, want it emptied", m.prefs.display.Rail.Hidden)
	}
}

// With nothing hidden there is nothing to offer, and it says so rather than opening an
// empty list.
func TestShowHiddenWithNoneHidden(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{})
	m.focus = paneRail
	m, _ = press(t, m, keyCode('S'))
	if m.picker.active() {
		t.Error("no hidden groups should not open a picker")
	}
	if !strings.Contains(m.status(), "no hidden") {
		t.Errorf("status = %q", m.status())
	}
}

// The last group cannot be hidden — an empty rail has nothing to navigate.
func TestCannotHideTheLastGroup(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{Rail: config.Rail{
		Hidden: []string{dmsGroupKey, unreadGroupKey, "Work", "Friends"},
	}})
	m.focus = paneRail
	if len(m.rail.groups) != 1 {
		t.Fatalf("fixture has %d groups, want 1", len(m.rail.groups))
	}
	m, cmd := press(t, m, keyCode('H'))
	if untimed(t, cmd) != nil {
		t.Error("hiding the last group should do nothing")
	}
	if len(m.rail.groups) != 1 {
		t.Error("the rail was emptied")
	}
	if !strings.Contains(m.status(), "only group") {
		t.Errorf("status = %q, should say why", m.status())
	}
}

// Renaming a rail group changes its label but not its key, so ordering, hiding and
// space rules keep addressing it.
func TestRenameGroup(t *testing.T) {
	t.Parallel()

	m, path := laidOut(t, config.Display{})
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")

	m, _ = press(t, m, keyText("a"))
	if m.prompt.kind != promptGroupName || m.prompt.input != "Work" {
		t.Fatalf("prompt = %v %q", m.prompt.kind, m.prompt.input)
	}
	for range len(m.prompt.input) {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "Day Job" {
		m, _ = press(t, m, keyText(string(r)))
	}
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		runCmd(t, cmd)
	}
	at := indexOfGroupExact(m.rail.groups, "Work")
	if at < 0 {
		t.Fatalf("the key changed: %v", railKeys(m))
	}
	if m.rail.groups[at].label != "Day Job" {
		t.Errorf("label = %q, want the new one", m.rail.groups[at].label)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Display.NameFor(config.NameTargetSpace+"Work") != "Day Job" {
		t.Errorf("names = %v", reloaded.Display.Names)
	}

	// Clearing it restores the group's own name.
	m, _ = press(t, m, keyText("a"))
	for range len(m.prompt.input) {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m, cmd = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		runCmd(t, cmd)
	}
	at = indexOfGroupExact(m.rail.groups, "Work")
	if m.rail.groups[at].label != "Work" {
		t.Errorf("label = %q, want the default back", m.rail.groups[at].label)
	}
	if len(m.prefs.display.Names) != 0 {
		t.Errorf("names = %v, want the entry removed", m.prefs.display.Names)
	}
}

// The first-name rule is a two-state toggle, so it is a keystroke rather than a picker.
func TestToggleFirstNameOnly(t *testing.T) {
	t.Parallel()

	m, path := laidOut(t, config.Display{})
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")

	m, cmd := press(t, m, keyCode('F'))
	if cmd != nil {
		runCmd(t, cmd)
	}
	if len(m.prefs.display.SpaceRules) != 1 || !m.prefs.display.SpaceRules[0].FirstNameOnly {
		t.Fatalf("rules = %+v, want it on for Work", m.prefs.display.SpaceRules)
	}
	if m.prefs.display.SpaceRules[0].Space != "Work" {
		t.Errorf("rule is for %q, want the group key", m.prefs.display.SpaceRules[0].Space)
	}
	if !strings.Contains(m.status(), "on") {
		t.Errorf("status = %q, should say which way it went", m.status())
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Display.SpaceRules) != 1 {
		t.Error("the rule did not persist")
	}

	// Off again, and the inert entry is dropped rather than kept as false.
	m, cmd = press(t, m, keyCode('F'))
	if cmd != nil {
		runCmd(t, cmd)
	}
	if len(m.prefs.display.SpaceRules) != 0 {
		t.Errorf("rules = %+v, want the entry removed when switched off", m.prefs.display.SpaceRules)
	}
}

// A tag's row takes a name rule as a space's does, written against tag:<name>.
func TestFirstNameOnlyOnATag(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{})
	m.focus = paneRail
	for _, key := range []string{homeGroupKey, dmsGroupKey, unreadGroupKey} {
		m.rail.cursor = indexOfGroup(m.rail.groups, key)
		next, _ := press(t, m, keyCode('F'))
		if rules := next.prefs.display.SpaceRules; len(rules) != 1 || rules[0].Space != key || !rules[0].FirstNameOnly {
			t.Errorf("%s created %+v, want first names only for the tag", key, rules)
		}
	}
}

// Layout edits do not apply to an invitation: it is a decision, not a room to rename.
func TestRenameIgnoresInvites(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{})
	mdl, _ := m.handleInvites([]domain.Room{
		{ID: "!i:x", Name: "Invited", Membership: domain.MembershipInvite, InvitedBy: "@a:x"},
	})
	m = mdl
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, inviteGroupKey)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next

	m, _ = press(t, m, keyText("a"))
	if m.prompt.active() {
		t.Error("an invitation should not open the rename prompt")
	}
}

// indexOfGroupExact is indexOfGroup without its fall-back-to-zero, so a test can tell
// "not present" from "the first one".
func indexOfGroupExact(groups []group, key string) int {
	for i := range groups {
		if groups[i].key == key {
			return i
		}
	}
	return -1
}

// A room in two spaces that disagree: the **list row** is drawn with the rules of the
// space you are looking at, and the **sender labels** with the room's own.
func TestTheListFollowsTheSpaceAndTheTimelineDoesNot(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{
		SpaceRules: []config.SpaceRule{{Space: "Work", FirstNameOnly: true}},
	})
	// A DM named after one person, in Friends *and* Work, ordered so that Friends —
	// which has no rule — is the room's own space.
	dm := domain.Room{
		ID: "!a:x", Name: "Michael Livingston",
		Members: []string{"Michael Livingston"}, IsDirect: true,
	}
	m = update(t, m, roomsMsg{rooms: []domain.Room{dm, {ID: "!b:x", Name: "Bravo"}}})
	m = update(t, m, spacesMsg{spaces: []domain.Space{
		{ID: "!f:x", Name: "Friends", Children: []domain.RoomID{"!a:x", "!b:x"}},
		{ID: "!w:x", Name: "Work", Children: []domain.RoomID{"!a:x"}},
	}})
	if got := m.homesOf("!a:x"); len(got) == 0 || got[0] != "Friends" {
		t.Fatalf("precondition: homesOf = %v, want Friends first", got)
	}

	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	if got := m.roomLabelHere(dm); got != "Michael" {
		t.Errorf("listed under Work, the row is %q, want Work's rule applied", got)
	}
	// **The half that was got wrong the first time.** Opening the room from Work must
	// not reshape the conversation inside it.
	if got := m.shapedName("Michael Livingston", "!a:x"); got != "Michael Livingston" {
		t.Errorf("the sender label is %q, want the room's own space to decide", got)
	}

	m.rail.cursor = indexOfGroup(m.rail.groups, "Friends")
	if got := m.roomLabelHere(dm); got != "Michael Livingston" {
		t.Errorf("listed under Friends, the row is %q, want no rule applied", got)
	}
}

// The rail's selection is not always the room's context.
func TestASpanningGroupLeavesTheRoomsOwnSpaceToDecide(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{
		SpaceRules: []config.SpaceRule{{Space: "Work", FirstNameOnly: true}},
	})
	for _, key := range []string{homeGroupKey, dmsGroupKey, unreadGroupKey} {
		m.rail.cursor = indexOfGroup(m.rail.groups, key)
		if m.listedSpace("!a:x") != "Work" {
			t.Errorf("on %q, Alpha lost its own space", key)
		}
		if m.listedSpace("!b:x") == "Work" {
			t.Errorf("on %q, Bravo picked up a space that is not its own", key)
		}
	}
	// A room in no space at all has no rule to find, either way round — and still gets
	// the global half, which is what keeps the width cap working everywhere.
	if m.nameRuleFor(m.ownSpace("!nowhere:x")).firstOnly {
		t.Error("a room in no space matched a space rule")
	}
}

// The width cap is a sender-label setting and has never reached a room label.
func TestTheWidthCapIsForSenderLabelsOnly(t *testing.T) {
	t.Parallel()

	m, _ := laidOut(t, config.Display{MaxNameLength: 6})
	if got := m.shapedName("Michael Livingston", "!a:x"); got != "Micha…" {
		t.Errorf("sender label = %q, want it capped at 6", got)
	}
	long := domain.Room{ID: "!a:x", Name: "Beer & Escape Room Planning"}
	if got := m.roomLabel(long); got != "Beer & Escape Room Planning" {
		t.Errorf("room label = %q, want it uncapped", got)
	}
}
