package domain

import "time"

// SearchHit is one message matching a search: enough to list it and jump to it.
type SearchHit struct {
	RoomID  RoomID
	EventID EventID
	// Sender is the MXID; SenderName is the cached display name, if any.
	Sender     string
	SenderName string
	// Word is the tracked entry this hit matched; empty for other searches.
	Word      string
	Timestamp time.Time
	// Snippet is the matching excerpt, elided at both ends when the message is longer,
	// with each matched run wrapped in HighlightStart/HighlightEnd.
	Snippet string
	// FileName is the attachment's name, if any.
	FileName string
	// Mentions carry each person's current cached name (Mention.Known), so a snippet
	// names them as the timeline would.
	Mentions []Mention
}

// HighlightStart and HighlightEnd bracket matched terms in a SearchHit's Snippet.
const (
	HighlightStart = "\x01"
	HighlightEnd   = "\x02"
)

// SaidRequest is one page of what someone said, across rooms: newest first from Before,
// by any of Senders. Hidden people are not left out; the caller asked for them.
type SaidRequest struct {
	// Senders is every ID the person goes by (PersonIDs); none is nobody.
	Senders []string
	Rooms   RoomSet
	// Before is the oldest message already shown; the page is what came before it. Zero
	// starts at the newest.
	Before MessagePlace
	Limit  int
}

// MessagePlace is where a message stands in time across rooms: what MessagesFrom pages by.
type MessagePlace struct {
	RoomID    RoomID
	ID        EventID
	Timestamp time.Time
	Seq       int64
}

// Place is where msg stands.
func (msg Message) Place() MessagePlace {
	return MessagePlace{RoomID: msg.RoomID, ID: msg.ID, Timestamp: msg.Timestamp, Seq: msg.Seq}
}
