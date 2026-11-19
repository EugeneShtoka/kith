package domain

import (
	"slices"
	"time"
)

// WorkingHours is one [[agent.write.hours]]: when an assistant may send to the rooms
// Match names. Outside them a send waits for the next opening.
type WorkingHours struct {
	// Match is one place-vocabulary entry; empty names every room.
	Match string
	// Start and End are offsets from local midnight; an End before Start wraps past
	// midnight, and those hours belong to the day they start on.
	Start, End time.Duration
	// Days are the days the hours open on; empty is every day.
	Days []time.Weekday
}

// HoursFor is the narrowest entry naming the room, the first of equals; ok is false
// when none does, so any time will do.
func HoursFor(hours []WorkingHours, room RoomFacts) (WorkingHours, bool) {
	at, best := -1, EntryInvalid-1
	for i := range hours {
		kind, ok := EntryInvalid, hours[i].Match == ""
		if !ok {
			kind, ok = room.Match(hours[i].Match)
		}
		if ok && kind > best {
			at, best = i, kind
		}
	}
	if at < 0 {
		return WorkingHours{}, false
	}
	return hours[at], true
}

// Covers reports whether t is inside the hours, on t's own clock.
func (h WorkingHours) Covers(t time.Time) bool {
	at, day := sinceMidnight(t), t.Weekday()
	var in bool
	switch {
	case h.Start < h.End:
		in = at >= h.Start && at < h.End
	case h.Start > h.End:
		in = at >= h.Start || at < h.End
		if at < h.End {
			day = (day + 6) % 7 // the small hours belong to the day before
		}
	}
	return in && (len(h.Days) == 0 || slices.Contains(h.Days, day))
}

// Next is t when the hours cover it, else when they next open; t when they never do.
func (h WorkingHours) Next(t time.Time) time.Time {
	if h.Covers(t) {
		return t
	}
	y, m, d := t.Date()
	for ahead := range 8 {
		// Built from the clock, not midnight plus Start, so a DST day opens on time.
		open := time.Date(y, m, d+ahead, int(h.Start/time.Hour), int(h.Start%time.Hour/time.Minute), 0, 0, t.Location())
		if open.After(t) && h.Covers(open) {
			return open
		}
	}
	return t
}

// sinceMidnight is t's wall-clock time as an offset from its own midnight.
func sinceMidnight(t time.Time) time.Duration {
	hh, mm, ss := t.Clock()
	return time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute + time.Duration(ss)*time.Second
}
