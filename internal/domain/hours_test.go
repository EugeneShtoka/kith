package domain_test

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The narrowest entry naming the room applies, the first of equals; none naming it
// means any time.
func TestHoursForTakesTheNarrowest(t *testing.T) {
	t.Parallel()
	room := domain.RoomFacts{ID: "matrix:a/!standup:x", Name: "Standup", Spaces: []string{"Work"}}
	everywhere := domain.WorkingHours{Start: 9 * time.Hour, End: 18 * time.Hour}
	work := domain.WorkingHours{Match: "space:Work", Start: 8 * time.Hour, End: 17 * time.Hour}
	standup := domain.WorkingHours{Match: "room:Standup", Start: 10 * time.Hour, End: 11 * time.Hour}
	other := domain.WorkingHours{Match: "room:Other", Start: time.Hour, End: 2 * time.Hour}
	for _, c := range []struct {
		name  string
		hours []domain.WorkingHours
		want  domain.WorkingHours
		ok    bool
	}{
		{"none", nil, domain.WorkingHours{}, false},
		{"only another room's", []domain.WorkingHours{other}, domain.WorkingHours{}, false},
		{"everywhere", []domain.WorkingHours{other, everywhere}, everywhere, true},
		{"a space over everywhere", []domain.WorkingHours{work, everywhere}, work, true},
		{"the room over its space", []domain.WorkingHours{everywhere, work, standup}, standup, true},
		{"first of equals", []domain.WorkingHours{work, {Match: "group", Start: time.Hour, End: 3 * time.Hour}}, work, true},
	} {
		got, ok := domain.HoursFor(c.hours, room)
		if ok != c.ok || got.Match != c.want.Match || got.Start != c.want.Start {
			t.Errorf("%s: got %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// Next is the time itself inside the hours, else their next opening: later today,
// tomorrow, past the days they are closed, and for hours past midnight, on the day
// they start.
func TestNextIsTheNextOpening(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("+02", 2*60*60)
	at := func(day, hh, mm int) time.Time { return time.Date(2026, 11, day, hh, mm, 0, 0, loc) } // 16th is a Monday
	weekdays := []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday}
	office := domain.WorkingHours{Start: 9 * time.Hour, End: 18 * time.Hour, Days: weekdays}
	night := domain.WorkingHours{Start: 22 * time.Hour, End: 2 * time.Hour, Days: []time.Weekday{time.Friday}}
	for _, c := range []struct {
		name      string
		hours     domain.WorkingHours
		now, want time.Time
	}{
		{"inside", office, at(17, 10, 30), at(17, 10, 30)},
		{"at the opening", office, at(17, 9, 0), at(17, 9, 0)},
		{"before it opens", office, at(17, 7, 15), at(17, 9, 0)},
		{"at the closing", office, at(17, 18, 0), at(18, 9, 0)},
		{"thursday evening, past the weekend", office, at(19, 20, 0), at(22, 9, 0)},
		{"saturday", office, at(21, 12, 0), at(22, 9, 0)},
		{"every day, late", domain.WorkingHours{Start: 9 * time.Hour, End: 18 * time.Hour}, at(21, 23, 0), at(22, 9, 0)},
		{"friday night", night, at(20, 23, 0), at(20, 23, 0)},
		{"saturday small hours belong to friday", night, at(21, 1, 0), at(21, 1, 0)},
		{"after them, a week on", night, at(21, 3, 0), at(27, 22, 0)},
		{"friday small hours belong to thursday", night, at(20, 1, 0), at(20, 22, 0)},
		{"never open", domain.WorkingHours{Start: time.Hour, End: time.Hour}, at(17, 5, 0), at(17, 5, 0)},
	} {
		if got := c.hours.Next(c.now); !got.Equal(c.want) {
			t.Errorf("%s: Next(%s) = %s, want %s", c.name, c.now.Format("Mon 15:04"), got.Format("Mon 02 15:04"), c.want.Format("Mon 02 15:04"))
		}
	}
}

// On the day clocks go forward, the opening is at its wall-clock time.
func TestNextOpensOnTheClockAcrossDST(t *testing.T) {
	t.Parallel()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no zone data: %v", err)
	}
	h := domain.WorkingHours{Start: 9 * time.Hour, End: 18 * time.Hour}
	now := time.Date(2026, 3, 28, 20, 0, 0, 0, berlin) // the night before the change
	if got := h.Next(now); got.Hour() != 9 || got.Day() != 29 {
		t.Errorf("Next = %s, want 09:00 on the 29th", got)
	}
}
