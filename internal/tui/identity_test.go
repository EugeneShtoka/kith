package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// aliasPeople are the room's members, including two accounts of one person, one bridged.
var aliasPeople = []domain.Member{
	{UserID: "matrix:@whatsapp_4470:x", DisplayName: "Dana Levi (WA)"},
	{UserID: "matrix:@dana:x", DisplayName: "Dana Levi"},
	{UserID: "matrix:@bob:x", DisplayName: "Bob Stone"},
}

// aliasing returns a model with a config file, a room, its members and one message, plus the
// config path.
func aliasing(t *testing.T, display config.Display) (Model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	base := config.Config{Matrix: config.Matrix{Accounts: []config.MatrixAccount{{Name: "me", Homeserver: "https://x", User: "@me:x"}}}, Display: display, Tags: starter(t).Tags, Profiles: starter(t).Profiles}
	if err := config.Save(path, base); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	m := update(t, starterNew(apitest.Nop{}, display),
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}})
	m = sized(t, m).WithConfigFile(path, asStarted(base)).keptIn()
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, membersMsg{roomID: mx + "!a:x", members: aliasPeople})
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@whatsapp_4470:x", SenderName: "Dana Levi (WA)", Body: "hello"},
	}}})
	m = m.clearStatus()
	return m, path
}

// run presses a key and executes the returned command, so a save happens.
func run(t *testing.T, m Model, key tea.KeyPressMsg) Model {
	t.Helper()
	next, cmd := press(t, m, key)
	return deliver(t, next, cmd)
}

// typeText types into whatever prompt or filter is open.
func typeText(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		m, _ = press(t, m, keyText(string(r)))
	}
	return m
}

// p on the room, a on a person, name, color: the setting is applied and written.
func TestAliasFlowFromPeoplePicker(t *testing.T) {
	t.Parallel()

	m, path := aliasing(t, config.Display{})
	m.focus = paneRooms

	m, _ = press(t, m, keyText("p"))
	if m.picker.kind != pickerPeople {
		t.Fatalf("p should open the people picker, got kind %v", m.picker.kind)
	}
	// The picker shows the MXID detail.
	body := stripStyles(strings.Join(m.pickerLines(80, 8), "\n"))
	if !strings.Contains(body, "matrix:@whatsapp_4470:x") {
		t.Errorf("the people picker must show MXIDs — that is what it is for:\n%s", body)
	}

	m, _ = press(t, m, keyText("a"))
	if m.picker.kind != pickerIdentity {
		t.Fatalf("a should open the identity picker, got kind %v", m.picker.kind)
	}
	if m.choosing.identity.mxid != "matrix:@whatsapp_4470:x" {
		t.Errorf("pending mxid = %q, want the selected person", m.choosing.identity.mxid)
	}

	// "+ new person" is the only entry with no identities configured.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.prompt.kind != promptAlias {
		t.Fatalf("choosing a new person should prompt for a name, got %v", m.prompt.kind)
	}
	for range len(m.prompt.input) {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m = typeText(t, m, "Dana")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.picker.kind != pickerColor {
		t.Fatalf("naming should lead to the color picker, got %v", m.picker.kind)
	}

	m = typeText(t, m, "green")
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// Applied to the running app, not just written.
	if len(m.prefs.display.Identities) != 1 {
		t.Fatalf("identities = %+v, want one", m.prefs.display.Identities)
	}
	got := m.prefs.display.Identities[0]
	if got.Alias != "Dana" || got.Color != "#8ff586" || len(got.IDs) != 1 || got.IDs[0] != "matrix:@whatsapp_4470:x" {
		t.Errorf("identity = %+v", got)
	}
	if ident, ok := m.prefs.identities["matrix:@whatsapp_4470:x"]; !ok || ident.alias != "Dana" || !ident.pinned {
		t.Errorf("the model's identity map was not rebuilt: %+v", ident)
	}
	if !strings.Contains(m.status(), "Dana") {
		t.Errorf("status = %q, should name what changed", m.status())
	}

	// And written, so it survives a restart.
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("the written config does not load: %v", err)
	}
	if len(reloaded.Display.Identities) != 1 || reloaded.Display.Identities[0].Alias != "Dana" {
		body, _ := os.ReadFile(path)
		t.Errorf("config did not persist the identity:\n%s", body)
	}
}

