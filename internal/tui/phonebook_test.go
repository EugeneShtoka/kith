package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// roster answers a room's members.
type roster struct {
	apitest.Nop
	members []domain.Member
}

func (r roster) Members(context.Context, domain.RoomID, int) ([]domain.Member, error) {
	return r.members, nil
}

// carding is a model whose phonebook is a folder holding Eli's card, with a room
// where Dana (a WhatsApp number) and Eli (a Matrix user) wrote.
func carding(t *testing.T, members []domain.Member) (Model, string) {
	t.Helper()
	dir := t.TempDir()
	eli := "---\nmatrix: [\"@eli:x\"]\npets: [Rex]\norigin: Haifa\n---\nOld friend.\n"
	if err := os.WriteFile(filepath.Join(dir, "Eli Stone.md"), []byte(eli), 0o600); err != nil {
		t.Fatal(err)
	}
	m := sized(t, withRooms(t, starterNew(roster{members: members}, config.Display{})))
	m.prefs.phonebookDir = dir
	m.selves = []string{"@me:x"}
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@whatsapp_15550100001:x", SenderName: "Dana", Timestamp: time.Unix(1000, 0), Body: "hi"},
		{ID: "$2", RoomID: "!a:x", Sender: "@eli:x", SenderName: "Eli", Timestamp: time.Unix(1001, 0), Body: "hey"},
	}}})
	m.focus = paneTimeline
	return m, dir
}

// opened presses I on the selected message and delivers the card read.
func cardOf(t *testing.T, m Model, sender string) Model {
	t.Helper()
	m.timeline.selected = domain.EventID(map[string]string{"dana": "$1", "eli": "$2"}[sender])
	next, cmd := press(t, m, keyText("I"))
	return deliver(t, next, cmd)
}

func rows(m Model) []string {
	var out []string
	for _, item := range m.picker.all {
		out = append(out, stripIsolates(item.label+"="+item.detail))
	}
	return out
}

// A sender with no card is offered one: a field chosen from the keys the folder's
// cards use is written on a new card named after them, with their number; the body of
// another's card is never touched.
func TestASendersCardIsMadeAtTheFirstField(t *testing.T) {
	t.Parallel()
	m, dir := carding(t, nil)
	m = cardOf(t, m, "dana")
	if m.picker.kind != pickerPerson || !strings.Contains(m.picker.spec.title, "no card yet") {
		t.Fatalf("picker %v %q", m.picker.kind, m.picker.spec.title)
	}
	next, cmd := m.acceptPick() // Add a field
	m = deliver(t, next, cmd)
	if strings.Join(rows(m), ",") != "origin=,pets=,New field=name it" {
		t.Fatalf("keys offered = %v", rows(m))
	}
	m = pickLabel(t, m, "origin")
	m = typeIn(t, m, "Tel Aviv")
	raw, err := os.ReadFile(filepath.Join(dir, "Dana.md"))
	if err != nil {
		t.Fatalf("no card made: %v", err)
	}
	if text := string(raw); !strings.Contains(text, "+15550100001") || !strings.Contains(text, "origin: Tel Aviv") || !strings.Contains(text, "person") {
		t.Errorf("card:\n%s", text)
	}
	if !strings.Contains(m.picker.spec.title, "Dana.md") {
		t.Errorf("after writing: title %q", m.picker.spec.title)
	}
	if eli, _ := os.ReadFile(filepath.Join(dir, "Eli Stone.md")); !strings.HasSuffix(string(eli), "---\nOld friend.\n") {
		t.Errorf("Eli's card changed:\n%s", eli)
	}
}

