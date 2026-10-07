package domain

import (
	"cmp"
	"sort"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/richtext"
)

// EventID identifies a single Matrix event (e.g. "$abc:example.org").
type EventID string

// Message is a single timeline message event, reduced to what the client renders.
type Message struct {
	ID         EventID
	RoomID     RoomID
	Sender     string // the sender's Matrix ID (@user:server)
	SenderName string // the sender's room display name, if resolved; may be empty
	// Body is the resolved plain text: an edit's replacement, empty when redacted.
	Body string
	// Format is the formatting drawn over Body (see richtext.Formatted), usually none.
	Format richtext.Formatted
	// Timestamp is the origin time; the timeline orders by it, ties broken by ID.
	Timestamp      time.Time
	Redacted       bool
	RedactedBy     string
	RedactedReason string
	RedactedAt     time.Time
	Edited         bool // content replaced by an m.replace edit
	// EditedAt is when the edit now supplying Body was sent: edits arrive in any
	// order, and only a newer one may replace them. Zero means unknown (older than any).
	EditedAt time.Time
	// RevisionID is the event this message arrived as, when not its own ID (an edit
	// carries its target's ID); on a message an edit was folded onto, the edit it
	// shows, which breaks a tie between edits sent in the same millisecond.
	RevisionID EventID
	// Reverted marks a copy sent because the edit the message showed was deleted: it
	// is what the message shows now (an older version, or nothing yet), and replaces
	// the body and the edit whatever their times say.
	Reverted   bool
	ReplyTo    EventID // m.in_reply_to
	ThreadRoot EventID // the m.thread root, if in a thread
	// Mentioned marks a message mentioning the logged-in user (m.mentions or a pill).
	Mentioned bool
	Emote     bool // m.emote (`/me`)
	// Placeholder marks a bridge's stand-in for a message it could not read yet (a
	// WhatsApp message it failed to decrypt), which it replaces with the message by an
	// edit when it can: not news itself, while the edit that replaces it is.
	Placeholder bool
	Media       *Media // attachment metadata, or nil
	Poll        *Poll  // the poll the message asks, or nil
	// Mentions are the formatted-body pills, so the timeline can color those names.
	Mentions []Mention
}

// Mention is one pill within a message and the text that names it.
type Mention struct {
	// UserID is the person named, and RoomID the room named. Exactly one is set.
	UserID string
	RoomID string
	Name   string
}

// Target is what this mention links to: an MXID or a room ID.
func (m Mention) Target() string {
	if m.RoomID != "" {
		return m.RoomID
	}
	return m.UserID
}

// URI is the `matrix:` address this mention points at, or "" when it points at nothing
// nameable.
func (m Mention) URI() string {
	switch target := m.Target(); {
	case strings.HasPrefix(target, "@"):
		return "matrix:u/" + target[1:]
	case strings.HasPrefix(target, "!"):
		return "matrix:roomid/" + target[1:]
	case strings.HasPrefix(target, "#"):
		return "matrix:r/" + target[1:]
	default:
		return ""
	}
}

// Notifies reports whether this mention belongs in `m.mentions` — the field that makes
// somebody's client light up.
func (m Mention) Notifies() bool { return m.UserID != "" }

// IsUpdate reports whether this stream item revises an already-delivered message (an
// edit or a redaction marker) rather than being a new one.
func (m Message) IsUpdate() bool { return m.Edited || m.Redacted }

// Summary is the message in one line of plain text.
func (m Message) Summary() string {
	if m.Media == nil {
		return m.Body
	}
	if c := m.Caption(); c != "" {
		return c
	}
	if m.Media.Name != "" {
		return "[" + string(m.Media.Type) + ": " + m.Media.Name + "]"
	}
	return "[" + string(m.Media.Type) + "]"
}

// spoilerPlaceholder stands in for spoiler text wherever a message is not drawn.
const spoilerPlaceholder = "++spoiler++"

// NotifyBody is what a notification is allowed to say about a message.
func (m Message) NotifyBody() string {
	if m.Format.IsZero() {
		return m.Summary()
	}
	covered, any := coverSpoilers(m.Format.Text(), m.Format.Spans())
	if !any {
		return m.Summary()
	}
	return covered
}

