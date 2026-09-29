package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Spam is the hand-written spam lists, in the place-entry vocabulary.
type Spam struct {
	Entries []string
	// Except is the carve-out; it outranks Entries and every rule.
	Except []string
}

// Has reports whether anything is listed.
func (s Spam) Has() bool { return len(s.Entries) > 0 }

// Excused reports whether a room is carved out; no rule overturns that.
func (s Spam) Excused(room RoomFacts) bool { return namesAny(s.Except, room) }

// Names reports whether the hand-written list names this room.
func (s Spam) Names(room RoomFacts) bool { return namesAny(s.Entries, room) }

// Lists reports whether this exact entry is in the list.
func (s Spam) Lists(entry string) bool { return slices.Contains(s.Entries, entry) }

// With adds or removes one entry from the hand-written list.
func (s Spam) With(entry string, spam bool) Spam {
	s.Entries = withEntry(s.Entries, entry, spam)
	return s
}

// Excusing adds or removes one exemption.
func (s Spam) Excusing(entry string, excused bool) Spam {
	s.Except = withEntry(s.Except, entry, excused)
	return s
}

// withEntry returns entries with entry removed, then appended if present (nil when
// empty).
func withEntry(entries []string, entry string, present bool) []string {
	out := make([]string, 0, len(entries)+1)
	for _, e := range entries {
		if e != entry {
			out = append(out, e)
		}
	}
	if present {
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SpamRule is what put a room in Spam.
type SpamRule int

const (
	// SpamNotSpam is the zero value: this room is not in Spam.
	SpamNotSpam SpamRule = iota
	// SpamByHand is you, saying so.
	SpamByHand
	// SpamFirstMessage: the room's first message was caught.
	SpamFirstMessage
	// SpamDirect: a one-sided direct message was caught.
	SpamDirect
	// SpamMostly: most messages over the window were caught.
	SpamMostly
)

// Reason is the rule in the words a status line and a `:why` answer use.
func (r SpamRule) Reason(filter string) string {
	named := ""
	if filter != "" {
		named = fmt.Sprintf(" (%s)", filter)
	}
	switch r {
	case SpamByHand:
		return "you marked it as spam"
	case SpamFirstMessage:
		return "the first message in it was caught by a filter" + named
	case SpamDirect:
		return "it is a direct message and its message was caught by a filter" + named
	case SpamMostly:
		return "most of what is said in it is caught by a filter" + named
	case SpamNotSpam:
		return ""
	}
	return ""
}

// SpamVerdict is one room's promotion: which rule, which filter, and when.
type SpamVerdict struct {
	Room   RoomID
	Rule   SpamRule
	Filter string
	At     time.Time
	// Released is a person taking the room out. No rule may put it back.
	Released bool
}

// Spam reports whether this verdict puts a room in Spam; a release never does.
func (v SpamVerdict) Spam() bool { return v.Rule != SpamNotSpam && !v.Released }

// ErrSpamReleased refuses a rule's verdict for a room a person released.
var ErrSpamReleased = errors.New("the room was released from Spam")

// SpamFilter identifies spam messages; SpamRules promote rooms.
type SpamFilter struct {
	Name  string  // reported when it catches something
	Words Tracked // with Tracked's glob widths
	From  string  // optional sender MXID
}

// Catches reports whether this filter catches one message.
func (f SpamFilter) Catches(body, sender string) bool {
	if f.From != "" && !strings.EqualFold(strings.TrimSpace(f.From), sender) {
		return false
	}
	if f.anything() {
		return f.From != ""
	}
	return len(f.Words.Find(body)) > 0
}

// anything reports whether the words match everything (none, or only stars).
func (f SpamFilter) anything() bool {
	for _, word := range f.Words.Words {
		if strings.Trim(strings.TrimSpace(word), "*") != "" {
			return false
		}
	}
	return true
}

// SpamRules is the whole of what promotes a room, and each part is its own switch.
type SpamRules struct {
	Filters      []SpamFilter
	FirstMessage bool
	Direct       bool
	// Ratio, Floor and Window: the caught share, the minimum message count, and the
	// period they are counted over.
	Ratio  float64
	Floor  int
	Window time.Duration
}

// Any reports whether any rule can fire.
func (r SpamRules) Any() bool { return len(r.Filters) > 0 }

// Catches reports which filter catches a message, if any.
func (r SpamRules) Catches(body, sender string) (SpamFilter, bool) {
	for _, filter := range r.Filters {
		if filter.Catches(body, sender) {
			return filter, true
		}
	}
	return SpamFilter{}, false
}

// SpamCase is what is known about one message when the rules are asked about it.
type SpamCase struct {
	Filter string // the filter that caught it, or ""
	First  bool   // nothing in the room before it
	Direct bool
	Mine   bool // you have written in this room
	// Caught and Total count the room's messages in the window.
	Caught, Total int
}

// Promotes decides whether this message moves its room into Spam, and by which rule.
func (r SpamRules) Promotes(c SpamCase) (SpamRule, bool) {
	if c.Filter == "" {
		return SpamNotSpam, false
	}
	if r.FirstMessage && c.First {
		return SpamFirstMessage, true
	}
	if r.Direct && c.Direct && !c.Mine {
		return SpamDirect, true
	}
	if r.Ratio > 0 && c.Total >= r.Floor && c.share() >= r.Ratio {
		return SpamMostly, true
	}
	return SpamNotSpam, false
}

// share is Caught/Total, zero for an empty window.
func (c SpamCase) share() float64 {
	if c.Total <= 0 {
		return 0
	}
	return float64(c.Caught) / float64(c.Total)
}
