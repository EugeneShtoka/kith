package tui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Adding a filter asks for each part in turn: its name, the tag it moves rooms into, the
// words or the sender it catches, where it watches and what it moves a room by. Nothing
// is written until the last step, so a half-made filter is never refused and lost.

const filterTable = recordPrefix + "filter"

// filterMoveRows are the moves a filter's catch can make, in the order they are tried.
var filterMoveRows = []struct{ value, label, detail string }{
	{config.FilterMoveAlways, "Always", "any message it catches"},
	{config.FilterMoveFirstMessage, "First message", "the room's first message"},
	{config.FilterMoveDirect, "Direct message", "a DM you never wrote in"},
	{config.FilterMoveRatio, "Mostly", "most of what the room says, over the filter's window"},
}

// startFilter begins a new filter with its name, from the Filters list.
func (m Model) startFilter() (Model, tea.Cmd) { return m.startFilterFrom("", config.Filter{}) }

// startFilterFrom begins a new filter with preset filled in (a place's page sets its
// tag or the space it watches); back is the page to return to, "" for the Filters list.
func (m Model) startFilterFrom(back string, preset config.Filter) (Model, tea.Cmd) {
	m.choosing.filter, m.choosing.filterBack = preset, back
	return m.filterPage().openPrompt(promptFilterName), nil
}

// filterPage is the page a new filter's prompts are asked over: the place's it began
// on, or the Filters list.
func (m Model) filterPage() Model {
	if back := m.choosing.filterBack; back != "" {
		return m.settingsIn(back, back+"#addfilter")
	}
	return m.settingsIn(filterTable, filterTable+recordAdd)
}

func (m Model) submitFilterName(input string) (Model, tea.Cmd) {
	name := strings.TrimSpace(input)
	if name == "" {
		return m.backToFilters(), nil
	}
	taken := slices.ContainsFunc(m.conf.base.Filters, func(f config.Filter) bool {
		return strings.EqualFold(strings.TrimSpace(f.Name), name)
	})
	if taken {
		return m.filterPage().openPromptWith(promptFilterName, name).say("a filter is already called " + quoted(name)), nil
	}
	m.choosing.filter.Name = name
	if m.choosing.filter.Tag != "" {
		return m.filterPage().openPrompt(promptFilterWords), nil
	}
	return m.openFilterTags(), nil
}

// openFilterTags lists the tags a filter can move rooms into.
func (m Model) openFilterTags() Model {
	items := make([]pickerItem, 0, len(m.conf.base.Tags))
	for i := range m.conf.base.Tags {
		name := m.conf.base.Tags[i].Name
		items = append(items, pickerItem{label: isolate(name), value: name, match: name})
	}
	m.picker = newPicker(pickerFilterTag, items)
	return m
}

func (m Model) chooseFilterTag(tag string) (Model, tea.Cmd) {
	m.choosing.filter.Tag = tag
	return m.filterPage().openPrompt(promptFilterWords), nil
}

// submitFilterWords takes space- or comma-separated globs; none asks for a sender.
func (m Model) submitFilterWords(input string) (Model, tea.Cmd) {
	words := strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ' ' })
	m.choosing.filter.Words = words
	if len(words) == 0 {
		return m.filterPage().openPrompt(promptFilterFrom), nil
	}
	return m.openFilterPlaces(), nil
}

func (m Model) submitFilterFrom(input string) (Model, tea.Cmd) {
	from := strings.TrimSpace(input)
	if from == "" && len(m.choosing.filter.Words) == 0 {
		return m.filterPage().openPrompt(promptFilterFrom).say("a filter needs words or a sender — with neither it would catch everything"), nil
	}
	m.choosing.filter.From = from
	return m.openFilterPlaces(), nil
}

// openFilterPlaces lists the rail's spaces and tags; none ticked is everywhere.
func (m Model) openFilterPlaces() Model {
	var items []pickerItem
	for _, g := range m.rail.groups {
		if !isSpaceGroup(g.key) && !isTagGroup(g.key) {
			continue
		}
		items = append(items, pickerItem{label: g.label, detail: placeWord(g.key), value: domain.HomeEntry(g.key)})
	}
	if len(items) == 0 {
		return m.openFilterMoves()
	}
	checked := map[string]bool{}
	for _, entry := range m.choosing.filter.In {
		checked[entry] = true
	}
	m.picker = newCheckedPicker(pickerFilterPlaces, items, checked)
	return m
}

