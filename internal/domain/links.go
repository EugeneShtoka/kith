package domain

import (
	"regexp"
	"strings"
)

// linkPattern finds web links in a message body.
var linkPattern = regexp.MustCompile(`https?://[^\s<>"'|` + "`" + `]+`)

// Links returns the web links in body, in the order they appear, without duplicates.
func Links(body string) []string {
	found := linkPattern.FindAllString(body, -1)
	if len(found) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(found))
	out := make([]string, 0, len(found))
	for _, raw := range found {
		link := trimLinkTail(raw)
		if link == "" || seen[link] {
			continue
		}
		seen[link] = true
		out = append(out, link)
	}
	return out
}

// trimLinkTail removes punctuation that ends the sentence rather than the URL — "see
// https://example.org." is a link and a full stop, not a link ending in a dot.
func trimLinkTail(link string) string {
	for len(link) > 0 {
		last := link[len(link)-1]
		switch last {
		case '.', ',', ';', ':', '!', '?', '\'', '"':
			link = link[:len(link)-1]
		case ')', ']', '}':
			if balanced(link, opener(last), last) {
				return link
			}
			link = link[:len(link)-1]
		default:
			return link
		}
	}
	return link
}

// opener is the bracket that matches a closing one.
func opener(closing byte) byte {
	switch closing {
	case ')':
		return '('
	case ']':
		return '['
	case '}':
		return '{'
	default:
		return 0
	}
}

// balanced reports whether s has at least as many openers as closers, meaning the
// trailing closer belongs to the link rather than to the prose around it.
func balanced(s string, open, closing byte) bool {
	return strings.Count(s, string(open)) >= strings.Count(s, string(closing))
}

// OpenableLink reports whether a link is safe to hand to the desktop's URL handler.
func OpenableLink(link string) bool {
	lower := strings.ToLower(link)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// LinkSpan is one web link and where it sits in the body, in byte offsets.
type LinkSpan struct {
	Start, End int
	URL        string
}

// LinkSpans returns the web links in body with their positions, in the order they
// appear — duplicates included, since each occurrence is its own run on screen.
func LinkSpans(body string) []LinkSpan {
	found := linkPattern.FindAllStringIndex(body, -1)
	if len(found) == 0 {
		return nil
	}
	out := make([]LinkSpan, 0, len(found))
	for _, at := range found {
		raw := body[at[0]:at[1]]
		link := trimLinkTail(raw)
		if link == "" {
			continue
		}
		// The trim only ever removes from the end, so the span shortens with it.
		out = append(out, LinkSpan{Start: at[0], End: at[0] + len(link), URL: link})
	}
	return out
}

// SafeHyperlink reports whether a link may be handed to the terminal as an OSC 8
// target.
func SafeHyperlink(link string) bool {
	if link == "" {
		return false
	}
	for _, r := range link {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}
