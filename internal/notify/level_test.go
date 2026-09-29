package notify_test

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/notify"
)

// at builds a local wall-clock time on a fixed day, for the quiet-hours tests.
func at(hour, min int) time.Time {
	return time.Date(2026, 8, 21, hour, min, 0, 0, time.Local)
}

func TestParseLevel(t *testing.T) {
	t.Parallel()

	// Pairs rather than a map: one input is deliberately padded, which a map key linter
	// flags as suspicious.
	ok := []struct {
		in   string
		want notify.Level
	}{
		{"none", notify.LevelNone},
		{"mention", notify.LevelMention},
		{"dm", notify.LevelDM},
		{"all", notify.LevelAll},
		{"  All ", notify.LevelAll}, // trimmed and case-folded
		{"DM", notify.LevelDM},
	}
	for _, tc := range ok {
		in, want := tc.in, tc.want
		got, err := notify.ParseLevel(in)
		if err != nil {
			t.Errorf("ParseLevel(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
	// A typo is an error, not a silent fallback — silence is this feature's failure
	// mode and would be indistinguishable from working.
	for _, bad := range []string{"", "mentions", "direct", "everything", "1"} {
		if _, err := notify.ParseLevel(bad); err == nil {
			t.Errorf("ParseLevel(%q) should have errored", bad)
		}
	}
	if got := notify.LevelDM.String(); got != "dm" {
		t.Errorf("LevelDM.String() = %q, want dm", got)
	}
}

func TestParseWhen(t *testing.T) {
	t.Parallel()

	w, err := notify.ParseWhen("")
	if err != nil || w.Covers(at(3, 0)) || w.String() != "none" {
		t.Errorf("unset window = %v, %v; want one covering nothing", w, err)
	}
	if w, err = notify.ParseWhen(" 22:00 - 8:30 "); err != nil || w.String() != "22:00-08:30" {
		t.Errorf("ParseWhen = %v, %v; want 22:00-08:30", w, err)
	}
	for _, bad := range []string{
		"22:00", "22:00-", "-08:00", "22-08:00", "22:00-08",
		"24:00-08:00", "22:60-08:00", "ten-08:00", "22:00 to 08:00",
	} {
		if _, err := notify.ParseWhen(bad); err == nil {
			t.Errorf("ParseWhen(%q) should have errored", bad)
		}
	}
}

func TestWindowCovers(t *testing.T) {
	t.Parallel()

	overnight, err := notify.ParseWhen("22:00-08:00")
	if err != nil {
		t.Fatal(err)
	}
	daytime, err := notify.ParseWhen("09:00-17:00")
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		w    notify.Window
		when time.Time
		want bool
	}{
		// An overnight window is the evening plus the morning, not the 22 hours between
		// — the bug this wrap-around exists to avoid.
		"overnight, late evening":  {overnight, at(23, 30), true},
		"overnight, small hours":   {overnight, at(3, 0), true},
		"overnight, at the start":  {overnight, at(22, 0), true},  // start inclusive
		"overnight, at the end":    {overnight, at(8, 0), false},  // end exclusive
		"overnight, mid-afternoon": {overnight, at(15, 0), false}, // the point of wrapping
		"overnight, one before":    {overnight, at(21, 59), false},

		"same-day, inside":      {daytime, at(12, 0), true},
		"same-day, at start":    {daytime, at(9, 0), true},
		"same-day, at end":      {daytime, at(17, 0), false},
		"same-day, before":      {daytime, at(8, 59), false},
		"same-day, small hours": {daytime, at(3, 0), false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.w.Covers(tc.when); got != tc.want {
				t.Errorf("Covers(%s) = %v, want %v", tc.when.Format("15:04"), got, tc.want)
			}
		})
	}
}

func TestLevelAdmits(t *testing.T) {
	t.Parallel()

	mention := notify.Event{Mentioned: true}
	direct := notify.Event{Direct: true}
	plain := notify.Event{}

	tests := map[string]struct {
		level notify.Level
		event notify.Event
		want  bool
	}{
		"none admits nothing":      {notify.LevelNone, mention, false},
		"mention admits a mention": {notify.LevelMention, mention, true},
		"mention refuses a DM":     {notify.LevelMention, direct, false},
		"dm admits a DM":           {notify.LevelDM, direct, true},
		"dm admits a mention too":  {notify.LevelDM, mention, true},
		"dm refuses the rest":      {notify.LevelDM, plain, false},
		"all admits anything":      {notify.LevelAll, plain, true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tc.level.Admits(tc.event); got != tc.want {
				t.Errorf("%v.Admits(%+v) = %v, want %v", tc.level, tc.event, got, tc.want)
			}
		})
	}
}

// A tracked word clears the mention bar, which is what tracking promises when it is
// allowed to interrupt — and it does so as its own fact, so nothing downstream reads a
// mention nobody made.
func TestTrackedWordClearsTheMentionBar(t *testing.T) {
	t.Parallel()

	tracked := notify.Event{Tracked: true}
	for _, level := range []notify.Level{notify.LevelMention, notify.LevelDM, notify.LevelAll} {
		if !level.Admits(tracked) {
			t.Errorf("%v refused a tracked word", level)
		}
	}
	if notify.LevelNone.Admits(tracked) {
		t.Error("LevelNone admitted a tracked word; silence has to mean silence")
	}
	// And it is not a mention: the two facts stay apart.
	if tracked.Mentioned {
		t.Error("a tracked hit set Mentioned, which would mislead the template and the badge")
	}
}
