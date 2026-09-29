package domain

import (
	"strconv"
	"strings"
	"time"
)

// Narrowing a search by who said it and roughly when.

// SearchFilter is a parsed query: the terms to match, and what to narrow them to.
type SearchFilter struct {
	// Terms is what is left after the filters are lifted out — what FTS5 matches.
	Terms string
	// Sender is an MXID or a display-name fragment.
	Sender string
	// Since and Until bound the timestamps, inclusive of Since and exclusive of Until.
	Since time.Time
	Until time.Time
	// Mentioned narrows to the messages that name *us*.
	Mentioned bool
	// HasFile narrows to the messages carrying an attachment.
	HasFile bool
	// Starred narrows to the messages you have starred.
	Starred bool
	// Tracked narrows to the messages carrying one of your tracked words, and Words is
	// which of them to look for — the whole list, or one entry when `/tracked <word>`
	// named it.
	Tracked bool
	Words   []string
}

// Empty reports whether the filter would match on nothing at all — no terms and no
// narrowing. Searching for that is a question with no question in it.
func (f SearchFilter) Empty() bool {
	return f.Terms == "" && f.Sender == "" && f.Since.IsZero() && f.Until.IsZero() &&
		!f.Mentioned && !f.HasFile && !f.Starred && !f.Tracked
}

// ParseSearch lifts the filter tokens out of an input and returns them beside the terms
// that remain. now is passed in rather than read so that "7d" is testable and so the
// pure layer keeps its one rule: no clocks.
func ParseSearch(input string, now time.Time) SearchFilter {
	var filter SearchFilter
	var terms []string
	for field := range strings.FieldsSeq(input) {
		name, value, found := strings.Cut(field, ":")
		if !found || value == "" {
			terms = append(terms, field)
			continue
		}
		switch strings.ToLower(name) {
		case "from":
			filter.Sender = strings.TrimPrefix(value, "@") // typed either way
			if strings.Contains(value, ":") {
				filter.Sender = value // a full MXID, colons and all
			}
		case "since":
			if at, ok := parseWhen(value, now, false); ok {
				filter.Since = at
				continue
			}
			terms = append(terms, field)
		case "until":
			if at, ok := parseWhen(value, now, true); ok {
				filter.Until = at
				continue
			}
			terms = append(terms, field)
		default:
			terms = append(terms, field)
		}
	}
	filter.Terms = strings.Join(terms, " ")
	return filter
}

// parseWhen reads a date bound: a relative span (7d, 2w, 3m), a word (today,
// yesterday), or an absolute date (2026-08-01).
func parseWhen(value string, now time.Time, endOfDay bool) (time.Time, bool) {
	day := func(t time.Time) time.Time {
		y, m, d := t.Date()
		start := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
		if endOfDay {
			return start.AddDate(0, 0, 1)
		}
		return start
	}
	switch strings.ToLower(value) {
	case "today":
		return day(now), true
	case "yesterday":
		return day(now.AddDate(0, 0, -1)), true
	}
	if at, err := time.ParseInLocation("2006-01-02", value, now.Location()); err == nil {
		return day(at), true
	}
	// A relative span: a count and a unit, counted back from now.
	if len(value) < 2 {
		return time.Time{}, false
	}
	n, err := strconv.Atoi(value[:len(value)-1])
	if err != nil || n < 0 {
		return time.Time{}, false
	}
	switch value[len(value)-1] {
	case 'h':
		// Hours, added for `/summary 2h` and useful to `since:` for the same reason:
		// the span somebody means when they say "what happened while I was at lunch" is
		// shorter than a day.
		return now.Add(-time.Duration(n) * time.Hour), true
	case 'd':
		return now.AddDate(0, 0, -n), true
	case 'w':
		return now.AddDate(0, 0, -7*n), true
	case 'm':
		return now.AddDate(0, -n, 0), true
	case 'y':
		return now.AddDate(-n, 0, 0), true
	}
	return time.Time{}, false
}

// SearchRequest is one search: what to match, where to look, and how much to return.
type SearchRequest struct {
	Filter SearchFilter
	// Rooms is which rooms are searched (EveryRoom for all; empty is none).
	Rooms RoomSet
	Limit int
}
