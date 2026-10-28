package domain

import (
	"cmp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/richtext"
)

// EventID identifies a single Matrix event (e.g. "$abc:example.org").
type EventID string

// Message is a timeline message event, reduced to what the client renders.
type Message struct {
	ID         EventID
	RoomID     RoomID
	Sender     string // the sender's ID (matrix:@user:server)
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
	// EditedAt is when the edit supplying Body was sent; edits arrive in any order and
	// only a newer one replaces it. Zero means unknown (older than any).
	EditedAt time.Time
	// RevisionID is the event this arrived as when not ID (an edit carries its target's
	// ID); on a folded message, the edit shown. Breaks ties between same-time edits.
	RevisionID EventID
	// Reverted marks a copy sent because the shown edit was deleted; it replaces body
	// and edit regardless of times.
	Reverted   bool
	ReplyTo    EventID // m.in_reply_to
	ThreadRoot EventID // the m.thread root, if in a thread
	// Mentioned marks a message mentioning the logged-in user (m.mentions or a pill).
	Mentioned bool
	Emote     bool // m.emote (`/me`)
	// Placeholder marks a bridge's stand-in for a message it could not decrypt yet; the
	// bridge later replaces it by an edit, which counts as new.
	Placeholder bool
	// Undecrypted marks a stand-in for an encrypted message whose key has not arrived;
	// the decrypted copy, when it comes, replaces it.
	Undecrypted bool
	Media       *Media // attachment metadata, or nil
	Poll        *Poll  // the poll the message asks, or nil
	// Seq orders messages sharing a Timestamp (larger is later); 0 when unknown. Set
	// by networks with whole-second times and unordered IDs (WhatsApp).
	Seq int64
	// Late marks a message that arrived by filling a gap in history after it was sent
	// (off the client wire): notifications fold the old ones into one.
	Late bool
	// Mentions are the formatted-body pills.
	Mentions []Mention
}

// Mention is one pill within a message and the text that names it.
type Mention struct {
	// Exactly one of UserID and RoomID is set.
	UserID string
	RoomID string
	Name   string
	// Known is the person's current member name, filled in on read and never stored.
	Known string
}

// Target is the mention's person or room ID.
func (m Mention) Target() string {
	if m.RoomID != "" {
		return m.RoomID
	}
	return m.UserID
}

