package domain

// EmojiKind is which vocabulary a recorded emoji belongs to.
type EmojiKind string

// The emoji vocabularies.
const (
	// EmojiReaction is emoji sent as m.reaction annotations.
	EmojiReaction EmojiKind = "reaction"
	// EmojiComposed is emoji typed into a message body.
	EmojiComposed EmojiKind = "compose"
)
