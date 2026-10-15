package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// A filter catches messages and moves their room into a tag. What it moved is a
// verdict, kept per room and tag; a person taking the room out releases it, and no
// filter moves a released room back into that tag.

// MoveRule is what moved a room into a tag.
type MoveRule int

const (
	// MoveNone is the zero value: no filter moved the room.
	MoveNone MoveRule = iota
	// MoveAlways: a filter that moves on any catch caught a message.
	MoveAlways
	// MoveFirstMessage: the room's first message was caught.
	MoveFirstMessage
	// MoveDirect: a direct message you never wrote in was caught.
	MoveDirect
	// MoveMostly: most messages over the filter's window were caught.
	MoveMostly
	// MoveSenderFirst: a person's first message anywhere was caught (a mark on them).
	MoveSenderFirst
	// MoveSenderMostly: most of a person's messages over the window, in every room,
	// were caught.
	MoveSenderMostly
	// MoveSenderSpread: a person sent the same caught message in several rooms.
	MoveSenderSpread
)

// Reason is the rule in words, for `/why`.
func (r MoveRule) Reason(filter string) string {
	named := ""
	if filter != "" {
		named = fmt.Sprintf(" (%s)", filter)
	}
	switch r {
	case MoveAlways:
		return "a message in it was caught by a filter" + named
	case MoveFirstMessage:
		return "the first message in it was caught by a filter" + named
	case MoveDirect:
		return "it is a direct message and its message was caught by a filter" + named
	case MoveMostly:
		return "most of what is said in it is caught by a filter" + named
	case MoveSenderFirst:
		return "their first message was caught by a filter" + named
	case MoveSenderMostly:
		return "most of what they write is caught by a filter" + named
	case MoveSenderSpread:
		return "they sent the same caught message in several rooms" + named
	case MoveNone:
		return ""
	}
	return ""
}

// TagVerdict is one room's move into one tag, or its release from it.
type TagVerdict struct {
	Room   RoomID
	Tag    string
	Rule   MoveRule
	Filter string
	At     time.Time
	// Released means a person took the room out; no filter may put it back.
	Released bool
}

// In reports whether this verdict puts the room in its tag; a release never does.
func (v TagVerdict) In() bool { return v.Rule != MoveNone && !v.Released }

