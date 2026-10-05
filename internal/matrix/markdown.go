package matrix

import (
	"slices"
	"sort"
	"strings"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/markdown"
)

// Outgoing Markdown: bridges translate formatted_body, so without HTML a message
// arrives with its asterisks intact. mautrix's renderer is used (what its bridges
// use). HTML in the source is escaped, never passed through.

// renderBody renders a typed body to HTML, "" when it has no formatting and no
// mentions. Mentions are written into the Markdown source as links before a single
// render; substituting into the HTML afterwards could rewrite inside href attributes.
func renderBody(body string, mentions []domain.Mention) string {
	content := format.RenderMarkdownCustom(withMentionLinks(body, mentions), markdown.Renderer)
	if content.Format != event.FormatHTML {
		// Plain text: mautrix round-trips its HTML to decide this.
		return ""
	}
	return content.FormattedBody
}

// withMentionLinks rewrites each mention's first occurrence in body as a Markdown
// link to that person. Same rules as pillHTML (first occurrence, longest name first).
func withMentionLinks(body string, mentions []domain.Mention) string {
	if len(mentions) == 0 {
		return body
	}
	// Segments alternate untouched text and links already written.
	segments := []string{body}
	for _, mention := range longestFirst(mentions) {
		segments = splitOnceLink(segments, mention)
	}
	return strings.Join(segments, "")
}

// longestFirst orders mentions longest name first, so "Dan" cannot claim the "Dan"
// inside "Daniel".
func longestFirst(mentions []domain.Mention) []domain.Mention {
	ordered := slices.Clone(mentions)
	sort.SliceStable(ordered, func(i, j int) bool {
		return len(ordered[i].Name) > len(ordered[j].Name)
	})
	return ordered
}

// splitOnceLink links the first occurrence of mention's name outside existing links.
func splitOnceLink(segments []string, mention domain.Mention) []string {
	for i, segment := range segments {
		if isMentionLink(segment) {
			continue
		}
		at := strings.Index(segment, mention.Name)
		if at < 0 {
			continue
		}
		// MarkdownMentionWithName escapes its own text; don't escape twice.
		link := format.MarkdownMentionWithName(mention.Name, id.UserID(mention.UserID))
		if mention.RoomID != "" {
			// A room link: mautrix's helper is typed to users.
			link = roomMentionLink(mention.Name, mention.RoomID)
		}
		out := make([]string, 0, len(segments)+2)
		out = append(out, segments[:i]...)
		out = append(out, segment[:at], link, segment[at+len(mention.Name):])
		return append(out, segments[i+1:]...)
	}
	return segments
}

// roomMentionLink is a Markdown link to a room by ID (portals often lack an alias).
func roomMentionLink(name, roomID string) string {
	// The ID is not percent-encoded, matching the plain path's anchors.
	return "[" + escapeLinkText(name) + "](https://matrix.to/#/" + roomID + ")"
}

// escapeLinkText escapes the characters that would end a Markdown link's text early.
func escapeLinkText(name string) string {
	return strings.NewReplacer("[", "\\[", "]", "\\]").Replace(name)
}

// isMentionLink reports whether a segment is a link splitOnceLink wrote.
func isMentionLink(segment string) bool {
	return strings.HasPrefix(segment, "[") && strings.Contains(segment, "](https://matrix.to/#/")
}
