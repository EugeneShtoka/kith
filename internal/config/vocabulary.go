package config

// The words a configuration file is allowed to use, and the one place they are spelled.

// Media display modes ([display.media] mode).
const (
	// MediaPlaceholder draws a text chip only; the view key opens the real image.
	MediaPlaceholder = "placeholder"
	// MediaInline draws the picture in the timeline with block characters.
	MediaInline = "inline"
)

// MediaModes is every accepted mode, in the order an error should list them.
func MediaModes() []string { return []string{MediaPlaceholder, MediaInline} }

// Media detail levels ([display.media] detail) — how much of a picture one
// character carries.
const (
	// MediaSextant packs six subcells into a character; the default.
	MediaSextant = "sextant"
	// MediaHalf uses upper/lower half-blocks, which every terminal has.
	MediaHalf = "half"
)

// MediaDetails is every accepted detail level, most detailed first.
func MediaDetails() []string { return []string{MediaSextant, MediaHalf} }

// Emoji tiers ([display.emoji] set) — how much of the standard set to offer.
const (
	// EmojiCurated is the hand-picked set; the default.
	EmojiCurated = "curated"
	// EmojiStandard is the commonly-supported subset of Unicode's list.
	EmojiStandard = "standard"
	// EmojiComplete is everything the generated tables carry.
	EmojiComplete = "complete"
)

// EmojiTiers is every accepted tier, smallest first.
func EmojiTiers() []string { return []string{EmojiCurated, EmojiStandard, EmojiComplete} }

// Unread sources ([display] unread) — what a badge counts.
const (
	// UnreadMessages counts cached messages after the read receipt; the default.
	UnreadMessages = "messages"
	// UnreadNotifications counts what the homeserver's push rules decided.
	UnreadNotifications = "notifications"
)

// UnreadSources is every accepted source.
func UnreadSources() []string { return []string{UnreadMessages, UnreadNotifications} }

// Thread listings ([display.threads] in_room_list) — which of a room's threads are
// listed beneath it.
const (
	ThreadsUnread = "unread"
	ThreadsAll    = "all"
	ThreadsNever  = "never"
)

// ThreadListings is every accepted value.
func ThreadListings() []string { return []string{ThreadsUnread, ThreadsAll, ThreadsNever} }

// SkinToneNone is the absence of a tone, which is a spelling a user can write and
// not the same thing as an empty setting — both resolve to no modifier.
const SkinToneNone = "none"

// skinToneModifier is each accepted tone name and the Unicode modifier that applies it,
// in the order the chart names them.
var skinToneModifier = []struct {
	Name     string
	Modifier string
}{
	{SkinToneNone, ""},
	{"light", "\U0001F3FB"},
	{"medium-light", "\U0001F3FC"},
	{"medium", "\U0001F3FD"},
	{"medium-dark", "\U0001F3FE"},
	{"dark", "\U0001F3FF"},
}

// SkinToneNames is the accepted spellings, in chart order.
func SkinToneNames() []string {
	out := make([]string, len(skinToneModifier))
	for i, t := range skinToneModifier {
		out[i] = t.Name
	}
	return out
}

// SkinToneModifier is the Unicode modifier for a tone name, and whether the name is
// one. "none" is a name, and its modifier is the empty string.
func SkinToneModifier(name string) (string, bool) {
	for _, t := range skinToneModifier {
		if t.Name == name {
			return t.Modifier, true
		}
	}
	return "", false
}

// What to do with a message somebody deleted ([display.deleted]).
const (
	// DeletedShow draws the "(deleted)" placeholder where the message was, which is the
	// default: a gap in a conversation is a fact about it.
	DeletedShow = "show"
	// DeletedHide takes the row out altogether.
	DeletedHide = "hide"
)

// DeletedModes is every accepted value, in the order an error should list them.
func DeletedModes() []string { return []string{DeletedShow, DeletedHide} }

// Spelling underline styles ([spell] underline) — how a misspelled word is marked
// in the composer.
const (
	// SpellCurly is a curly underline (SGR 4:3); the default.
	SpellCurly = "curly"
	// SpellLine is a plain underline (SGR 4), for a terminal that draws 4:3 badly
	// rather than not at all.
	SpellLine = "line"
	// SpellNoUnderline marks nothing.
	SpellNoUnderline = "none"
)

// SpellUnderlines is every accepted style, in the order an error should list them.
func SpellUnderlines() []string { return []string{SpellCurly, SpellLine, SpellNoUnderline} }

// How wide the completion vocabulary is ([complete] scope).
const (
	// CompleteScopeRoom weighs this room's words highest, then its spaces', then
	// everywhere.
	CompleteScopeRoom = "room"
	// CompleteScopeSpace drops the room term: useful where a set of rooms share one
	// vocabulary and no single one of them owns it.
	CompleteScopeSpace = "space"
	// CompleteScopeGlobal counts every word once, wherever it was said.
	CompleteScopeGlobal = "global"
)

// CompleteScopes is every accepted width, in the order an error should list them.
func CompleteScopes() []string {
	return []string{CompleteScopeRoom, CompleteScopeSpace, CompleteScopeGlobal}
}

// Where completions come from ([complete] sources).
const (
	// CompleteHistory is the cache: what has been said in this room, its spaces, and
	// anywhere, weighted in that order and again for the words you wrote yourself.
	CompleteHistory = "history"
	// CompleteFrequency is the installed word-frequency list for the language being
	// typed in — the same data the rare-word check reads.
	CompleteFrequency = "frequency"
)

// CompleteSources is every accepted source, in the order an error should list them.
func CompleteSources() []string { return []string{CompleteHistory, CompleteFrequency} }

// Rare-word underline styles ([spell] rare_underline) — how a word the dictionary
// accepted but the corpus barely knows is marked.
const (
	// SpellDotted is a dotted underline (SGR 4:4); the default, and deliberately not
	// the curly a misspelling gets.
	SpellDotted = "dotted"
)

// RareUnderlines is every accepted style for a rare word.
func RareUnderlines() []string { return []string{SpellDotted, SpellLine, SpellNoUnderline} }

// What autocorrect may rewrite ([spell] autocorrect) — the values name the class of
// word rather than a degree of aggression, because the levels are not ordered by how
// careful they are: each names what it is allowed to touch.
const (
	// AutocorrectOff rewrites nothing.
	AutocorrectOff = "off"
	// AutocorrectMisspellings rewrites words the dictionary rejects, when the fix is
	// not a guess.
	AutocorrectMisspellings = "misspellings"
	// AutocorrectRare rewrites only words the dictionary *accepts* whose typed form the
	// corpus has never seen and which have exactly one commoner neighbor.
	AutocorrectRare = "rare"
	// AutocorrectAll rewrites both.
	AutocorrectAll = "all"
)

// AutocorrectModes is every accepted value, least touched first.
func AutocorrectModes() []string {
	return []string{AutocorrectOff, AutocorrectMisspellings, AutocorrectRare, AutocorrectAll}
}