// URI is the mention's `matrix:` URI, or "".
func (m Mention) URI() string {
	switch target := MatrixNative(m.Target()); {
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

// Notifies reports whether this mention belongs in `m.mentions`.
func (m Mention) Notifies() bool { return m.UserID != "" }

// IsUpdate reports whether this stream item revises a delivered message (edit or
// redaction).
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

// spoilerPlaceholder replaces spoiler text where a message is not drawn (notifications).
const spoilerPlaceholder = "++spoiler++"

// NotifyBody is the message text for a notification, spoilers covered.
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

// coverSpoilers replaces spoiler spans with the placeholder; false when there are none.
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
			// Overlapping or nested.
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

// Revision is one version of a message.
type Revision struct {
	ID     EventID
	Body   string
	Format richtext.Formatted
	At     time.Time // the edit's own timestamp
}

// Deletion is who removed a message, why, and when.
type Deletion struct {
	At     time.Time
	By     string
	Reason string
}

// Happened reports whether there is a deletion.
func (d Deletion) Happened() bool { return d.By != "" || !d.At.IsZero() }

// Caption is the text sent with an attachment, or "" when there is none.
func (m Message) Caption() string {
	if m.Media == nil || m.Body == m.Media.Name {
		return ""
	}
	return m.Body
}

// MergeMessages unions two slices, deduplicated by non-empty ID, in CompareMessages
// order.
func MergeMessages(existing, incoming []Message) []Message {
	merged := make([]Message, 0, len(existing)+len(incoming))
	index := make(map[EventID]int, len(existing)+len(incoming))
	for _, group := range [][]Message{existing, incoming} {
		for i := range group {
			m := &group[i]
			if m.ID != "" {
				if idx, ok := index[m.ID]; ok {
					merged[idx] = combineStandIn(merged[idx], *m)
					continue
				}
				index[m.ID] = len(merged)
			}
			row := *m
			row.Reverted = false // an instruction to combine, not stored
			merged = append(merged, row)
		}
	}
	slices.SortStableFunc(merged, CompareMessages)
	return merged
}

// CompareMessages is timeline order: time, Seq, then ID shorter first. Telegram IDs
// end in a message number, so shorter-first makes 99 precede 100. The cache queries
// use the same order.
func CompareMessages(a, b Message) int {
	return cmp.Or(a.Timestamp.Compare(b.Timestamp), cmp.Compare(a.Seq, b.Seq),
		cmp.Compare(len(a.ID), len(b.ID)), strings.Compare(string(a.ID), string(b.ID)))
}

// combineStandIn is combine, except that a decrypted message is never folded onto its
// undecrypted stand-in: the stand-in is folded onto it instead, so the message shows.
func combineStandIn(a, b Message) Message {
	if a.Undecrypted && !b.Undecrypted {
		return combine(b, a)
	}
	return combine(a, b)
}

// combine folds duplicate b onto first-seen a: Redacted/Edited are sticky, missing
// fields fill, the earliest Timestamp wins, a newer edit's Body wins, and a plain
// duplicate never reverts an edit or un-redacts.
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
	// A poll's later copy has the current votes.
	a.Media, a.Poll = cmp.Or(a.Media, b.Media), cmp.Or(b.Poll, a.Poll)
	a.Seq = cmp.Or(a.Seq, b.Seq) // first Seq sticks
	if a.Mentions == nil {
		a.Mentions = b.Mentions
	}
	switch {
	case a.Redacted:
		// Never regains a body, not even from an edit (servers leave edits unredacted).
	case b.Reverted:
		// The shown edit was deleted.
		a.Body, a.Format, a.Edited = b.Body, b.Format, b.Edited
		a.EditedAt, a.RevisionID = b.EditedAt, b.RevisionID
	case b.Edited && (!a.Edited || SupersedesEdit(b, a.EditTime(), a.RevisionID)):
		// Format always goes with Body: a stale Format would show the old words.
		a.Body, a.Format = b.Body, b.Format
		a.EditedAt, a.RevisionID = b.EditTime(), b.RevisionID
		if b.Media != nil { // a bridge's edit replacing a placeholder may bring it
			a.Media = b.Media
		}
		a.Placeholder = false
	case a.Body == "" && b.Body != "":
		a.Body = b.Body
		if a.Format.IsZero() {
			a.Format = b.Format
		}
	}
	if a.Redacted {
		// Keep only that it was edited, as the cache does.
		a.EditedAt, a.RevisionID = time.Time{}, ""
	}
	return a
}

// EditTime is when the shown edit was sent, zero when unknown: EditedAt on a folded
// message, Timestamp on an edit event.
func (m Message) EditTime() time.Time {
	if !m.EditedAt.IsZero() {
		return m.EditedAt
	}
	if m.RevisionID != "" && m.RevisionID != m.ID {
		return m.Timestamp
	}
	return time.Time{}
}

// SupersedesEdit reports whether edit replaces the shown edit: later wins, event ID
// breaks ties, so any delivery order agrees. A zero shownAt loses to any edit.
func SupersedesEdit(edit Message, shownAt time.Time, shownRev EventID) bool {
	at := edit.EditTime()
	if !at.Equal(shownAt) {
		return at.After(shownAt)
	}
	return edit.RevisionID >= shownRev
}

// TimelinePage is one page of scrollback, oldest first.
type TimelinePage struct {
	Messages []Message
	// Reactions are the m.reaction events in this page.
	Reactions []Reaction
	Next      string
}