func (m Model) acceptFilterPlaces(entries []string) (Model, tea.Cmd) {
	m.choosing.filter.In = entries
	return m.openFilterMoves(), nil
}

// openFilterMoves lists the moves, the default ones ticked.
func (m Model) openFilterMoves() Model {
	items := make([]pickerItem, 0, len(filterMoveRows))
	checked := map[string]bool{}
	for _, row := range filterMoveRows {
		items = append(items, pickerItem{label: row.label, detail: row.detail, value: row.value})
		checked[row.value] = slices.Contains(config.FilterMovesDefault, row.value)
	}
	m.picker = newCheckedPicker(pickerFilterMoves, items, checked)
	return m
}

// acceptFilterMoves writes the filter and opens it; a refusal keeps the draft here.
func (m Model) acceptFilterMoves(moves []string) (Model, tea.Cmd) {
	if len(moves) == 0 {
		return m.say("tick at least one move — a filter that moves nothing does nothing"), nil
	}
	f := m.choosing.filter
	if !slices.Equal(moves, config.FilterMovesDefault) {
		f.Moves = moves
	}
	cfg := m.conf.base.Clone()
	cfg.Filters = append(cfg.Filters, f)
	if _, err := setup.Filters(cfg); err != nil {
		return m.sayErr("this filter", err), nil
	}
	if err := setup.PlaceEntries(cfg); err != nil {
		return m.sayErr("this filter", err), nil
	}
	m = m.closePicker()
	back := m.choosing.filterBack
	m.choosing.filter, m.choosing.filterBack = config.Filter{}, ""
	next, cmd := m.applyConfig(cfg, fmt.Sprintf("added the filter %s — :rescan judges what is already cached", quoted(f.Name)))
	if back != "" {
		return next.settingsIn(back, ""), cmd
	}
	at := filterTable + "#" + strconv.Itoa(len(cfg.Filters)-1)
	return next.settingsIn(at, ""), cmd
}

// backToFilters drops the draft and returns where it began: a place's page, or the
// Filters list on its add row.
func (m Model) backToFilters() Model {
	back := m.choosing.filterBack
	m.choosing.filter, m.choosing.filterBack = config.Filter{}, ""
	if back != "" {
		return m.closePrompt().settingsIn(back, "")
	}
	return m.closePrompt().settingsIn(filterTable, filterTable+recordAdd)
}

// chosenField makes a filter's tag, moves and places choices rather than typed text,
// as adding one asks them; any other field is returned as it is.
func (m Model) chosenField(s setting, table, field string) setting {
	if table != "filter" {
		return s
	}
	switch field {
	case "tag":
		s.kind, s.choices = settingChoice, nil
		for i := range m.conf.base.Tags {
			name := m.conf.base.Tags[i].Name
			s.choices = append(s.choices, settingChoiceOption{name, name, "moves rooms into " + name})
		}
	case "moves":
		s.kind, s.choices = settingMulti, nil
		for _, row := range filterMoveRows {
			s.choices = append(s.choices, settingChoiceOption{row.value, row.label, row.detail})
		}
		set := s.setEntries
		s.setEntries = func(c *config.Config, moves []string) error {
			if len(moves) == 0 {
				return errors.New("tick at least one move — a filter that moves nothing does nothing")
			}
			return set(c, moves)
		}
	case "in":
		s.kind, s.choices = settingMulti, m.placeChoices(s.entries(m.conf.base))
	}
	return s
}

// placeChoices are the rail's spaces and tags as place entries, then any entry already
// written that names something else (a room, a network), so ticking keeps it.
func (m Model) placeChoices(current []string) []settingChoiceOption {
	var out []settingChoiceOption
	for _, g := range m.rail.groups {
		if isSpaceGroup(g.key) || isTagGroup(g.key) {
			out = append(out, settingChoiceOption{domain.HomeEntry(g.key), g.label, placeWord(g.key)})
		}
	}
	for _, entry := range current {
		if !slices.ContainsFunc(out, func(c settingChoiceOption) bool { return c.value == entry }) {
			out = append(out, settingChoiceOption{entry, entry, "as written"})
		}
	}
	return out
}
