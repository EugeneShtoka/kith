package domain

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Draft is an outgoing message: the text, who it mentions, and what it replies to.
type Draft struct {
	// Body is the plain text, also the fallback for clients ignoring the HTML body.
	Body string
	// Mentions are the people named in Body.
	Mentions []Mention
	// Plain sends the body as typed, with no Markdown rendering.
	Plain bool
	// Emote sends an m.emote (`/me`) rather than m.text.
	Emote bool
	// Edits is the message this replaces (m.replace).
	Edits EventID
	// ReplyTo is the event being replied to, empty for a new message.
	ReplyTo EventID
	// TxnID makes a repeated send deliver once.
	TxnID string
	// ThreadRoot is the thread sent into, empty for the main timeline.
	ThreadRoot EventID
}

// LiveMentions are the draft's mentions whose text still appears in the body.
func (d Draft) LiveMentions() []Mention {
	if len(d.Mentions) == 0 {
		return nil
	}
	live := make([]Mention, 0, len(d.Mentions))
	seen := make(map[string]bool, len(d.Mentions))
	for _, mention := range d.Mentions {
		if mention.UserID == "" || mention.Name == "" || seen[mention.UserID] {
			continue
		}
		if strings.Contains(d.Body, mention.Name) {
			seen[mention.UserID] = true
			live = append(live, mention)
		}
	}
	return live
}

// NewTxnID is a fresh transaction ID for one send.
func NewTxnID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	return "kith-" + hex.EncodeToString(raw[:])
}