// a on a message goes straight to naming its sender.
func TestAliasFlowFromMessage(t *testing.T) {
	t.Parallel()

	m, _ := aliasing(t, config.Display{})
	m.focus = paneTimeline
	m.compose.insertMode = false

	m, _ = press(t, m, keyText("a"))
	if m.picker.kind != pickerIdentity {
		t.Fatalf("a on a message should open the identity picker, got %v", m.picker.kind)
	}
	if m.choosing.identity.mxid != "matrix:@whatsapp_4470:x" {
		t.Errorf("pending = %+v, want the message's sender", m.choosing.identity)
	}
	if m.choosing.identity.name == "" {
		t.Error("the pending edit should carry a name for the prompts")
	}
}

// Adding an account to an existing person must not leave them in two identities.
func TestAliasAddsToExistingIdentity(t *testing.T) {
	t.Parallel()

	m, path := aliasing(t, config.Display{Identities: []config.Identity{
		{Alias: "Dana", Color: "#8ff586", IDs: []string{"matrix:@dana:x"}},
		{Alias: "Someone else", IDs: []string{"matrix:@whatsapp_4470:x", "matrix:@keepme:x"}},
	}})
	m.focus = paneRooms

	m, _ = press(t, m, keyText("p"))
	m, _ = press(t, m, keyText("a")) // the bridged Dana, first in the list
	if m.choosing.identity.mxid != "matrix:@whatsapp_4470:x" {
		t.Fatalf("pending = %+v", m.choosing.identity)
	}
	// Existing identities are offered, and the one already holding this MXID says so.
	labels := map[string]string{}
	for _, item := range m.picker.items {
		labels[item.label] = item.detail
	}
	if !strings.Contains(labels["Someone else"], "already here") {
		t.Errorf("the identity already holding this account should say so, got %q", labels["Someone else"])
	}

	// Filter to Dana and choose it.
	m = typeText(t, m, "Dana")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.picker.kind != pickerColor {
		t.Fatalf("choosing an existing identity should go to the color, got %v", m.picker.kind)
	}
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // "— no color —"

	byAlias := map[string]config.Identity{}
	for _, ident := range m.prefs.display.Identities {
		byAlias[ident.Alias] = ident
	}
	dana := byAlias["Dana"]
	if len(dana.IDs) != 2 {
		t.Errorf("Dana = %+v, want both accounts", dana)
	}
	// Moved, not copied: the old identity no longer claims them.
	other := byAlias["Someone else"]
	for _, mxid := range other.IDs {
		if mxid == "matrix:@whatsapp_4470:x" {
			t.Error("the account is still in its old identity — belonging to two people at once")
		}
	}
	if len(other.IDs) != 1 || other.IDs[0] != "matrix:@keepme:x" {
		t.Errorf("the old identity's other accounts should be untouched, got %+v", other)
	}
	if _, err := config.Load(path); err != nil {
		t.Errorf("written config does not load: %v", err)
	}
}

// An identity emptied by a move is dropped.
func TestAliasDropsEmptiedIdentity(t *testing.T) {
	t.Parallel()

	m, _ := aliasing(t, config.Display{Identities: []config.Identity{
		{Alias: "Only", IDs: []string{"matrix:@whatsapp_4470:x"}},
		{Alias: "Target", IDs: []string{"matrix:@dana:x"}},
	}})
	m.focus = paneRooms
	m, _ = press(t, m, keyText("p"))
	m, _ = press(t, m, keyText("a"))
	m = typeText(t, m, "Target")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	for _, ident := range m.prefs.display.Identities {
		if ident.Alias == "Only" {
			t.Errorf("an identity left with nobody in it should be dropped, got %+v", ident)
		}
	}
}

