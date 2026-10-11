package config

// The enumerated values config settings accept.

// Media display modes ([display.media] mode).
const (
	// MediaPlaceholder draws a text chip only; the view key opens the real image.
	MediaPlaceholder = "placeholder"
	// MediaInline draws the picture in the timeline with block characters.
	MediaInline = "inline"
)

// MediaModes is every accepted mode, in the order an error should list them.
func MediaModes() []string { return []string{MediaPlaceholder, MediaInline} }

// Media detail levels ([display.media] detail).
const (
	// MediaSextant packs six subcells into a character; the default.
	MediaSextant = "sextant"
	// MediaHalf uses upper/lower half-blocks, which every terminal has.
	MediaHalf = "half"
)

// MediaDetails is every accepted detail level, most detailed first.
func MediaDetails() []string { return []string{MediaSextant, MediaHalf} }

// Emoji tiers ([display.emoji] set).
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

// Unread sources ([display] unread): what a badge counts.
const (
	// UnreadMessages counts cached messages after the read receipt; the default.
	UnreadMessages = "messages"
	// UnreadNotifications counts what the homeserver's push rules decided.
	UnreadNotifications = "notifications"
)

// UnreadSources is every accepted source.
func UnreadSources() []string { return []string{UnreadMessages, UnreadNotifications} }

// Thread listings ([display.threads] in_room_list).
const (
	ThreadsUnread = "unread"
	ThreadsAll    = "all"
	ThreadsNever  = "never"
)

// ThreadListings is every accepted value.
func ThreadListings() []string { return []string{ThreadsUnread, ThreadsAll, ThreadsNever} }

// SkinToneNone is an explicit no-tone setting; like an empty one, it applies no modifier.
const SkinToneNone = "none"

// skinToneModifier is each tone name and its Unicode modifier, in chart order.
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

// SkinToneModifier is the Unicode modifier for a tone name ("" for "none"), and whether
// the name is known.
func SkinToneModifier(name string) (string, bool) {
	for _, t := range skinToneModifier {
		if t.Name == name {
			return t.Modifier, true
		}
	}
	return "", false
}

// Deleted-message modes ([display.deleted]).
const (
	// DeletedShow draws a "(deleted)" placeholder; the default.
	DeletedShow = "show"
	// DeletedHide takes the row out altogether.
	DeletedHide = "hide"
)

// DeletedModes is every accepted value, in the order an error should list them.
func DeletedModes() []string { return []string{DeletedShow, DeletedHide} }

// Misspelling underline styles ([spell] underline).
const (
	// SpellCurly is a curly underline (SGR 4:3); the default.
	SpellCurly = "curly"
	// SpellLine is a plain underline (SGR 4), for terminals that draw 4:3 badly.
	SpellLine = "line"
	// SpellNoUnderline marks nothing.
	SpellNoUnderline = "none"
)

// SpellUnderlines is every accepted style, in the order an error should list them.
func SpellUnderlines() []string { return []string{SpellCurly, SpellLine, SpellNoUnderline} }

// Completion vocabulary scopes ([complete] scope).
const (
	// CompleteScopeRoom weighs this room's words highest, then its spaces', then all.
	CompleteScopeRoom = "room"
	// CompleteScopeSpace drops the room term, for rooms that share one vocabulary.
	CompleteScopeSpace = "space"
	// CompleteScopeGlobal counts every word once, wherever it was said.
	CompleteScopeGlobal = "global"
)

// CompleteScopes is every accepted width, in the order an error should list them.
func CompleteScopes() []string {
	return []string{CompleteScopeRoom, CompleteScopeSpace, CompleteScopeGlobal}
}

// Completion sources ([complete] sources).
const (
	// CompleteHistory is cached messages: room, spaces, anywhere, weighted in that order
	// and boosted for your own words.
	CompleteHistory = "history"
	// CompleteFrequency is the installed word-frequency list for the typed language (the
	// rare-word check's data).
	CompleteFrequency = "frequency"
)

// CompleteSources is every accepted source, in the order an error should list them.
func CompleteSources() []string { return []string{CompleteHistory, CompleteFrequency} }

// Rare-word underline styles ([spell] rare_underline): for words the dictionary accepts
// but the corpus barely knows.
const (
	// SpellDotted is a dotted underline (SGR 4:4); the default, distinct from a misspelling's.
	SpellDotted = "dotted"
)

// RareUnderlines is every accepted style for a rare word.
func RareUnderlines() []string { return []string{SpellDotted, SpellLine, SpellNoUnderline} }

// Autocorrect modes ([spell] autocorrect); each names the class of word it may rewrite.
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
