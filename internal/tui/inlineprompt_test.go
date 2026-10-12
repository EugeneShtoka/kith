package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// lineIndex is the first line of the screen holding text, -1 if none.
func lineIndex(screen, text string) int {
	for i, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, text) {
			return i
		}
	}
	return -1
}

// Over a list in the timeline pane, a prompt is drawn right after the list's last row and
// the status line keeps only its keys; submitting closes the list it was asked over.
func TestAPromptOverAListFollowsItsRows(t *testing.T) {
	t.Parallel()
	m, _ := laidOut(t, config.Display{})
	m = m.settingsIn(placePage("tag:Spam"), "")
	m = pickLabel(t, m, "Add a filter moving rooms here")
	if !m.promptInline() {
		t.Fatal("the filter's name is not asked inline over the page")
	}
	screen := stripStyles(m.View().Content)
	last, field := lineIndex(screen, "Delete this tag"), lineIndex(screen, "new filter's name:")
	if last < 0 || field <= last || field > last+2 {
		t.Errorf("the field is on line %d, the list ends on %d; want it right after:\n%s", field, last, screen)
	}
	if status := stripStyles(m.renderStatus()); strings.Contains(status, "new filter's name") || !strings.Contains(status, "esc") {
		t.Errorf("status = %q, want only the keys", status)
	}
	if item, _ := m.picker.selected(); !strings.HasSuffix(item.value, "#addfilter") {
		t.Errorf("cursor on %q, want the row that began it", item.value)
	}
}

// Over the timeline itself a prompt stays in the status line.
func TestAPromptOverTheTimelineStaysInTheStatusLine(t *testing.T) {
	t.Parallel()
	m, _ := laidOut(t, config.Display{})
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	m, _ = press(t, m, keyText("a")) // name this group
	if m.prompt.kind != promptGroupName {
		t.Fatalf("prompt %v, want the group's name", m.prompt.kind)
	}
	if m.promptInline() {
		t.Error("a prompt over the timeline was drawn inline")
	}
	if status := stripStyles(m.renderStatus()); !strings.Contains(status, "show this group as") {
		t.Errorf("status = %q, want the field", status)
	}
}

// A card's field is asked over the card; esc keeps the card open.
func TestACardFieldIsAskedOverTheCard(t *testing.T) {
	t.Parallel()
	m, _ := carding(t, []domain.Member{{UserID: "@me:x"}, {UserID: "@eli:x", DisplayName: "Eli"}})
	m.timeline.members = []domain.Member{{UserID: "@eli:x", DisplayName: "Eli"}}
	m, _ = m.openPeopleForRoom()
	next, cmd := press(t, m, keyText("I"))
	m = deliver(t, next, cmd)
	if m.picker.kind != pickerPerson {
		t.Fatalf("picker %v, want Eli's card", m.picker.kind)
	}
	m = pickLabel(t, m, "origin")
	if !m.promptInline() || m.picker.kind != pickerPerson {
		t.Fatalf("editing a field: inline %v over picker %v, want inline over the card", m.promptInline(), m.picker.kind)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.prompt.active() || m.picker.kind != pickerPerson {
		t.Errorf("esc: prompt %v, picker %v; want the card back", m.prompt.kind, m.picker.kind)
	}
	// A field chosen to add is asked over the card too, not over the list of fields.
	m = m.closePicker()
	m, _ = m.choosePersonKey("pets")
	if !m.promptInline() || m.picker.kind != pickerPerson || m.prompt.kind != promptPersonValue {
		t.Errorf("adding pets: inline %v over picker %v, prompt %v; want its value asked over the card", m.promptInline(), m.picker.kind, m.prompt.kind)
	}
}
