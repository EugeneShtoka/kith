package domain

import "time"

// SearchHit is one message matching a search: enough to list it, and enough to jump to
// it.
type SearchHit struct {
	RoomID  RoomID
	EventID EventID
	// Sender is the MXID; SenderName is the resolved display name when the cache has
	// one, so the results list can name people the way the timeline does.
	Sender     string
	SenderName string
	// Word is the tracked entry this hit matched, for the tracked list and empty for
	// every other kind.
	Word      string
	Timestamp time.Time
	// Snippet is the matching excerpt, elided at both ends when the message is longer,
	// with each matched run wrapped in HighlightStart/HighlightEnd.
	Snippet string
	// FileName is the attachment's name when the message carries one, and empty
	// otherwise.
	FileName string
}

// HighlightStart and HighlightEnd bracket the matched terms inside a SearchHit's
// Snippet, as the search engine tokenized them.
const (
	HighlightStart = "\x01"
	HighlightEnd   = "\x02"
)
