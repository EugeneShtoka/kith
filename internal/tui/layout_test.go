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
	m = sized(t, m).WithConfigFile(path, base).keptIn()
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
		Priority:   []string{"Friends", "Work"},
	})
	// A DM named after one person, in Friends *and* Work, ranked so that Friends —
	// which has no rule — is the room's own space (the DMs tag the rail shows first
	// would be otherwise).
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