// Backing out at any step changes nothing; the flow writes only at the end.
func TestAliasFlowIsAbandonable(t *testing.T) {
	t.Parallel()

	steps := map[string]func(*testing.T, Model) Model{
		"at the people picker": func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyText("p"))
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			return m
		},
		"at the identity picker": func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyText("p"))
			m, _ = press(t, m, keyText("a"))
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			return m
		},
		"at the name prompt": func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyText("p"))
			m, _ = press(t, m, keyText("a"))
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			return m
		},
		"at the color picker": func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keyText("p"))
			m, _ = press(t, m, keyText("a"))
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m = typeText(t, m, "X")
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
			return m
		},
	}
	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, path := aliasing(t, config.Display{})
			before, _ := os.ReadFile(path)
			m.focus = paneRooms
			m = step(t, m)
			if len(m.prefs.display.Identities) != 0 {
				t.Errorf("backing out %s created an identity: %+v", name, m.prefs.display.Identities)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Errorf("backing out %s wrote to the config", name)
			}
		})
	}
}

// An empty name abandons the edit rather than creating a nameless person.
func TestAliasRejectsEmptyName(t *testing.T) {
	t.Parallel()

	m, _ := aliasing(t, config.Display{})
	m.focus = paneRooms
	m, _ = press(t, m, keyText("p"))
	m, _ = press(t, m, keyText("a"))
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for range len(m.prompt.input) {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m, cmd := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if untimed(t, cmd) != nil {
		t.Error("an empty name should not save anything")
	}
	if len(m.prefs.display.Identities) != 0 {
		t.Errorf("an empty name created %+v", m.prefs.display.Identities)
	}
	if m.choosing.identity.mxid != "" {
		t.Error("the abandoned edit should be cleared")
	}
	if m.status() == "" {
		t.Errorf("status = %q, should explain why nothing happened", m.status())
	}
}

// The people picker is modal, which is why `a` can mean "set alias" there.
func TestPeoplePickerIsModal(t *testing.T) {
	t.Parallel()

	m, _ := aliasing(t, config.Display{})
	m.focus = paneRooms
	m, _ = press(t, m, keyText("p"))
	if m.picker.mode != pickerNavigate {
		t.Fatal("the people picker must start in navigate mode so `a` is an action")
	}
	// i switches to filtering, where the same letter is text.
	m, _ = press(t, m, keyText("i"))
	m, _ = press(t, m, keyText("a"))
	if m.picker.filter != "a" {
		t.Errorf("filter = %q, want the letter typed once filtering", m.picker.filter)
	}
	if m.picker.kind != pickerPeople {
		t.Error("filtering should not have triggered the alias action")
	}
	// Filtering matches MXIDs as well as names.
	for _, r := range "whatsapp" {
		m, _ = press(t, m, keyText(string(r)))
	}
	m.picker.filter = "whatsapp"
	m.picker = m.picker.refilter()
	if len(m.picker.items) != 1 || m.picker.items[0].value != "matrix:@whatsapp_4470:x" {
		t.Errorf("filtering by MXID gave %+v", m.picker.items)
	}
}

// With no config path nothing is written and that is reported; the setting still applies.
func TestAliasWithoutConfigPath(t *testing.T) {
	t.Parallel()

	m := update(t, starterNew(apitest.Nop{}, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}})
	m = sized(t, m) // no WithConfigFile
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, membersMsg{roomID: mx + "!a:x", members: aliasPeople})
	m.focus = paneRooms

	m, _ = press(t, m, keyText("p"))
	m, _ = press(t, m, keyText("a"))
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeText(t, m, "X")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(m.prefs.display.Identities) != 1 {
		t.Error("the change should still apply to the running session")
	}
	if !strings.Contains(m.status(), "not saved") {
		t.Errorf("status = %q, should say it was not saved", m.status())
	}
}

// p is inert where there is nothing to list.
func TestPeoplePickerNeedsMembers(t *testing.T) {
	t.Parallel()

	m, _ := aliasing(t, config.Display{})
	m.timeline.members = nil
	m.focus = paneRooms
	m, _ = press(t, m, keyText("p"))
	if m.picker.active() {
		t.Error("p should not open an empty people picker")
	}
	if m.status() == "" {
		t.Errorf("status = %q, should say why", m.status())
	}
}
