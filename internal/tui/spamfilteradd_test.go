package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Adding a spam filter asks for each part and writes it whole at the end; until then
// nothing is written, so a filter with a name alone is never refused and lost.
func TestAddingASpamFilterAsksForEachPart(t *testing.T) {
	t.Parallel()
	m, _ := opened(t, config.Notifications{Enabled: true})
	m = m.settingsIn(spamFilterTable, "")
	m, _ = m.chooseSetting(spamFilterTable + recordAdd)
	if m.prompt.kind != promptFilterName {
		t.Fatalf("Add one opened prompt %v, want the name", m.prompt.kind)
	}
	m = typeIn(t, m, "elections")
	if m.prompt.kind != promptFilterWords || len(m.conf.base.Spam.Filters) != 0 {
		t.Fatalf("after the name: prompt %v, filters %v; want the words asked, nothing written", m.prompt.kind, m.conf.base.Spam.Filters)
	}
	m = typeIn(t, m, "")
	if m.prompt.kind != promptFilterFrom {
		t.Fatalf("no words: prompt %v, want the sender asked", m.prompt.kind)
	}
	m = typeIn(t, m, "")
	if m.prompt.kind != promptFilterFrom || !strings.Contains(m.status(), "words or a sender") {
		t.Fatalf("neither words nor sender: prompt %v, status %q; want it asked again", m.prompt.kind, m.status())
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.picker.kind != pickerSetting || m.prompt.active() || m.choosing.spamFilter.Name != "" {
		t.Fatalf("esc: picker %v, prompt %v, draft %+v; want the filter list back, the draft gone", m.picker.kind, m.prompt.kind, m.choosing.spamFilter)
	}

	m, _ = m.chooseSetting(spamFilterTable + recordAdd)
	m = typeIn(t, m, "elections")
	m = typeIn(t, m, "vote*, ballot")
	if m.picker.kind != pickerFilterPlaces {
		t.Fatalf("after the words: picker %v, want the places", m.picker.kind)
	}
	m.picker.checked = map[string]bool{"space:Work": true}
	m, _ = m.acceptPick()
	if m.picker.kind != pickerFilterMoves || !slices.Equal(m.picker.checkedValues(), config.SpamMovesDefault) {
		t.Fatalf("after the places: picker %v ticked %v, want the moves with the default ticked", m.picker.kind, m.picker.checkedValues())
	}
	m.picker.checked = map[string]bool{}
	m, _ = m.acceptPick()
	if m.picker.kind != pickerFilterMoves || len(m.conf.base.Spam.Filters) != 0 {
		t.Fatalf("no move ticked: picker %v, filters %v; want it kept open, nothing written", m.picker.kind, m.conf.base.Spam.Filters)
	}
	m.picker.checked = map[string]bool{"always": true}
	next, cmd := m.acceptPick()
	m = deliver(t, next, cmd)
	want := config.SpamFilter{Name: "elections", Words: []string{"vote*", "ballot"}, In: []string{"space:Work"}, Moves: []string{"always"}}
	if got := m.conf.base.Spam.Filters; len(got) != 1 || got[0].Name != want.Name || !slices.Equal(got[0].Words, want.Words) ||
		!slices.Equal(got[0].In, want.In) || !slices.Equal(got[0].Moves, want.Moves) {
		t.Fatalf("filters = %+v, want %+v", got, want)
	}
	if m.choosing.settingGroup != spamFilterTable+"#0" {
		t.Errorf("after adding: on %q, want the new filter's page", m.choosing.settingGroup)
	}
}
