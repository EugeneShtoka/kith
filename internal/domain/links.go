package domain

import (
	"regexp"
	"slices"
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

// LinksWithHrefs is Links plus the web targets of the message's formatted links, which
// can show text without a scheme (Slack's <https://x/y|x/y>). Body links come first;
// non-web targets are dropped.
func LinksWithHrefs(body string, hrefs []string) []string {
	out := Links(body)
	for _, href := range hrefs {
		if OpenableLink(href) && !slices.Contains(out, href) {
			out = append(out, href)
		}
	}
	return out
}

// trimLinkTail strips trailing sentence punctuation and unbalanced closing brackets.
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

// balanced reports whether s has at least as many openers as closers, so a trailing
// closer belongs to the link.
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

// LinkSpans returns the web links in body with their positions, duplicates included.
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
