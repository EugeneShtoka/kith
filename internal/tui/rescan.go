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

// peopleRescanner is a daemon that judges senders by the filters' sender moves.
type peopleRescanner interface {
	RescanPeople(ctx context.Context, since time.Time, apply bool, people []string) ([]domain.PersonCandidate, error)
}

// rescanTimeout bounds a rescan: it reads every room's cached history.
const rescanTimeout = 2 * time.Minute

type rescannedMsg struct {
	since   time.Time
	applied bool
	found   []domain.MoveCandidate
	people  []domain.PersonCandidate
	err     error
}

// openRescan previews which rooms the filters would move and which people they would
// mark, judging what is cached since arg ago (everything when empty); nothing changes
// until the picker is applied.
func (m Model) openRescan(arg string) (Model, tea.Cmd) {
	since, err := parseSince(arg, time.Now())
	if err != nil {
		return m.sayErr("rescan", err), nil
	}
	return m.say("rescanning cached messages"), m.rescanCmd(since, false, nil, nil)
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

// rescanCmd previews (apply false) or applies a rescan. Applying asks only for what
// was ticked: an empty list there means everything to the daemon.
func (m Model) rescanCmd(since time.Time, apply bool, rooms []domain.RoomID, people []string) tea.Cmd {
	rescanner, ok := m.backend.(filterRescanner)
	if !ok {
		return func() tea.Msg { return rescannedMsg{err: fmt.Errorf("only kithd rescans with the filters")} }
	}
	persons, judgesPeople := m.backend.(peopleRescanner)
	app := m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(app, rescanTimeout)
		defer cancel()
		out := rescannedMsg{since: since, applied: apply}
		if !apply || len(rooms) > 0 {
			out.found, out.err = rescanner.RescanFilters(ctx, since, apply, rooms)
		}
		if out.err == nil && judgesPeople && (!apply || len(people) > 0) {
			out.people, out.err = persons.RescanPeople(ctx, since, apply, people)
		}
		return out
	}
}

func (m Model) handleRescanned(msg rescannedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("rescan", msg.err), nil
	}
	if msg.applied {
		return m.say("made " + rescanSummary(len(msg.found), len(msg.people))), m.loadVerdictsCmd()
	}
	if len(msg.found)+len(msg.people) == 0 {
		return m.say("the filters move no room and mark no one on what is cached"), nil
	}
	items := make([]pickerItem, 0, len(msg.found)+len(msg.people))
	checked := make(map[string]bool, len(msg.found)+len(msg.people))
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
	for i := range msg.people {
		c := msg.people[i]
		id := markValue(c.Verdict)
		name := m.personName(c.Verdict.Person)
		items = append(items, pickerItem{
			label:  isolate(name),
			detail: "mark into " + isolate(m.tagLabel(c.Verdict.Tag)) + " · " + c.Verdict.Filter + " · " + isolate(firstLine(c.Sample.Body)),
			value:  id,
			match:  name + " " + c.Verdict.Person + " " + c.Verdict.Tag + " " + c.Verdict.Filter + " " + c.Sample.Body,
		})
		checked[id] = true
	}
	m.rescanSince = msg.since
	spec := pickerSpecs[pickerRescan]
	spec.title = "Make " + rescanSummary(len(msg.found), len(msg.people)) + "?"
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
	var rooms []domain.RoomID
	var people []string
	for _, v := range values {
		if person, ok := personOfMark(v); ok {
			if !slices.Contains(people, person) {
				people = append(people, person)
			}
		} else if room := roomOfMove(v); !slices.Contains(rooms, room) {
			rooms = append(rooms, room)
		}
	}
	return m, m.rescanCmd(since, true, rooms, people)
}

// A move's picker value is its room and tag; applying moves a room into every tag the
// rescan found for it.
func moveValue(v domain.TagVerdict) string { return string(v.Room) + "\x00" + v.Tag }

func roomOfMove(value string) domain.RoomID {
	room, _, _ := strings.Cut(value, "\x00")
	return domain.RoomID(room)
}

// A mark's picker value is its person and tag behind a prefix no room ID has; applying
// marks the person into every tag the rescan found for them.
const markPrefix = "person\x00"

func markValue(v domain.PersonVerdict) string { return markPrefix + v.Person + "\x00" + v.Tag }

func personOfMark(value string) (string, bool) {
	rest, ok := strings.CutPrefix(value, markPrefix)
	if !ok {
		return "", false
	}
	person, _, _ := strings.Cut(rest, "\x00")
	return person, true
}

// rescanSummary is what a rescan found or did: "2 moves and 1 mark".
func rescanSummary(moves, marks int) string {
	switch {
	case marks == 0:
		return movesPhrase(moves)
	case moves == 0:
		return marksPhrase(marks)
	}
	return movesPhrase(moves) + " and " + marksPhrase(marks)
}

// marksPhrase pluralizes a count of marks.
func marksPhrase(n int) string {
	if n == 1 {
		return "1 mark"
	}
	return fmt.Sprintf("%d marks", n)
}

// movesPhrase pluralizes a count of moves.
func movesPhrase(n int) string {
	if n == 1 {
		return "1 move"
	}
	return fmt.Sprintf("%d moves", n)
}