// SameTag reports whether two tag names are one tag.
func SameTag(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// ErrReleased refuses a filter's verdict for a room a person released from that tag.
var ErrReleased = errors.New("the room was taken out of that tag")

// Filter catches messages and moves their room into Tag.
type Filter struct {
	Name  string  // reported when it catches something
	Tag   string  // the tag it moves rooms into
	Words Tracked // with Tracked's glob widths
	From  string  // optional sender ID
	// In is the places it watches, in the place vocabulary; empty is everywhere.
	In []string
	// Lang and NotLang are language codes (families and regions expanded): a message is
	// caught when it is in one of Lang, and when it cannot be in any of NotLang. Scripts
	// are writing systems, one of which must be the message's. Empty is any.
	Lang, NotLang, Scripts []string
	Moves                  FilterMoves
	// Ratio, Floor and Window: the caught share, the minimum message count, and the
	// period they are counted over, for the ratio moves.
	Ratio  float64
	Floor  int
	Window time.Duration
	// Spread is how many rooms one caught message must reach, for sender_spread.
	Spread int
}

// FilterMoves is which rules a filter's catch can move a room by.
type FilterMoves struct {
	Always       bool // any catch
	FirstMessage bool // the room's first message
	Direct       bool // a direct message you never wrote in
	Ratio        bool // most of the window
	// The sender moves mark the person who sent the message, not its room.
	SenderFirst  bool // their first message anywhere
	SenderRatio  bool // most of what they wrote in the window, every room together
	SenderSpread bool // the same message in Spread rooms within the window
}

// MovesRooms reports whether a catch can move its room.
func (m FilterMoves) MovesRooms() bool { return m.Always || m.FirstMessage || m.Direct || m.Ratio }

// MarksSenders reports whether a catch can mark its sender.
func (m FilterMoves) MarksSenders() bool { return m.SenderFirst || m.SenderRatio || m.SenderSpread }

// Catches reports whether this filter catches one message in room.
func (f Filter) Catches(body, sender string, room RoomFacts) bool {
	if f.From != "" && !strings.EqualFold(strings.TrimSpace(f.From), sender) {
		return false
	}
	if len(f.In) > 0 && !namesAny(f.In, room) {
		return false
	}
	if f.byLanguage() && !f.speaks(LanguageOf(body)) {
		return false
	}
	if f.anything() {
		return f.From != "" || f.byLanguage()
	}
	return len(f.Words.Find(body)) > 0
}

// byLanguage reports whether the filter judges what language a message is in.
func (f Filter) byLanguage() bool {
	return len(f.Lang) > 0 || len(f.NotLang) > 0 || len(f.Scripts) > 0
}

// speaks reports whether a message in lang passes every language clause.
func (f Filter) speaks(lang Language) bool {
	return (len(f.Lang) == 0 || lang.In(f.Lang)) &&
		(len(f.NotLang) == 0 || lang.Outside(f.NotLang)) &&
		(len(f.Scripts) == 0 || slices.Contains(f.Scripts, lang.Script))
}

// anything reports whether the words match everything (none, or only stars).
func (f Filter) anything() bool {
	for _, word := range f.Words.Words {
		if strings.Trim(strings.TrimSpace(word), "*") != "" {
			return false
		}
	}
	return true
}

// MoveCase is what a filter's moves know about the message it caught.
type MoveCase struct {
	First  bool // nothing in the room before it
	Direct bool
	Mine   bool // you have written in this room
	// Caught and Total count the room's messages in the filter's window.
	Caught, Total int
}

// Moving reports whether a caught message moves its room, and by which rule.
func (f Filter) Moving(c MoveCase) (MoveRule, bool) {
	switch {
	case f.Moves.Always:
		return MoveAlways, true
	case f.Moves.FirstMessage && c.First:
		return MoveFirstMessage, true
	case f.Moves.Direct && c.Direct && !c.Mine:
		return MoveDirect, true
	case f.Moves.Ratio && c.Total >= f.Floor && c.share() >= f.Ratio:
		return MoveMostly, true
	}
	return MoveNone, false
}

// SenderCase is what a filter's sender moves know about the person a caught message
// came from, over every room.
type SenderCase struct {
	First bool // nothing of theirs before it
	// Caught and Total count their messages in the filter's window.
	Caught, Total int
	// Rooms is how many rooms the same caught message reached within the window.
	Rooms int
}

// Marking reports whether a caught message marks its sender, and by which rule.
func (f Filter) Marking(c SenderCase) (MoveRule, bool) {
	switch {
	case f.Moves.SenderFirst && c.First:
		return MoveSenderFirst, true
	case f.Moves.SenderSpread && c.Rooms >= f.Spread:
		return MoveSenderSpread, true
	case f.Moves.SenderRatio && c.Total >= f.Floor && MoveCase{Caught: c.Caught, Total: c.Total}.share() >= f.Ratio:
		return MoveSenderMostly, true
	}
	return MoveNone, false
}

// share is Caught/Total, zero for an empty window.
func (c MoveCase) share() float64 {
	if c.Total <= 0 {
		return 0
	}
	return float64(c.Caught) / float64(c.Total)
}

// Filters is every filter, in config order.
type Filters []Filter

// Catching is every filter that catches a message in room, in config order.
func (fs Filters) Catching(body, sender string, room RoomFacts) []Filter {
	var out []Filter
	for i := range fs {
		if fs[i].Catches(body, sender, room) {
			out = append(out, fs[i])
		}
	}
	return out
}

// MoveCandidate is a room a rescan of cached history would move into a tag, and the
// message that moves it.
type MoveCandidate struct {
	Verdict TagVerdict
	Sample  Message
}

// WithVerdict is a room's verdict set with v applied: a move or a release replaces the
// tag's entry, anything else drops it. A filter's move never overwrites a release.
func WithVerdict(set []TagVerdict, v TagVerdict) ([]TagVerdict, error) {
	at := slices.IndexFunc(set, func(o TagVerdict) bool { return SameTag(o.Tag, v.Tag) })
	if at >= 0 && v.In() && set[at].Released {
		return set, fmt.Errorf("%s in %s: %w", v.Room, v.Tag, ErrReleased)
	}
	out := slices.Clone(set)
	if at >= 0 {
		out = slices.Delete(out, at, at+1)
	}
	if v.In() || v.Released {
		out = append(out, v)
	}
	return out, nil
}

// Retagged is a room's verdict set with tag from's entry moved to tag to, or dropped
// when to is empty; changed reports whether it held one. Moved onto a tag that has an
// entry already (two tags combined), a release wins, else to's own entry stays.
func Retagged(set []TagVerdict, from, to string) (out []TagVerdict, changed bool) {
	at := slices.IndexFunc(set, func(v TagVerdict) bool { return SameTag(v.Tag, from) })
	if at < 0 {
		return set, false
	}
	moving := set[at]
	out = slices.Delete(slices.Clone(set), at, at+1)
	if to == "" {
		return out, true
	}
	moving.Tag = to
	if into := slices.IndexFunc(out, func(v TagVerdict) bool { return SameTag(v.Tag, to) }); into >= 0 {
		if moving.Released && !out[into].Released {
			out[into] = moving
		}
		return out, true
	}
	return append(out, moving), true
}
