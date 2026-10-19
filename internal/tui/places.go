package tui

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Settings → Places: every rail row that is a space or a tag, each with one page
// holding everything about it: a tag's rule, rooms and properties, the place's own
// display rows, and the filters moving rooms into it or watching it. The rail's edit key
// opens a row's page.

// placePagePrefix keys a place's page by its rail key: page:Work, page:tag:Spam.
const placePagePrefix = "page:"

func placePage(key string) string { return placePagePrefix + key }

// placeKeys is every place with a page, in rail order, then tags the rail does not show.
func (m Model) placeKeys() []string {
	var keys []string
	for _, g := range m.rail.groups {
		if isSpaceGroup(g.key) || isTagGroup(g.key) {
			keys = append(keys, g.key)
		}
	}
	for i := range m.conf.base.Tags {
		if key := tagGroupKey(m.conf.base.Tags[i].Name); !slices.ContainsFunc(keys, func(k string) bool { return strings.EqualFold(k, key) }) {
			keys = append(keys, key)
		}
	}
	return keys
}

// pageLabel is a place as the rail shows it.
func (m Model) pageLabel(key string) string {
	if at := indexOfGroup(m.rail.groups, key); at >= 0 && at < len(m.rail.groups) && m.rail.groups[at].key == key {
		return m.rail.groups[at].label
	}
	if name, ok := domain.TagOf(key); ok {
		return name
	}
	return key
}

// placesRows are the Places group: a row per place, then a row making a tag.
func (m Model) placesRows() []setting {
	keys := m.placeKeys()
	rows := make([]setting, 0, len(keys)+1)
	for _, key := range keys {
		page := placePage(key)
		rows = append(rows, setting{
			key: page, group: "places", kind: settingOpen, label: m.pageLabel(key),
			show: func(config.Config) string { return m.placeSummary(key) },
			open: func(m Model) (Model, tea.Cmd) { return m.settingsIn(page, ""), nil },
		})
	}
	return append(rows, setting{
		key: "places#new", group: "places", kind: settingOpen, label: "New tag",
		show: func(config.Config) string { return "" },
		open: func(m Model) (Model, tea.Cmd) {
			m.choosing.tag = tagEditing{}
			return m.openPrompt(promptTagName), nil
		},
	})
}

// placeSummary is a place in a line: what kind it is, its rule and rooms for a tag, and
// how many filters move rooms into it or watch it.
func (m Model) placeSummary(key string) string {
	parts := []string{placeWord(key)}
	if name, ok := domain.TagOf(key); ok {
		if at := m.configTag(name); at >= 0 {
			parts = append(parts, tagSummary(m.conf.base.Tags[at]))
		}
	}
	if n := len(m.placeFilters(key)); n > 0 {
		parts = append(parts, fmt.Sprintf("%d filter%s", n, map[bool]string{true: "", false: "s"}[n == 1]))
	}
	return strings.Join(parts, " · ")
}

// placePageRows are one place's page.
func (m Model) placePageRows(key string) []setting {
	group := placePage(key)
	var rows []setting
	name, isTag := domain.TagOf(key)
	if isTag {
		if m.configTag(name) < 0 {
			return nil
		}
		rows = append(rows, m.tagPageRows(name, group)...)
	}
	label := m.pageLabel(key)
	if isSpaceGroup(key) || (isTag && m.isHomeTag(key)) {
		rows = append(rows, nameRow(key, label, group))
	}
	rows = append(rows, m.fieldRow(key, group), colorRow(key, group))
	if !isTag || !m.isArchiveTag(name) {
		rows = append(rows, archivedRow(key, group))
	}
	rows = append(rows, m.placeFilterRows(key, group)...)
	if isTag {
		rows = append(rows, setting{
			key: group + "#delete", group: group, kind: settingOpen, label: "Delete this tag",
			show: func(config.Config) string { return "" },
			open: func(m Model) (Model, tea.Cmd) {
				m.confirm = confirmState{action: pendingDeleteTag, group: name}
				return m.closePicker(), nil
			},
		})
	}
	return rows
}

