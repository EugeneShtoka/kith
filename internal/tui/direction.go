package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// roomLayout is the open room's side ([display.direction]): decided once per opening,
// so the timeline does not flip while you read; a side set by hand applies at once.
type roomLayout struct {
	room domain.RoomID
	rtl  bool
	// settled: decided for this opening. Until then an auto room reads left to right
	// and is guessed again as messages arrive.
	settled bool
}

// guessWindow is how many of the newest messages the auto guess reads.
const guessWindow = 100

// mirrored reports whether the open room's timeline reads right to left: the sender
// column on the right, the text flushed against it.
func (m Model) mirrored() bool {
	return m.timeline.layout.room == m.openRoom && m.timeline.layout.rtl
}

// settleLayout decides the open room's side: a place set by hand, else the guess from
// its newest messages when auto is on, else left to right. The guess is made once per
// opening, as soon as a message with letters is loaded; it costs one pass over those
// messages' text.
func (m Model) settleLayout() Model {
	l := m.timeline.layout
	if l.room != m.openRoom {
		l = roomLayout{room: m.openRoom}
	}
	switch set := m.handSetLayout(); {
	case set != domain.LayoutUnset:
		l.rtl, l.settled = set == domain.LayoutRTL, true
	case l.settled:
	case !m.prefs.display.Direction.Auto:
		l.rtl, l.settled = false, true
	default:
		if guess := guessLayout(m.timeline.messages); guess != domain.LayoutUnset {
			l.rtl, l.settled = guess == domain.LayoutRTL, true
		}
	}
	m.timeline.layout = l
	return m
}

// resettleLayout decides the open room's side again, after the settings changed.
func (m Model) resettleLayout() Model {
	m.timeline.layout.settled = false
	return m.settleLayout()
}

// handSetLayout is the side [display.direction] rtl/ltr sets the open room to.
func (m Model) handSetLayout() domain.Layout {
	room, ok := m.roomByID(m.openRoom)
	if !ok {
		return domain.LayoutUnset
	}
	return m.directions().Of(m.factsFor(room))
}

// directions is [display.direction]'s hand-set lists.
func (m Model) directions() domain.Directions {
	d := m.prefs.display.Direction
	return domain.Directions{RTL: d.RTL, LTR: d.LTR}
}

// guessLayout guesses from the newest guessWindow messages' words.
func guessLayout(msgs []domain.Message) domain.Layout {
	bodies := make([]string, 0, min(len(msgs), guessWindow))
	for i := len(msgs) - 1; i >= 0 && len(bodies) < guessWindow; i-- {
		if msg := msgs[i]; !msg.Redacted && msg.Media == nil {
			bodies = append(bodies, msg.Body)
		}
	}
	return domain.GuessLayout(bodies)
}

// cycleDirection sets the room under the cursor by hand: right to left, then left to
// right, then back to what its space or the guess says. Only the room's own entry
// moves; one naming its space is edited in the config.
func (m Model) cycleDirection() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok {
		return m, nil
	}
	name := m.roomName(room)
	id := string(room.ID)
	dirs := m.directions()
	next, done := domain.LayoutRTL, name+" reads right to left"
	switch dirs.Lists(id) {
	case domain.LayoutRTL:
		next, done = domain.LayoutLTR, name+" reads left to right"
	case domain.LayoutLTR:
		next, done = domain.LayoutUnset, name+" takes its direction from [display.direction] again"
	case domain.LayoutUnset:
	}
	dirs = dirs.With(id, next)
	display := m.prefs.display
	display.Direction.RTL, display.Direction.LTR = dirs.RTL, dirs.LTR
	return m.applyDisplay(display, done)
}

// padStart flushes a drawn row against the right edge of width.
func padStart(row string, width int) string {
	if pad := width - ansi.StringWidth(row); pad > 0 {
		return strings.Repeat(" ", pad) + row
	}
	return row
}

// leadRow puts lead before a flushed row's text, keeping it against the right edge:
// what trails a message in reading order leads it on screen when it reads right to left.
func leadRow(row, lead string, width int) string {
	return padStart(lead+strings.TrimLeft(row, " "), width)
}

// mirroredChips is reaction chips in reading order for a mirrored row: the first read
// is the rightmost.
func mirroredChips(chips []string) []string {
	out := slices.Clone(chips)
	slices.Reverse(out)
	return out
}
