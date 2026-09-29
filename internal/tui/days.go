package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Day motions. Back goes to the start of the current day first (vim's paragraph
// convention), then to the day before; forward goes to the next day's first message.

// dayStart is the index of the first message of the day that message i falls in.
func dayStart(msgs []domain.Message, i int) int {
	day := dayKey(msgs[i].Timestamp)
	for i > 0 && dayKey(msgs[i-1].Timestamp) == day {
		i--
	}
	return i
}

// dayEnd is the index of the last message of the day that message i falls in.
func dayEnd(msgs []domain.Message, i int) int {
	day := dayKey(msgs[i].Timestamp)
	for i+1 < len(msgs) && dayKey(msgs[i+1].Timestamp) == day {
		i++
	}
	return i
}

// jumpDay moves the cursor count days older (dir < 0) or newer and puts that day's
// first message at the top. Running out stops at the last day reached.
func (m Model) jumpDay(count, dir int) (Model, tea.Cmd) {
	msgs := m.shownMessages()
	if len(msgs) == 0 {
		return m, nil
	}
	// With no cursor, start from the newest message.
	from := indexIn(msgs, m.timeline.selected)
	if from < 0 {
		from = len(msgs) - 1
	}

	target, at := -1, from
	for range max(count, 1) {
		next := dayStep(msgs, at, dir)
		if next < 0 {
			break
		}
		target, at = next, next
	}

	if target < 0 {
		return m.noMoreDays(dir)
	}
	m.timeline.selected = msgs[target].ID
	return m.dayAtTop(), nil
}

// dayStep is one day's worth of movement from an index, and -1 when there is no
// further day that way in what is loaded.
func dayStep(msgs []domain.Message, from, dir int) int {
	if dir < 0 {
		if start := dayStart(msgs, from); start < from {
			return start // not yet at the top of this day
		} else if start > 0 {
			return dayStart(msgs, start-1)
		}
		return -1
	}
	if end := dayEnd(msgs, from); end+1 < len(msgs) {
		return end + 1
	}
	return -1
}

// noMoreDays answers a jump that has nowhere to go; going back loads older history.
func (m Model) noMoreDays(dir int) (Model, tea.Cmd) {
	if dir > 0 {
		return m.say("already on the last day"), nil
	}
	if m.atStartHere() {
		return m.say("no earlier day in this room"), nil
	}
	return m.doing("loading earlier history…").loadOlder()
}

// dayAtTop scrolls so the selected message, and its date separator, sit at the top.
func (m Model) dayAtTop() Model {
	_, to, ok := m.selectionRows()
	if !ok {
		return m
	}
	top := to
	if m.selectionHasDivider() {
		top++
	}
	m.timeline.scroll = max(top-m.msgAreaRows(), 0)
	if mx := m.maxScroll(); m.timeline.scroll > mx {
		m.timeline.scroll = mx
	}
	return m
}

// selectionHasDivider reports whether a date separator is drawn above the selected
// message.
func (m Model) selectionHasDivider() bool {
	derived, w := m.walkFor()
	i, ok := derived.index[m.selectedID()]
	if !ok {
		return false
	}
	return m.entryFor(w, i).divider
}
