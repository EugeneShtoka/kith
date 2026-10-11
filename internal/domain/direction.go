package domain

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Layout is which side a room's timeline reads from (sender column left or right).
type Layout int

const (
	// LayoutUnset is a room no list names: auto-detection, or left to right.
	LayoutUnset Layout = iota
	// LayoutLTR is the sender column on the left.
	LayoutLTR
	// LayoutRTL is the sender column on the right, the text flushed against it.
	LayoutRTL
)

// Directions are place entries set by hand to one side.
type Directions struct {
	RTL, LTR []string
}

// Of is the side a room is set to: the narrowest matching entry wins; a tie sets nothing.
func (d Directions) Of(room RoomFacts) Layout {
	rtl, ltr := narrowest(d.RTL, room), narrowest(d.LTR, room)
	switch {
	case rtl > ltr:
		return LayoutRTL
	case ltr > rtl:
		return LayoutLTR
	default:
		return LayoutUnset
	}
}

// Lists reports which list holds this exact entry, if either does.
func (d Directions) Lists(entry string) Layout {
	switch {
	case slices.Contains(d.RTL, entry):
		return LayoutRTL
	case slices.Contains(d.LTR, entry):
		return LayoutLTR
	default:
		return LayoutUnset
	}
}

// With sets one entry to a side, or out of both lists for LayoutUnset.
func (d Directions) With(entry string, side Layout) Directions {
	return Directions{
		RTL: withEntry(d.RTL, entry, side == LayoutRTL),
		LTR: withEntry(d.LTR, entry, side == LayoutLTR),
	}
}

// narrowest is the narrowest kind of entry matching room; EntryInvalid when none do.
func narrowest(list []string, room RoomFacts) EntryKind {
	best := EntryInvalid
	for _, entry := range list {
		if kind, ok := room.Match(entry); ok && kind > best {
			best = kind
		}
	}
	return best
}

// GuessLayout votes per message (RTL when most of its non-link letters are); RTL needs
// a majority of messages with letters. LayoutUnset when none has a letter.
func GuessLayout(bodies []string) Layout {
	rtl, voted := 0, 0
	for _, body := range bodies {
		r, l := letterSides(withoutLinks(body))
		if r+l == 0 {
			continue
		}
		voted++
		if r > l {
			rtl++
		}
	}
	switch {
	case voted == 0:
		return LayoutUnset
	case 2*rtl > voted:
		return LayoutRTL
	default:
		return LayoutLTR
	}
}

// bridgeHeader is a bridge's English first line for a forward or an album.
var bridgeHeader = regexp.MustCompile(`^(↷ Forwarded|Forwarded message from [^\n]*|` +
	`Sent an album with \d+ \w+( and \d+ \w+)*:)[ \t]*(\n|$)`)

// WithoutBridgeHeader strips a bridge's header line, which is not the sender's words.
func WithoutBridgeHeader(body string) string {
	return bridgeHeader.ReplaceAllString(body, "")
}

// HasWords reports whether body has a letter outside its links.
func HasWords(body string) bool {
	r, l := letterSides(withoutLinks(body))
	return r+l > 0
}

func withoutLinks(body string) string {
	links := LinkSpans(body)
	if len(links) == 0 {
		return body
	}
	var b strings.Builder
	at := 0
	for _, link := range links {
		b.WriteString(body[at:link.Start])
		at = link.End
	}
	b.WriteString(body[at:])
	return b.String()
}

// rtlScripts are the scripts written right to left.
var rtlScripts = []*unicode.RangeTable{
	unicode.Hebrew, unicode.Arabic, unicode.Syriac, unicode.Thaana, unicode.Nko,
	unicode.Samaritan, unicode.Mandaic, unicode.Adlam,
}

// letterSides counts RTL letters and other letters.
func letterSides(s string) (rtl, ltr int) {
	for _, r := range s {
		switch {
		case !unicode.IsLetter(r):
		case unicode.IsOneOf(rtlScripts, r):
			rtl++
		default:
			ltr++
		}
	}
	return rtl, ltr
}