// A card is found by any of the person's IDs; a field is edited in place, a comma
// making it a list and nothing removing it; a new field is named first.
func TestACardIsEditedAField(t *testing.T) {
	t.Parallel()
	m, dir := carding(t, nil)
	m = cardOf(t, m, "eli")
	if got := strings.Join(rows(m), ","); got != "matrix=@eli:x,pets=Rex,origin=Haifa,Add a field=" {
		t.Fatalf("rows = %v", got)
	}
	m = pickLabel(t, m, "pets")
	m = typeIn(t, m, "Rex, Mia")
	m = pickLabel(t, m, "origin")
	m = typeIn(t, m, "")
	m.picker.cursor = len(m.picker.items) - 1 // Add a field
	next, cmd := m.acceptPick()
	m = deliver(t, next, cmd)
	m = pickLabel(t, m, "New field")
	m = typeIn(t, m, "spouse")
	m = typeIn(t, m, "[[Noa Stone]]")
	m.picker.cursor = len(m.picker.items) - 1
	next, cmd = m.acceptPick()
	m = deliver(t, next, cmd)
	m = pickLabel(t, m, "New field")
	m = typeIn(t, m, "children")
	m = typeIn(t, m, "Noa, Tom")
	raw, _ := os.ReadFile(filepath.Join(dir, "Eli Stone.md"))
	text := string(raw)
	if !strings.Contains(text, "pets: [Rex, Mia]") || strings.Contains(text, "origin") || !strings.Contains(text, "spouse: '[[Noa Stone]]'") || !strings.HasSuffix(text, "Old friend.\n") ||
		!strings.Contains(text, "children:\n  - Noa\n  - Tom") {
		t.Errorf("card:\n%s", text)
	}
}

// From the member list I opens the person under the cursor; in a room with one person
// besides you and the bots it opens theirs, and in a room of several it says so.
func TestACardOpensFromMembersAndRooms(t *testing.T) {
	t.Parallel()
	m, _ := carding(t, []domain.Member{{UserID: "@me:x"}, {UserID: "@whatsappbot:x"}, {UserID: "@eli:x", DisplayName: "Eli"}})
	m.timeline.members = []domain.Member{{UserID: "@eli:x", DisplayName: "Eli"}}
	m, _ = m.openPeopleForRoom()
	next, cmd := press(t, m, keyText("I"))
	if m = deliver(t, next, cmd); m.picker.kind != pickerPerson || !strings.Contains(m.picker.spec.title, "Eli Stone.md") {
		t.Errorf("from the member list: picker %v %q", m.picker.kind, m.picker.spec.title)
	}

	m = m.closePicker()
	m.focus = paneRooms
	next, cmd = press(t, m, keyText("I"))
	m = deliverTwice(t, next, cmd)
	if m.picker.kind != pickerPerson || !strings.Contains(m.picker.spec.title, "Eli Stone.md") {
		t.Errorf("from a one-person room: picker %v %q", m.picker.kind, m.picker.spec.title)
	}

	crowd, _ := carding(t, []domain.Member{{UserID: "@me:x"}, {UserID: "@eli:x"}, {UserID: "@whatsapp_15550100001:x"}})
	crowd.focus = paneRooms
	next, cmd = press(t, crowd, keyText("I"))
	crowd = deliver(t, next, cmd)
	if crowd.picker.kind == pickerPerson || !strings.Contains(crowd.status(), "not a room with one person") {
		t.Errorf("a room of several: picker %v, said %q", crowd.picker.kind, crowd.status())
	}
}

// What the phonebook knows a person by: a number, a Matrix user, a bridged Telegram
// user, and each network's own ID, every identity ID of theirs included.
func TestAPersonIsKnownByEveryIDOfTheirs(t *testing.T) {
	t.Parallel()
	m := starterNew(apitest.Nop{}, config.Display{Identities: []config.Identity{{Alias: "Dana", IDs: []string{"@dana:x", "@telegram_42:x", "slack:T1.U2"}}}})
	ids := m.personIdentifiers("@dana:x")
	if ids["matrix"][0] != "@dana:x" || ids["telegram"][0] != "42" || ids["slack"][0] != "T1.U2" {
		t.Errorf("identifiers = %v", ids)
	}
	if got := m.personIdentifiers("@whatsapp_15550100001:x"); got["phone"][0] != "+15550100001" {
		t.Errorf("a WhatsApp puppet's = %v", got)
	}
	if got := m.personIdentifiers("telegram:77"); got["telegram"][0] != "77" {
		t.Errorf("a Telegram user's = %v", got)
	}
}
