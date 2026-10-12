package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// filterRescanner is a daemon that judges cached messages by the filters.
type filterRescanner interface {
	RescanFilters(ctx context.Context, since time.Time, apply bool, rooms []domain.RoomID) ([]domain.MoveCandidate, error)
}

// rescanTimeout bounds a rescan: it reads every room's cached history.
const rescanTimeout = 2 * time.Minute

type rescannedMsg struct {
	since   time.Time
	applied bool
	found   []domain.MoveCandidate
	err     error
}

// openRescan previews which rooms the filters would move, judging what is cached
// since arg ago (everything when empty); nothing moves until the picker is applied.
func (m Model) openRescan(arg string) (Model, tea.Cmd) {
	since, err := parseSince(arg, time.Now())
	if err != nil {
		return m.sayErr("rescan", err), nil
	}
	return m.say("rescanning cached messages"), m.rescanCmd(since, false, nil)
}

// parseSince reads "30d", "12h" or "1h30m" as that long before now; "" is the zero time.
func parseSince(arg string, now time.Time) (time.Time, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return time.Time{}, nil
	}
	var span time.Duration
	if days, ok := strings.CutSuffix(arg, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return time.Time{}, fmt.Errorf("%q is not a number of days — try 30d", arg)
		}
		span = time.Duration(n) * 24 * time.Hour
	} else {
		d, err := time.ParseDuration(arg)
		if err != nil || d <= 0 {
			return time.Time{}, fmt.Errorf("%q is not a span — try 30d, 12h", arg)
		}
		span = d
	}
	return now.Add(-span), nil
}

func (m Model) rescanCmd(since time.Time, apply bool, rooms []domain.RoomID) tea.Cmd {
	rescanner, ok := m.backend.(filterRescanner)
	if !ok {
		return func() tea.Msg { return rescannedMsg{err: fmt.Errorf("only kithd rescans with the filters")} }
	}
	app := m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(app, rescanTimeout)
		defer cancel()
		found, err := rescanner.RescanFilters(ctx, since, apply, rooms)
		return rescannedMsg{since: since, applied: apply, found: found, err: err}
	}
}

func (m Model) handleRescanned(msg rescannedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("rescan", msg.err), nil
	}
	if msg.applied {
		return m.say(fmt.Sprintf("moved %s", movesPhrase(len(msg.found)))), m.loadVerdictsCmd()
	}
	if len(msg.found) == 0 {
		return m.say("the filters move no room on what is cached"), nil
	}
	items := make([]pickerItem, 0, len(msg.found))
	checked := make(map[string]bool, len(msg.found))
	for i := range msg.found {
		c := msg.found[i]
		id := moveValue(c.Verdict)
		name := string(c.Verdict.Room)
		if room, ok := m.roomByID(c.Verdict.Room); ok {
			name = m.roomName(room)
		}
		items = append(items, pickerItem{
			label:  isolate(name),
			detail: "into " + isolate(m.tagLabel(c.Verdict.Tag)) + " · " + c.Verdict.Filter + " · " + isolate(firstLine(c.Sample.Body)),
			value:  id,
			match:  name + " " + c.Verdict.Tag + " " + c.Verdict.Filter + " " + c.Sample.Body,
		})
		checked[id] = true
	}
	m.rescanSince = msg.since
	spec := pickerSpecs[pickerRescan]
	spec.title = fmt.Sprintf("Make %s?", movesPhrase(len(items)))
	m.picker = newPickerWith(pickerRescan, spec, items)
	m.picker.checked = checked
	return m, nil
}

// applyRescan moves the ticked rooms, judged again as the daemon finds them now.
func (m Model) applyRescan(values []string) (Model, tea.Cmd) {
	since := m.rescanSince
	m = m.closePicker()
	if len(values) == 0 {
		return m.say("nothing moved"), nil
	}
	rooms := make([]domain.RoomID, 0, len(values))
	for _, v := range values {
		if room := roomOfMove(v); !slices.Contains(rooms, room) {
			rooms = append(rooms, room)
		}
	}
	return m, m.rescanCmd(since, true, rooms)
}

// A move's picker value is its room and tag; applying moves a room into every tag the
// rescan found for it.
func moveValue(v domain.TagVerdict) string { return string(v.Room) + "\x00" + v.Tag }

func roomOfMove(value string) domain.RoomID {
	room, _, _ := strings.Cut(value, "\x00")
	return domain.RoomID(room)
}

// movesPhrase pluralizes a count of moves.
func movesPhrase(n int) string {
	if n == 1 {
		return "1 move"
	}
	return fmt.Sprintf("%d moves", n)
}
