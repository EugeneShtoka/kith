package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Adding a spam filter asks for each part in turn: its name, the words or the sender it
// catches, where it watches and what it moves a room by. Nothing is written until the
// last step, so a half-made filter is never refused and lost.

const spamFilterTable = recordPrefix + "spam.filter"

// spamMoveRows are the moves a filter's catch can make, in the order the rules run.
var spamMoveRows = []struct{ value, label, detail string }{
	{config.SpamMoveAlways, "Always", "any message it catches"},
	{config.SpamMoveFirstMessage, "First message", "the room's first message"},
	{config.SpamMoveDirect, "Direct message", "a DM you never wrote in"},
	{config.SpamMoveRatio, "Mostly spam", "most of what the room says, over [spam] window"},
}

// startSpamFilter begins a new filter with its name.
func (m Model) startSpamFilter() (Model, tea.Cmd) {
	m.choosing.spamFilter = config.SpamFilter{}
	return m.closePicker().openPrompt(promptFilterName), nil
}

func (m Model) submitFilterName(input string) (Model, tea.Cmd) {
	name := strings.TrimSpace(input)
	if name == "" {
		return m.backToSpamFilters(), nil
	}
	taken := slices.ContainsFunc(m.conf.base.Spam.Filters, func(f config.SpamFilter) bool {
		return strings.EqualFold(strings.TrimSpace(f.Name), name)
	})
	if taken {
		return m.openPromptWith(promptFilterName, name).say("a filter is already called " + quoted(name)), nil
	}
	m.choosing.spamFilter.Name = name
	return m.openPrompt(promptFilterWords), nil
}

// submitFilterWords takes space- or comma-separated globs; none asks for a sender.
func (m Model) submitFilterWords(input string) (Model, tea.Cmd) {
	words := strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ' ' })
	m.choosing.spamFilter.Words = words
	if len(words) == 0 {
		return m.openPrompt(promptFilterFrom), nil
	}
	return m.openFilterPlaces(), nil
}

func (m Model) submitFilterFrom(input string) (Model, tea.Cmd) {
	from := strings.TrimSpace(input)
	if from == "" && len(m.choosing.spamFilter.Words) == 0 {
		return m.openPrompt(promptFilterFrom).say("a filter needs words or a sender — with neither it would catch everything"), nil
	}
	m.choosing.spamFilter.From = from
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
	m.picker = newCheckedPicker(pickerFilterPlaces, items, map[string]bool{})
	return m
}

func (m Model) acceptFilterPlaces(entries []string) (Model, tea.Cmd) {
	m.choosing.spamFilter.In = entries
	return m.openFilterMoves(), nil
}

// openFilterMoves lists the moves, the default ones ticked.
func (m Model) openFilterMoves() Model {
	items := make([]pickerItem, 0, len(spamMoveRows))
	checked := map[string]bool{}
	for _, row := range spamMoveRows {
		items = append(items, pickerItem{label: row.label, detail: row.detail, value: row.value})
		checked[row.value] = slices.Contains(config.SpamMovesDefault, row.value)
	}
	m.picker = newCheckedPicker(pickerFilterMoves, items, checked)
	return m
}

// acceptFilterMoves writes the filter and opens it; a refusal keeps the draft here.
func (m Model) acceptFilterMoves(moves []string) (Model, tea.Cmd) {
	if len(moves) == 0 {
		return m.say("tick at least one move — a filter that moves nothing does nothing"), nil
	}
	f := m.choosing.spamFilter
	if !slices.Equal(moves, config.SpamMovesDefault) {
		f.Moves = moves
	}
	cfg := m.conf.base.Clone()
	cfg.Spam.Filters = append(cfg.Spam.Filters, f)
	if _, err := setup.SpamRules(cfg.Spam); err != nil {
		return m.sayErr("this filter", err), nil
	}
	if err := setup.PlaceEntries(cfg); err != nil {
		return m.sayErr("this filter", err), nil
	}
	m = m.closePicker()
	m.choosing.spamFilter = config.SpamFilter{}
	next, cmd := m.applyConfig(cfg, fmt.Sprintf("added the spam filter %s — :rescan judges what is already cached", quoted(f.Name)))
	at := spamFilterTable + "#" + strconv.Itoa(len(cfg.Spam.Filters)-1)
	return next.settingsIn(at, ""), cmd
}

// backToSpamFilters drops the draft and returns to the filter list, on its add row.
func (m Model) backToSpamFilters() Model {
	m.choosing.spamFilter = config.SpamFilter{}
	return m.closePrompt().settingsIn(spamFilterTable, spamFilterTable+recordAdd)
}
