package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/rivo/uniseg"
)

// Clock is how times and dates are written for people, everywhere kith shows one:
// a time by a named style (TimeStyles), a date by a pattern of tokens (see
// ParseDatePattern), a long one for headings and a short one for columns. The zero
// Clock writes kith's defaults. Times meant for programs (RFC 3339) never use it.
type Clock struct {
	style       timeStyle
	long, short []dateToken
}

// Default patterns: what kith wrote before they could be chosen.
const (
	DefaultTimeStyle = "24h"
	DefaultLongDate  = "DDD, DD MMM YYYY"
	DefaultShortDate = "YYYY-MM-DD"
)

// timeStyle writes a time of day, with or without seconds.
type timeStyle struct {
	name string
	// layout is a time.Format layout of hours and minutes; seconds adds ":05" after
	// the minutes.
	layout string
	// half, when set, are the words for before and after noon, and before says they
	// precede the time.
	am, pm string
	before bool
}

// timeStyles are the accepted time styles, in the order a settings list shows them.
var timeStyles = []timeStyle{
	{name: "24h", layout: "15:04"},
	{name: "12h", layout: "3:04", am: " AM", pm: " PM"},
	{name: "12h-lower", layout: "3:04", am: "am", pm: "pm"},
	{name: "zh", layout: "3:04", am: "上午", pm: "下午", before: true},
	{name: "ja", layout: "3:04", am: "午前", pm: "午後", before: true},
	{name: "ko", layout: "3:04", am: "오전 ", pm: "오후 ", before: true},
}

// TimeStyles is the accepted time style names, each with how 14:30 looks in it.
func TimeStyles() [][2]string {
	at := time.Date(2026, 10, 8, 14, 30, 0, 0, time.UTC)
	out := make([][2]string, len(timeStyles))
	for i, s := range timeStyles {
		out[i] = [2]string{s.name, s.write(at, false)}
	}
	return out
}

// ParseClock reads a time style and two date patterns; an empty one is its default.
func ParseClock(style, long, short string) (Clock, error) {
	var c Clock
	var err error
	if c.style, err = parseTimeStyle(style); err != nil {
		return Clock{}, err
	}
	if c.long, err = ParseDatePattern(long); err != nil {
		return Clock{}, err
	}
	if c.short, err = ParseDatePattern(short); err != nil {
		return Clock{}, err
	}
	return c, nil
}

func parseTimeStyle(name string) (timeStyle, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultTimeStyle
	}
	names := make([]string, len(timeStyles))
	for i, s := range timeStyles {
		if s.name == name {
			return s, nil
		}
		names[i] = s.name
	}
	return timeStyle{}, fmt.Errorf("unknown time style %q (one of %s)", name, strings.Join(names, ", "))
}

// Time is t's time of day, in the local zone.
func (c Clock) Time(t time.Time) string { return c.styleOr().write(t.Local(), false) }

// TimeSeconds is Time with the seconds.
func (c Clock) TimeSeconds(t time.Time) string { return c.styleOr().write(t.Local(), true) }

// LongDate is t's date as a heading writes it.
func (c Clock) LongDate(t time.Time) string { return writeDate(c.longOr(), t.Local()) }

// ShortDate is t's date as a column writes it.
func (c Clock) ShortDate(t time.Time) string { return writeDate(c.shortOr(), t.Local()) }

// TimeWidth is the most cells Time takes on any time of day, so a column of times
// lines up; a shorter time is padded on the left.
func (c Clock) TimeWidth() int {
	widest := 0
	for h := range 24 {
		at := time.Date(2026, 1, 1, h, 59, 0, 0, time.Local)
		widest = max(widest, uniseg.StringWidth(c.styleOr().write(at, false)))
	}
	return widest
}

// PaddedTime is Time padded on the left to TimeWidth.
func (c Clock) PaddedTime(t time.Time) string {
	s := c.Time(t)
	return strings.Repeat(" ", max(c.TimeWidth()-uniseg.StringWidth(s), 0)) + s
}

func (c Clock) styleOr() timeStyle {
	if c.style.layout == "" {
		return timeStyles[0]
	}
	return c.style
}

func (c Clock) longOr() []dateToken {
	if c.long == nil {
		c.long, _ = ParseDatePattern(DefaultLongDate)
	}
	return c.long
}

func (c Clock) shortOr() []dateToken {
	if c.short == nil {
		c.short, _ = ParseDatePattern(DefaultShortDate)
	}
	return c.short
}

func (s timeStyle) write(t time.Time, seconds bool) string {
	layout := s.layout
	if seconds {
		layout += ":05"
	}
	out := t.Format(layout)
	if s.am == "" {
		return out
	}
	half := s.am
	if t.Hour() >= 12 {
		half = s.pm
	}
	if s.before {
		return half + out
	}
	return out + half
}

// dateToken is a piece of a date pattern: a field (kind and how many letters) or
// literal text.
type dateToken struct {
	kind    rune // 'Y', 'M', 'D', or 0 for literal
	n       int
	literal string
}

// ParseDatePattern reads a date pattern; "" is nil, which writes the default. Runs of Y,
// M and D are fields; everything else is written as it stands:
//
//	YYYY 2026   YY 26
//	M    10     MM 10   MMM Oct   MMMM October
//	D    8      DD 08   DDD Thu   DDDD Thursday
//
// so "DD.MM.YYYY", "MM/DD/YY", "DDD, D MMMM YYYY" and "YYYY年M月D日" all work.
// Names are English.
func ParseDatePattern(pattern string) ([]dateToken, error) {
	var out []dateToken
	runes := []rune(pattern)
	for i := 0; i < len(runes); {
		r := runes[i]
		if r != 'Y' && r != 'M' && r != 'D' {
			j := i
			for j < len(runes) && runes[j] != 'Y' && runes[j] != 'M' && runes[j] != 'D' {
				j++
			}
			out = append(out, dateToken{literal: string(runes[i:j])})
			i = j
			continue
		}
		j := i
		for j < len(runes) && runes[j] == r {
			j++
		}
		n := j - i
		switch {
		case r == 'Y' && n != 2 && n != 4:
			return nil, fmt.Errorf("%q in %q: write YY or YYYY for the year", string(runes[i:j]), pattern)
		case r != 'Y' && n > 4:
			return nil, fmt.Errorf("%q in %q: at most four %c", string(runes[i:j]), pattern, r)
		}
		out = append(out, dateToken{kind: r, n: n})
		i = j
	}
	return out, nil
}

func writeDate(tokens []dateToken, t time.Time) string {
	var b strings.Builder
	for _, tok := range tokens {
		switch tok.kind {
		case 0:
			b.WriteString(tok.literal)
		case 'Y':
			if tok.n == 2 {
				b.WriteString(t.Format("06"))
			} else {
				b.WriteString(t.Format("2006"))
			}
		case 'M':
			b.WriteString(t.Format([]string{"1", "01", "Jan", "January"}[tok.n-1]))
		case 'D':
			b.WriteString(t.Format([]string{"2", "02", "Mon", "Monday"}[tok.n-1]))
		}
	}
	return b.String()
}
