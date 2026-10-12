package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Places lists every rail row that is a space or a tag, in rail order, then tags the
// rail does not show, then New tag.
func TestPlacesListsEveryPlace(t *testing.T) {
	t.Parallel()
	m, _ := laidOut(t, config.Display{})
	cfg := m.conf.base.Clone()
	cfg.Tags = append(cfg.Tags, config.Tag{Name: "Secret", Hidden: true})
	m, _ = m.applyConfig(cfg, "")
	m = m.tagsOpen()
	var keys []string
	for _, item := range m.picker.items {
		keys = append(keys, strings.TrimPrefix(item.value, placePagePrefix))
	}
	var rail []string
	for _, key := range railKeys(m) {
		if isSpaceGroup(key) || isTagGroup(key) {
			rail = append(rail, key)
		}
	}
	if len(keys) < len(rail)+2 || !slices.Equal(keys[:len(rail)], rail) {
		t.Fatalf("Places = %v, want the rail's places %v first", keys, rail)
	}
	if !slices.Contains(keys, "tag:Secret") || keys[len(keys)-1] != "places#new" {
		t.Errorf("Places = %v, want the hidden tag and New tag last", keys)
	}
}

// A tag's page lists the filters moving rooms into it; adding one there skips the tag
// question, and the filter is written into that tag, back on the page.
func TestATagsPageAddsAFilterMovingRoomsThere(t *testing.T) {
	t.Parallel()
	m, _ := laidOut(t, config.Display{})
	cfg := m.conf.base.Clone()
	cfg.Filters = []config.Filter{{Name: "crypto", Tag: "Spam", Words: []string{"*bitcoin*"}}}
	m, _ = m.applyConfig(cfg, "")
	page := placePage("tag:Spam")
	m = m.settingsIn(page, "")
	if got := rowFor(t, m, "Filter").detail; !strings.Contains(got, "crypto") {
		t.Errorf("filter row reads %q, want crypto", got)
	}
	m = pickLabel(t, m, "Add a filter moving rooms here")
	m = typeIn(t, m, "elections")
	if m.prompt.kind != promptFilterWords {
		t.Fatalf("after the name: prompt %v, want the words (the tag is the page's)", m.prompt.kind)
	}
	m = typeIn(t, m, "vote")
	m.picker.checked = map[string]bool{}
	m, _ = m.acceptPick() // anywhere
	next, cmd := m.acceptPick()
	m = deliver(t, next, cmd)
	if n := len(m.conf.base.Filters); n != 2 || m.conf.base.Filters[1].Tag != "Spam" || m.conf.base.Filters[1].Name != "elections" {
		t.Fatalf("filters = %+v, want elections moving into Spam", m.conf.base.Filters)
	}
	if m.choosing.settingGroup != page {
		t.Errorf("after adding: on %q, want back on %q", m.choosing.settingGroup, page)
	}
}

// A space's page adds a filter watching it: the space starts ticked, the tag is asked.
func TestASpacesPageAddsAFilterWatchingIt(t *testing.T) {
	t.Parallel()
	m, _ := laidOut(t, config.Display{})
	m = m.settingsIn(placePage("Work"), "")
	m = pickLabel(t, m, "Add a filter watching here")
	m = typeIn(t, m, "standups")
	if m.picker.kind != pickerFilterTag {
		t.Fatalf("after the name: picker %v, want the tag asked", m.picker.kind)
	}
	m, _ = m.chooseFilterTag("Archived")
	m = typeIn(t, m, "standup")
	if !slices.Equal(m.picker.checkedValues(), []string{"space:Work"}) {
		t.Errorf("places ticked %v, want Work alone", m.picker.checkedValues())
	}
	m, _ = m.acceptPick()
	next, cmd := m.acceptPick()
	m = deliver(t, next, cmd)
	got := m.conf.base.Filters
	if len(got) != 1 || got[0].Tag != "Archived" || !slices.Equal(got[0].In, []string{"space:Work"}) {
		t.Fatalf("filters = %+v, want one watching Work, moving into Archived", got)
	}
	if rowFor(t, m.settingsIn(placePage("Work"), ""), "Filter").detail == "" {
		t.Error("Work's page does not list the filter watching it")
	}
}

// e on a rail row opens that place's page; `,` opens all of settings from anywhere.
func TestTheEditKeyOnARailRowOpensItsPage(t *testing.T) {
	t.Parallel()
	m, _ := laidOut(t, config.Display{})
	m.focus = paneRail
	m.rail.cursor = indexOfGroup(m.rail.groups, "Work")
	next, _ := press(t, m, keyText("e"))
	if next.picker.kind != pickerSetting || next.choosing.settingGroup != placePage("Work") {
		t.Errorf("e on Work: picker %v on %q, want Work's page", next.picker.kind, next.choosing.settingGroup)
	}
	if next, _ = press(t, m, keyText(",")); next.picker.kind != pickerSettingGroups {
		t.Errorf("`,` on the rail: picker %v, want all of settings", next.picker.kind)
	}
}

// A tag's page toggles notifies, writing it unset when on.
func TestATagsPageTogglesNotifies(t *testing.T) {
	t.Parallel()
	m, _ := editingTag(t, "Pinned")
	m = pickLabel(t, m, "Notifies")
	if n := m.conf.base.Tags[m.configTag("Pinned")].Notifies; n == nil || *n {
		t.Fatalf("notifies = %v, want false", n)
	}
	m = pickLabel(t, m, "Notifies")
	if n := m.conf.base.Tags[m.configTag("Pinned")].Notifies; n != nil {
		t.Errorf("notifies = %v, want unset (on)", *n)
	}
}
