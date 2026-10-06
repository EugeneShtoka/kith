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

// aliasRoom is the fixture the identity flows run against: a room whose members include
// two accounts of the same person, one of them bridged.
var aliasPeople = []domain.Member{
	{UserID: "@whatsapp_4470:x", DisplayName: "Dana Levi (WA)"},
	{UserID: "@dana:x", DisplayName: "Dana Levi"},
	{UserID: "@bob:x", DisplayName: "Bob Stone"},
}

// aliasing returns a model with a config file, a room, its members and one message,
// plus the path the config is written to.
func aliasing(t *testing.T, display config.Display) (Model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	base := config.Config{Homeserver: "https://x", User: "@me:x", Display: display, Tags: starter(t).Tags}
	base.Keys.FillDefaults()
	if err := config.Save(path, base); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	m := update(t, starterNew(apitest.Nop{}, display),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m).WithConfigFile(path, base).keptIn()
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, membersMsg{roomID: "!a:x", members: aliasPeople})
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@whatsapp_4470:x", SenderName: "Dana Levi (WA)", Body: "hello"},
	}}})
	m = m.clearStatus()
	return m, path
}

// run presses a key and executes whatever command comes back, so a save actually
// happens.
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

// The requested flow, end to end: p on the room, a on a person, name them, pick a color
// — and the setting is both applied and written.
func TestAliasFlowFromPeoplePicker(t *testing.T) {
	t.Parallel()

	m, path := aliasing(t, config.Display{})
	m.focus = paneRooms

	m, _ = press(t, m, keyText("p"))
	if m.picker.kind != pickerPeople {
		t.Fatalf("p should open the people picker, got kind %v", m.picker.kind)
	}
	// The picker shows the one thing you could not otherwise see.
	body := stripStyles(strings.Join(m.pickerLines(80, 8), "\n"))
	if !strings.Contains(body, "@whatsapp_4470:x") {
		t.Errorf("the people picker must show MXIDs — that is what it is for:\n%s", body)
	}

	m, _ = press(t, m, keyText("a"))
	if m.picker.kind != pickerIdentity {
		t.Fatalf("a should open the identity picker, got kind %v", m.picker.kind)
	}
	if m.choosing.identity.mxid != "@whatsapp_4470:x" {
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
	if got.Alias != "Dana" || got.Color != "#8ff586" || len(got.IDs) != 1 || got.IDs[0] != "@whatsapp_4470:x" {
		t.Errorf("identity = %+v", got)
	}
	if ident, ok := m.prefs.identities["@whatsapp_4470:x"]; !ok || ident.alias != "Dana" || !ident.pinned {
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

// The other requested path: a on a message goes straight to naming its sender, which is
// the common case — the person you want to name just said something.
func TestAliasFlowFromMessage(t *testing.T) {
	t.Parallel()

	m, _ := aliasing(t, config.Display{})
	m.focus = paneTimeline
	m.compose.insertMode = false

	m, _ = press(t, m, keyText("a"))
	if m.picker.kind != pickerIdentity {
		t.Fatalf("a on a message should open the identity picker, got %v", m.picker.kind)
	}
	if m.choosing.identity.mxid != "@whatsapp_4470:x" {
		t.Errorf("pending = %+v, want the message's sender", m.choosing.identity)
	}
	if m.choosing.identity.name == "" {
		t.Error("the pending edit should carry a name for the prompts")
	}
}

// Adding a second account to an existing person is the merge case, and it must not
// leave them in two identities at once — a state nothing else in the UI can render.
func TestAliasAddsToExistingIdentity(t *testing.T) {
	t.Parallel()

	m, path := aliasing(t, config.Display{Identities: []config.Identity{
		{Alias: "Dana", Color: "#8ff586", IDs: []string{"@dana:x"}},
		{Alias: "Someone else", IDs: []string{"@whatsapp_4470:x", "@keepme:x"}},
	}})
	m.focus = paneRooms

	m, _ = press(t, m, keyText("p"))
	m, _ = press(t, m, keyText("a")) // the bridged Dana, first in the list
	if m.choosing.identity.mxid != "@whatsapp_4470:x" {
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
	// Moved, not copied: the old identity must not still claim them.
	other := byAlias["Someone else"]
	for _, mxid := range other.IDs {
		if mxid == "@whatsapp_4470:x" {
			t.Error("the account is still in its old identity — belonging to two people at once")
		}
	}
	if len(other.IDs) != 1 || other.IDs[0] != "@keepme:x" {
		t.Errorf("the old identity's other accounts should be untouched, got %+v", other)
	}
	if _, err := config.Load(path); err != nil {
		t.Errorf("written config does not load: %v", err)
	}
}

// An identity emptied by a move is dropped: an entry naming nobody renders nowhere and
// is only noise in the file.
func TestAliasDropsEmptiedIdentity(t *testing.T) {
	t.Parallel()

	m, _ := aliasing(t, config.Display{Identities: []config.Identity{
		{Alias: "Only", IDs: []string{"@whatsapp_4470:x"}},
		{Alias: "Target", IDs: []string{"@dana:x"}},
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

// Backing out at any step changes nothing — the whole flow writes only at the end.
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

// An empty name is not a name: it abandons the edit rather than creating a nameless
// person.
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
	if !strings.Contains(m.status(), "no name") {
		t.Errorf("status = %q, should explain why nothing happened", m.status())
	}
}

// The people picker is modal, which is the only reason `a` can mean "set alias" there.
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
	// Filtering finds people by MXID as well as by name, since the MXID is what you can
	// see here and may be all you recognize.
	for _, r := range "whatsapp" {
		m, _ = press(t, m, keyText(string(r)))
	}
	m.picker.filter = "whatsapp"
	m.picker = m.picker.refilter()
	if len(m.picker.items) != 1 || m.picker.items[0].value != "@whatsapp_4470:x" {
		t.Errorf("filtering by MXID gave %+v", m.picker.items)
	}
}

// Nothing is written when there is nowhere to write, and that is reported rather than
// silently dropped — the setting did apply, it just will not survive a restart.
func TestAliasWithoutConfigPath(t *testing.T) {
	t.Parallel()

	m := update(t, starterNew(apitest.Nop{}, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m) // no WithConfigFile
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, membersMsg{roomID: "!a:x", members: aliasPeople})
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
	if !strings.Contains(m.status(), "no members") {
		t.Errorf("status = %q, should say why", m.status())
	}
}