// tagPageRows are a tag's own rows: its name, rule and rooms (edited in the tag editor's
// prompts and lists), and its properties.
func (m Model) tagPageRows(name, group string) []setting {
	at := m.configTag(name)
	t := m.conf.base.Tags[at]
	rows := []setting{{
		key: group + "#name", group: group, kind: settingOpen, label: "Name",
		show: func(config.Config) string { return isolate(t.Name) },
		open: func(m Model) (Model, tea.Cmd) {
			m.choosing.tag = tagEditing{tag: t.Name}
			return m.openPromptWith(promptTagName, t.Name), nil
		},
	}}
	for _, list := range []struct {
		key, label string
		field      func(*config.Tag) *[]string
	}{
		{tagRule, "Rule", func(c *config.Tag) *[]string { return &c.Rule }},
		{tagPicked, "Picked", func(c *config.Tag) *[]string { return &c.Picked }},
		{tagExcluded, "Excluded", func(c *config.Tag) *[]string { return &c.Excluded }},
		{tagPeople, "People", func(c *config.Tag) *[]string { return &c.People }},
	} {
		rows = append(rows, m.tagListRow(name, group, list.key, list.label, list.field))
	}
	rows = append(rows,
		m.tagMarksRow(name, group),
		tagToggle(name, group, countsUnreadKey, "Counts as unread", config.Tag.Counts, func(tag *config.Tag, on bool) { tag.CountsUnread = unsetIfOn(on) }),
		tagToggle(name, group, notifiesKey, "Notifies", config.Tag.Notifying, func(tag *config.Tag, on bool) { tag.Notifies = unsetIfOn(on) }),
	)
	for _, p := range tagProperties {
		rows = append(rows, tagToggle(name, group, p.key, p.label, p.get, p.set))
	}
	if claimed := m.claimedFrom(at); claimed != "" {
		rows = append(rows, setting{
			key: group + "#claimed", group: group, kind: settingOpen, label: "Also excludes",
			show: func(config.Config) string { return claimed },
			open: func(m Model) (Model, tea.Cmd) {
				return m.say("a tag exclusive from tags shows its rooms alone"), nil
			},
		})
	}
	return rows
}

// tagListRow is one of the tag's lists, edited an entry at a time as any list setting
// is; a room entry is shown by the room's name.
func (m Model) tagListRow(name, group, key, label string, field func(*config.Tag) *[]string) setting {
	at := func(c config.Config) int {
		return slices.IndexFunc(c.Tags, func(t config.Tag) bool { return domain.SameTag(t.Name, name) })
	}
	entries := func(c config.Config) []string {
		if i := at(c); i >= 0 {
			return slices.Clone(*field(&c.Tags[i]))
		}
		return nil
	}
	s := setting{
		key: group + "#" + key, group: group, kind: settingList, label: label,
		show:    func(c config.Config) string { return m.entriesWords(entries(c)) },
		set:     func(*config.Config, string) error { return errors.New("edited an entry at a time") },
		entries: entries,
		setEntries: func(c *config.Config, list []string) error {
			i := at(*c)
			if i < 0 {
				return fmt.Errorf("no tag is called %q", name)
			}
			*field(&c.Tags[i]) = list
			return nil
		},
		entryLabel: m.entryName,
	}
	switch key {
	case tagRule:
		s.show = func(c config.Config) string { return ruleWords(entries(c)) }
		s.entryLabel = nil
	case tagPeople:
		s.show = func(c config.Config) string { return m.peopleWords(entries(c)) }
		s.entryLabel = m.personName
	}
	return s
}

// peopleWords is a list of people as shown: how many, and their names.
func (m Model) peopleWords(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, m.personName(id))
	}
	return fmt.Sprintf("%d: %s", len(ids), strings.Join(names, ", "))
}

