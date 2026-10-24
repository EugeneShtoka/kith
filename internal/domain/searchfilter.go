package domain

import (
	"strconv"
	"strings"
	"time"
)

// Search filters: sender, date bounds and flags lifted out of a query.

// SearchFilter is a parsed query: terms to match and filters to narrow by.
type SearchFilter struct {
	// Terms is what FTS5 matches, after the filters are lifted out.
	Terms string
	// Sender is an MXID or a display-name fragment.
	Sender string
	// SenderField is a phonebook field term after from: (from:role:attorney), which the
	// client resolves into Senders.
	SenderField string
	// Senders narrows to messages from any of these IDs; nil leaves it open.
	Senders []string
	// Since is inclusive, Until exclusive.
	Since time.Time
	Until time.Time
	// Mentioned narrows to messages that mention you.
	Mentioned bool
	// HasFile narrows to messages with an attachment.
	HasFile bool
	// Starred narrows to messages you starred.
	Starred bool
	// Tracked narrows to messages with one of Words: all tracked words, or the one
	// `/tracked <word>` named.
	Tracked bool
	Words   []string
}

// Empty reports whether the filter has no terms and no narrowing.
func (f SearchFilter) Empty() bool {
	return f.Terms == "" && f.Sender == "" && f.SenderField == "" && f.Senders == nil && f.Since.IsZero() && f.Until.IsZero() &&
		!f.Mentioned && !f.HasFile && !f.Starred && !f.Tracked
}

// ParseSearch lifts filter tokens (from:, since:, until:) out of input; the rest are
// terms. now is passed in because the domain layer reads no clock.
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
			switch {
			case fieldTerm(value):
				filter.SenderField = value
			case strings.Contains(value, ":"):
				filter.Sender = TypedUserID(value) // a full ID, colons and all
			default:
				filter.Sender = strings.TrimPrefix(value, "@") // typed either way
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

// fieldTerm reports whether a from: value is a phonebook field term (key:value) rather
// than an ID.
func fieldTerm(value string) bool {
	key, rest, ok := strings.Cut(value, ":")
	return ok && key != "" && rest != "" && !strings.HasPrefix(value, "@") && !IsUserID(value)
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
	// A relative span, counted back from now.
	if len(value) < 2 {
		return time.Time{}, false
	}
	n, err := strconv.Atoi(value[:len(value)-1])
	if err != nil || n < 0 {
		return time.Time{}, false
	}
	switch value[len(value)-1] {
	case 'h':
		// Hours, for spans shorter than a day (`/summary 2h`).
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