// coverSpoilers replaces every covered run with the placeholder, and reports whether
// there was one at all.
func coverSpoilers(text string, spans []richtext.Span) (string, bool) {
	ranges := make([][2]int, 0, len(spans))
	for _, span := range spans {
		if span.Spoiler && span.Start < span.End && span.End <= len(text) {
			ranges = append(ranges, [2]int{span.Start, span.End})
		}
	}
	if len(ranges) == 0 {
		return text, false
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	var out strings.Builder
	at := 0
	for _, r := range ranges {
		if r[0] < at {
			// Overlapping or nested: whatever it covers is already covered.
			if r[1] > at {
				at = r[1]
			}
			continue
		}
		out.WriteString(text[at:r[0]])
		out.WriteString(spoilerPlaceholder)
		at = r[1]
	}
	out.WriteString(text[at:])
	return out.String(), true
}

// Revision is one version a message had, and when it had it.
type Revision struct {
	ID     EventID
	Body   string
	Format richtext.Formatted
	At     time.Time // the edit's own timestamp
}

// Deletion is the end of a message: who removed it, why they said they did, and when.
type Deletion struct {
	At     time.Time
	By     string
	Reason string
}

// Happened reports whether there is a deletion to show at all.
func (d Deletion) Happened() bool { return d.By != "" || !d.At.IsZero() }

// Caption is the text sent with an attachment, or "" when there is none.
func (m Message) Caption() string {
	if m.Media == nil || m.Body == m.Media.Name {
		return ""
	}
	return m.Body
}

// MergeMessages returns the union of two message slices, de-duplicated by event ID (an
// empty ID never collides) and ordered oldest→newest by timestamp with the event ID as
// a stable tiebreaker.
func MergeMessages(existing, incoming []Message) []Message {
	merged := make([]Message, 0, len(existing)+len(incoming))
	index := make(map[EventID]int, len(existing)+len(incoming))
	for _, group := range [][]Message{existing, incoming} {
		for i := range group {
			m := &group[i]
			if m.ID != "" {
				if idx, ok := index[m.ID]; ok {
					merged[idx] = combine(merged[idx], *m)
					continue
				}
				index[m.ID] = len(merged)
			}
			row := *m
			row.Reverted = false // an instruction to combine, not part of a row
			merged = append(merged, row)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if !merged[i].Timestamp.Equal(merged[j].Timestamp) {
			return merged[i].Timestamp.Before(merged[j].Timestamp)
		}
		return merged[i].ID < merged[j].ID
	})
	return merged
}

// combine folds a duplicate copy b onto the first-seen copy a, keeping the richest
// information from each: Redacted/Edited are sticky, missing fields are filled, the
// earliest non-zero Timestamp wins (an edit never moves the row), and an edit's Body
// wins while a plain duplicate never reverts an edit or un-redacts.
func combine(a, b Message) Message {
	if b.Redacted {
		// A redaction says what the body is now, even when that is empty.
		a.Body, a.Format = b.Body, b.Format
		if b.RedactedBy != "" {
			a.RedactedBy, a.RedactedReason = b.RedactedBy, b.RedactedReason
		}
		if !b.RedactedAt.IsZero() {
			a.RedactedAt = b.RedactedAt
		}
	}
	a.Redacted = a.Redacted || b.Redacted
	a.Edited = a.Edited || b.Edited
	a.Mentioned = a.Mentioned || b.Mentioned
	a.SenderName = cmp.Or(a.SenderName, b.SenderName)
	switch {
	case a.Timestamp.IsZero():
		a.Timestamp = b.Timestamp
	case !b.Timestamp.IsZero() && b.Timestamp.Before(a.Timestamp):
		a.Timestamp = b.Timestamp
	}
	a.Sender = cmp.Or(a.Sender, b.Sender)
	a.ReplyTo = cmp.Or(a.ReplyTo, b.ReplyTo)
	// Never clear a thread root: an edit arrives with an empty relation.
	a.ThreadRoot = cmp.Or(a.ThreadRoot, b.ThreadRoot)
	// A missing attachment is filled; a poll's later copy says how the votes stand now.
	a.Media, a.Poll = cmp.Or(a.Media, b.Media), cmp.Or(b.Poll, a.Poll)
	if a.Mentions == nil {
		a.Mentions = b.Mentions
	}
	switch {
	case a.Redacted:
		// A redacted message never regains a body: not from an older copy, and not
		// from an edit, which servers leave unredacted.
	case b.Reverted:
		// The edit shown was deleted: back to what the cache holds instead.
		a.Body, a.Format, a.Edited = b.Body, b.Format, b.Edited
		a.EditedAt, a.RevisionID = b.EditedAt, b.RevisionID
	case b.Edited && (!a.Edited || SupersedesEdit(b, a.EditTime(), a.RevisionID)):
		// The edit's formatting replaces the old, and plain replaces formatted: the
		// timeline draws Format over Body, so a kept Format would show the old words.
		a.Body, a.Format = b.Body, b.Format
		a.EditedAt, a.RevisionID = b.EditTime(), b.RevisionID
		if b.Media != nil { // an edit may bring the attachment (a bridge's placeholder replaced)
			a.Media = b.Media
		}
		a.Placeholder = false // replaced: the message it stood in for
	case a.Body == "" && b.Body != "":
		// Its formatting comes with it: Format is drawn over Body.
		a.Body = b.Body
		if a.Format.IsZero() {
			a.Format = b.Format
		}
	}
	if a.Redacted {
		// A deleted message says it was edited, not when or by which edit (as the
		// cache has it).
		a.EditedAt, a.RevisionID = time.Time{}, ""
	}
	return a
}

// EditTime is when the edit m shows was sent, zero when unknown. An edit event
// carries it as its Timestamp; a folded message keeps its own Timestamp and records
// the edit's in EditedAt.
func (m Message) EditTime() time.Time {
	if !m.EditedAt.IsZero() {
		return m.EditedAt
	}
	if m.RevisionID != "" && m.RevisionID != m.ID {
		return m.Timestamp
	}
	return time.Time{}
}

// SupersedesEdit reports whether edit replaces the edit sent at shownAt as shownRev:
// it was sent later, and the event ID breaks a tie, so every delivery order agrees.
// An unknown shown edit (zero time) is superseded by any other.
func SupersedesEdit(edit Message, shownAt time.Time, shownRev EventID) bool {
	at := edit.EditTime()
	if !at.Equal(shownAt) {
		return at.After(shownAt)
	}
	return edit.RevisionID >= shownRev
}

// TimelinePage is one page of room scrollback, ordered oldest→newest so it can be
// rendered top-to-bottom without reordering.
type TimelinePage struct {
	Messages []Message
	// Reactions are the m.reaction events found in this page.
	Reactions []Reaction
	Next      string
}
