package notify

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Level is the bar an incoming message must clear to earn a notification.
type Level int

// The trigger levels, weakest bar last.
const (
	LevelNone Level = iota
	LevelMention
	LevelDM
	LevelAll
)

// levelNames maps each Level to the spelling used in the config file.
var levelNames = map[Level]string{
	LevelNone:    "none",
	LevelMention: "mention",
	LevelDM:      "dm",
	LevelAll:     "all",
}

// Levels is every trigger level, weakest bar first — the order a picker steps through
// and an error lists.
func Levels() []Level { return []Level{LevelNone, LevelMention, LevelDM, LevelAll} }

// String returns the level's config spelling.
func (l Level) String() string {
	if name, ok := levelNames[l]; ok {
		return name
	}
	return "level(" + strconv.Itoa(int(l)) + ")"
}

// ParseLevel reads a configured level: "none", "mention", "dm" or "all".
func ParseLevel(s string) (Level, error) {
	for level, name := range levelNames {
		if strings.EqualFold(strings.TrimSpace(s), name) {
			return level, nil
		}
	}
	return LevelNone, fmt.Errorf("notify: unknown level %q (want none, mention, dm or all)", s)
}

// Window is a daily wall-clock window, held as offsets from local midnight.
type Window struct {
	Start, End time.Duration
}

// ParseWhen reads a window written "HH:MM-HH:MM"; empty is the zero Window. An end
// before the start wraps past midnight.
func ParseWhen(s string) (Window, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Window{}, nil
	}
	start, end, ok := strings.Cut(s, "-")
	from, to := strings.TrimSpace(start), strings.TrimSpace(end)
	if !ok || from == "" || to == "" {
		return Window{}, fmt.Errorf("notify: %q is not a window (want HH:MM-HH:MM)", s)
	}
	startAt, err := parseClock(from)
	if err != nil {
		return Window{}, fmt.Errorf("notify: window start: %w", err)
	}
	endAt, err := parseClock(to)
	if err != nil {
		return Window{}, fmt.Errorf("notify: window end: %w", err)
	}
	return Window{Start: startAt, End: endAt}, nil
}

// Covers reports whether t's local wall clock falls inside the window, treating the
// start as inclusive and the end as exclusive. A zero-length window covers nothing.
func (w Window) Covers(t time.Time) bool {
	if w.Start == w.End {
		return false
	}
	at := sinceMidnight(t.Local())
	if w.Start < w.End {
		return at >= w.Start && at < w.End
	}
	return at >= w.Start || at < w.End // wraps past midnight
}

// String renders the window as "HH:MM-HH:MM", or "none" when it covers nothing.
func (w Window) String() string {
	if w.Start == w.End {
		return "none"
	}
	return clockString(w.Start) + "-" + clockString(w.End)
}

// parseClock reads an "HH:MM" (or "H:MM") wall-clock time as an offset from
// midnight.
func parseClock(s string) (time.Duration, error) {
	hh, mm, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, fmt.Errorf("%q is not an HH:MM time", s)
	}
	hours, err := strconv.Atoi(hh)
	if err != nil || hours < 0 || hours > 23 {
		return 0, fmt.Errorf("%q has no valid hour (want 00-23)", s)
	}
	minutes, err := strconv.Atoi(mm)
	if err != nil || minutes < 0 || minutes > 59 {
		return 0, fmt.Errorf("%q has no valid minute (want 00-59)", s)
	}
	return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute, nil
}

// clockString renders an offset from midnight as "HH:MM".
func clockString(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// sinceMidnight is t's wall-clock time as an offset from its own midnight.
func sinceMidnight(t time.Time) time.Duration {
	h, m, s := t.Clock()
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second
}

// Event is what the rules decide about: the facts about one incoming message that
// bear on whether it should interrupt you.
type Event struct {
	// Mine marks our own message. It never notifies, whatever the levels say.
	Mine bool
	// Mentioned marks a message that mentions us (m.mentions or a pill).
	Mentioned bool
	// Direct marks a message in a direct-message room.
	Direct bool
	// Tracked marks a message carrying one of the user's tracked words, and only when
	// `[tracked] notify` is on — the switch is read where the event is built, so a rule
	// never has to ask whether tracking is meant to interrupt.
	Tracked bool
}

// Admits reports whether e clears the bar l. The levels are cumulative, so a bar of
// LevelDM admits a mention as well as a direct message.
func (l Level) Admits(e Event) bool {
	switch l {
	case LevelAll:
		return true
	case LevelDM:
		return e.Mentioned || e.Tracked || e.Direct
	case LevelMention:
		// A tracked word clears the mention bar, which is the whole of what tracking
		// promises when it is allowed to notify.
		return e.Mentioned || e.Tracked
	case LevelNone:
		return false
	default:
		return false
	}
}