// tagMarksRow lists the people filters put in the tag, read-only: a filter's mark is
// taken off in the tag's people (tagpeople.go), where taking it off also releases it.
func (m Model) tagMarksRow(name, group string) setting {
	return setting{
		key: group + "#marks", group: group, kind: settingOpen, label: "Put here by filters",
		show: func(config.Config) string {
			var marks []string
			for _, v := range m.rail.people {
				if v.In() && domain.SameTag(v.Tag, name) {
					marks = append(marks, m.personName(v.Person)+" ("+cmp.Or(v.Filter, "a filter")+")")
				}
			}
			if len(marks) == 0 {
				return "none"
			}
			return fmt.Sprintf("%d: %s", len(marks), strings.Join(marks, ", "))
		},
		open: func(m Model) (Model, tea.Cmd) {
			key := m.keys.keyHint(scopeRooms, actTagPeople)
			return m.say("a filter's mark comes off in the tag's people — " + key + " in its room list, then the tag's key"), nil
		},
	}
}

// unsetIfOn is a *bool whose nil is true: nil for on, false for off.
func unsetIfOn(on bool) *bool {
	if on {
		return nil
	}
	return &on
}

// tagToggle is one on/off property of the named tag.
func tagToggle(name, group, key, label string, get func(config.Tag) bool, set func(*config.Tag, bool)) setting {
	at := func(c config.Config) int {
		return slices.IndexFunc(c.Tags, func(t config.Tag) bool { return domain.SameTag(t.Name, name) })
	}
	return setting{
		key: group + "#" + key, group: group, kind: settingToggle, label: label,
		show: func(c config.Config) string {
			if i := at(c); i >= 0 {
				return onOff(get(c.Tags[i]))
			}
			return ""
		},
		set: func(c *config.Config, _ string) error {
			i := at(*c)
			if i < 0 {
				return fmt.Errorf("no tag is called %q", name)
			}
			set(&c.Tags[i], !get(c.Tags[i]))
			return nil
		},
	}
}

// placeFilters is the filters a place's page lists, by index: those moving rooms into a
// tag, or watching a space.
func (m Model) placeFilters(key string) []int {
	name, isTag := domain.TagOf(key)
	var out []int
	for i := range m.conf.base.Filters {
		f := &m.conf.base.Filters[i]
		watches := slices.ContainsFunc(f.In, func(entry string) bool { return domain.NamesHome(entry, key, m.pageLabel(key)) })
		if (isTag && domain.SameTag(f.Tag, name)) || (!isTag && watches) {
			out = append(out, i)
		}
	}
	return out
}

// placeFilterRows are a place's filters, each leading to its record, and a row adding one
// with the place filled in: the tag it moves rooms into, or the space it watches.
func (m Model) placeFilterRows(key, group string) []setting {
	name, isTag := domain.TagOf(key)
	t, _ := recordTable(filterTable)
	var rows []setting
	for _, i := range m.placeFilters(key) {
		record := filterTable + "#" + strconv.Itoa(i)
		summary := recordSummary(m.conf.base, t, i)
		rows = append(rows, setting{
			key: group + "#" + record, group: group, kind: settingOpen, label: "Filter",
			show: func(config.Config) string { return summary },
			open: func(m Model) (Model, tea.Cmd) { return m.settingsIn(record, ""), nil },
		})
	}
	preset := config.Filter{In: []string{domain.HomeEntry(key)}}
	label := "Add a filter watching here"
	if isTag {
		preset, label = config.Filter{Tag: name}, "Add a filter moving rooms here"
	}
	return append(rows, setting{
		key: group + "#addfilter", group: group, kind: settingOpen, label: label,
		show: func(config.Config) string { return "" },
		open: func(m Model) (Model, tea.Cmd) { return m.startFilterFrom(group, preset) },
	})
}

// openPlaceUnderCursor is the rail's edit key: the page of the space or tag under the
// cursor; false for any other row.
func (m Model) openPlaceUnderCursor() (Model, bool) {
	if m.focus != paneRail || m.rail.cursor < 0 || m.rail.cursor >= len(m.rail.groups) {
		return m, false
	}
	key := m.rail.groups[m.rail.cursor].key
	if !isSpaceGroup(key) && !isTagGroup(key) {
		return m, false
	}
	return m.settingsIn(placePage(key), ""), true
}
