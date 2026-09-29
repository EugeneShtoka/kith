package tui

import (
	"image/color"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// Emphasis on a row that is about to be bidi-reordered. ANSI cannot go into a line
// before the reorder (escape bytes are direction L and rule L2 scatters them; see
// bidimap.go), so every mark is held as logical byte offsets and resolved against
// the drawn string afterwards, with the style asked for per byte.

// styleKey identifies a style so runs coalesce without comparing escapes; zero is
// plain.
type styleKey uint64

// richStyle is one message body's styling: the sender's formatting, mentions and
// tracked words, all as logical ranges (which is what lets them color RTL lines).
type richStyle struct {
	spans    []richtext.Span
	mentions []mentionRange
	base     ansi.Style
	code     ansi.Color
	link     ansi.Color
	quote    ansi.Color
	// trackedColor is the alert badge's hue.
	trackedColor ansi.Color
	// hidden tints a covered spoiler (fg and bg); revealed opens this message's.
	hidden   ansi.Color
	revealed bool
	// tracked words draw in trackedColor and underlined: color alone is not enough
	// on every terminal.
	tracked []trackedRange
	// hyperlinks wraps link runs in OSC 8.
	hyperlinks bool
}

// trackedRange is a tracked word found in the text — the user's overlay, kept apart
// from richtext.Span, which is the sender's markup.
type trackedRange struct{ start, end int }

type mentionRange struct {
	start, end int
	c          ansi.Color
	// href is the pill's `matrix:` address for OSC 8; empty for none.
	href string
}

// at is the per-byte style styledRow asks for. Key bits: spans' marks in the low
// bits, tracked at bit 20, mention index at 32, link span index at 44 (so distinct
// links never coalesce).
func (r richStyle) at(logical int) (styleKey, ansi.Style, string) {
	key, style, href := styleKey(0), r.base, ""
	for i := range r.mentions {
		mn := &r.mentions[i]
		if logical >= mn.start && logical < mn.end {
			// +1: zero means plain.
			key |= styleKey(i+1) << 32
			// Bold, as the painted (styleMentions) path draws a pill.
			style = style.ForegroundColor(mn.c).Bold()
			if r.hyperlinks {
				href = mn.href
			}
			break
		}
	}
	for i := range r.tracked {
		if logical >= r.tracked[i].start && logical < r.tracked[i].end {
			// Before the spans, so the sender's emphasis still applies on top.
			key |= 1 << 20
			style = style.ForegroundColor(r.trackedColor).Underline(true)
			break
		}
	}
	for i := range r.spans {
		span := &r.spans[i]
		if logical < span.Start || logical >= span.End {
			continue
		}
		key, style = key|markKey(*span), r.styled(style, *span)
		if r.hyperlinks && span.Href != "" {
			// The sender's href outranks the inferred pill one.
			href = span.Href
			key |= styleKey(i+1) << 44
		}
	}
	return key, style, href
}

// markKey is the bits one span's marks contribute.
func markKey(span richtext.Span) styleKey {
	var key styleKey
	for i, on := range []bool{span.Bold, span.Italic, span.Code, span.Strike,
		span.Underline, span.Link, span.Quote, span.Heading, span.Spoiler} {
		if on {
			key |= 1 << i
		}
	}
	return key
}

// styled applies one span's marks. Bold/italic/strike are the terminal's own; code,
// quote and links take a color, since a terminal has no other way to say them.
func (r richStyle) styled(style ansi.Style, span richtext.Span) ansi.Style {
	if span.Bold || span.Heading {
		style = style.Bold()
	}
	if span.Italic {
		style = style.Italic(true)
	}
	if span.Strike {
		style = style.Strikethrough(true)
	}
	if span.Underline {
		style = style.Underline(true)
	}
	if span.Code {
		style = style.ForegroundColor(r.code)
	}
	if span.Quote {
		style = style.ForegroundColor(r.quote)
	}
	if span.Link {
		style = style.ForegroundColor(r.link).Underline(true)
	}
	if span.Spoiler && !r.revealed {
		// Same fg and bg: hides the text while keeping every width, so wrap and
		// reorder are undisturbed.
		style = style.ForegroundColor(r.hidden).BackgroundColor(r.hidden)
	}
	return style
}

// rangesOf finds each mentioned name in text, longest first and once each (the
// pill rules: a prefix name must not claim a longer one's text).
func rangesOf(text string, mentions []mentionSpan) []mentionRange {
	if len(mentions) == 0 {
		return nil
	}
	out := make([]mentionRange, 0, len(mentions))
	taken := make([]bool, len(text)+1)
	for _, mn := range mentions {
		at := 0
		for {
			i := strings.Index(text[at:], mn.name)
			if i < 0 {
				break
			}
			start := at + i
			if !taken[start] {
				for j := start; j < start+len(mn.name); j++ {
					taken[j] = true
				}
				out = append(out, mentionRange{
					start: start, end: start + len(mn.name), c: mn.c, href: mn.uri,
				})
				break
			}
			at = start + len(mn.name)
		}
	}
	return out
}

// clipRange narrows [start,end) to [from,to) and rebases it onto from.
func clipRange(start, end, from, to int) (int, int, bool) {
	s, e := max(start, from), min(end, to)
	return s - from, e - from, s < e
}

// shiftSpans narrows spans to a wrapped line's window [from, to), rebased; a span
// split by the wrap comes back on both lines.
func shiftSpans(spans []richtext.Span, from, to int) []richtext.Span {
	var out []richtext.Span
	for _, span := range spans {
		var ok bool
		if span.Start, span.End, ok = clipRange(span.Start, span.End, from, to); ok {
			out = append(out, span)
		}
	}
	return out
}

func shiftMentions(mentions []mentionRange, from, to int) []mentionRange {
	var out []mentionRange
	for _, mn := range mentions {
		if start, end, ok := clipRange(mn.start, mn.end, from, to); ok {
			out = append(out, mentionRange{start: start, end: end, c: mn.c, href: mn.href})
		}
	}
	return out
}

func shiftTracked(ranges []trackedRange, from, to int) []trackedRange {
	var out []trackedRange
	for _, rg := range ranges {
		if start, end, ok := clipRange(rg.start, rg.end, from, to); ok {
			out = append(out, trackedRange{start: start, end: end})
		}
	}
	return out
}

// segmentStarts is where each wrapped line begins in text, searching forward from
// the previous line's end so repeated lines land right. A line not found gives up
// (the message is drawn unstyled, never wrong).
func segmentStarts(text string, segments []string) ([]int, bool) {
	out := make([]int, 0, len(segments))
	cursor := 0
	for _, seg := range segments {
		if seg == "" {
			out = append(out, cursor)
			continue
		}
		i := strings.Index(text[cursor:], seg)
		if i < 0 {
			return nil, false
		}
		out = append(out, cursor+i)
		cursor += i + len(seg)
	}
	return out, true
}

// richStyleFor is a message's styling over the sender's tint.
func (m Model) richStyleFor(msg domain.Message, text string, mentions []mentionSpan, c color.Color) richStyle {
	base := ansi.Style{}
	if m.prefs.display.ColorMessages {
		base = base.ForegroundColor(ansiColor(c))
	}
	return richStyle{
		mentions:     rangesOf(text, mentions),
		base:         base,
		code:         ansiColor(m.theme.Palette.Badge),
		link:         ansiColor(m.theme.Palette.Accent),
		quote:        ansiColor(m.theme.Palette.Muted),
		trackedColor: ansiColor(m.theme.Palette.BadgeAlert),
		tracked:      trackedRanges(m.trackedIn(msg), text),
		hidden:       ansiColor(m.theme.Palette.SelectedDimBG),
		revealed:     m.timeline.revealed[msg.ID],
		hyperlinks:   m.prefs.display.UseHyperlinks(),
	}
}

// trackedRanges finds the tracked words in text.
func trackedRanges(words domain.Tracked, text string) []trackedRange {
	if !words.Any() {
		return nil
	}
	var out []trackedRange
	for _, hit := range words.Find(text) {
		out = append(out, trackedRange{start: hit.Start, end: hit.End})
	}
	return out
}

// linksMentions reports whether any pill would draw an OSC 8 link, which only the
// offset-mapped path can emit.
func (r richStyle) linksMentions() bool {
	if !r.hyperlinks {
		return false
	}
	for i := range r.mentions {
		if r.mentions[i].href != "" {
			return true
		}
	}
	return false
}

// window narrows the style to one wrapped line, rebased.
func (r richStyle) window(spans []richtext.Span, from, to int) richStyle {
	r.spans = shiftSpans(spans, from, to)
	r.mentions = shiftMentions(r.mentions, from, to)
	r.tracked = shiftTracked(r.tracked, from, to)
	return r
}

// ansiColor adapts a palette color to the escape writer's interface.
func ansiColor(c color.Color) ansi.Color { return c }
