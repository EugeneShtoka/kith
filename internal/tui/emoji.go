package tui

import (
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	emojidata "github.com/EugeneShtoka/kith/internal/emoji"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// emojiState is which emoji this client offers and in what order. A change to set,
// skin, refused or orders is what makes palette stale.
type emojiState struct {
	// set is the working vocabulary ([display.emoji] set plus the user's additions).
	set emojiSet
	// skin is the Fitzpatrick modifier from [display] skin_tone; empty means untoned.
	skin string
	// refused is what bridged networks were seen to reject as reactions, protocol →
	// untoned emoji (refusals.go).
	refused map[string]map[string]bool
	// orders is each room's emoji ranking, fetched once per room per session.
	orders map[domain.RoomID]emojiOrder
	// scope is the palette's frequency scope (room|space|global|static) and static
	// its fallback set. palette is the active 10, computed on room-open and frozen
	// for the visit so key positions hold.
	scope   string
	static  []string
	palette []string
}

// paletteSize is the react palette's slot count, one per react.pick_N binding.
const paletteSize = 10

// defaultReactions is the built-in static palette, used without [display.reactions].
var defaultReactions = []string{"👍", "❤️", "😂", "🎉", "😮", "😢", "🙏", "👀", "🔥", "💯"}

// paletteHint is the one-line legend of the palette with each slot's live pick key
// (by default "1👍 2❤️ … 0💯"); a slot with no key is shown bare.
func (m Model) paletteHint() string {
	var b strings.Builder
	for i, e := range m.glyphs.palette {
		if i > 0 {
			b.WriteByte(' ')
		}
		if i < paletteSize {
			b.WriteString(m.keys.keyHint(scopeReact, actPick1+action(i)))
		}
		b.WriteString(e)
	}
	return b.String()
}

// resolveReaction turns react-prompt input into the reaction key to send: a
// :shortcode: is looked up (case-insensitive), anything else is sent verbatim. An
// unknown shortcode is refused with the nearest names, never sent as literal text.
func (m Model) resolveReaction(s string) (string, bool) {
	s = strings.TrimSpace(s)
	name, isShortcode := shortcodeName(s)
	if !isShortcode {
		return s, true
	}
	if e, ok := m.glyphs.set.byName[name]; ok {
		return e, true
	}
	return m.nearestShortcodes(name), false
}

// shortcodeName is the name inside a :shortcode:, and whether the input was one.
func shortcodeName(s string) (string, bool) {
	if len(s) < 3 || !strings.HasPrefix(s, ":") || !strings.HasSuffix(s, ":") {
		return "", false
	}
	name := strings.ToLower(s[1 : len(s)-1])
	if name == "" || strings.ContainsAny(name, ": \t") {
		return "", false
	}
	return name, true
}

// nearestShortcodes is the refusal message, offering names that start with what was
// typed (a misspelling is usually a truncation: `:dance:` for `:dancer:`).
func (m Model) nearestShortcodes(name string) string {
	var near []string
	for _, candidate := range m.glyphs.set.names {
		if strings.HasPrefix(candidate, name) && candidate != name {
			near = append(near, ":"+candidate+":")
			if len(near) == 3 {
				break
			}
		}
	}
	if len(near) == 0 {
		return "no emoji called :" + name + ":"
	}
	return "no emoji called :" + name + ": — try " + strings.Join(near, " ")
}

// looksLikeEmoji tests shape, not set membership: an emoji has no ASCII letters and
// no colon, which rejects failed shortcodes and text reactions from usage history.
func looksLikeEmoji(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == ':' || (r < utf8.RuneSelf && unicode.IsLetter(r)) {
			return false
		}
	}
	return true
}

// onlyEmoji is looksLikeEmoji over a list, for the quick palette.
func onlyEmoji(list []string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(e string) bool { return !looksLikeEmoji(e) })
}

// emojiSet is the working vocabulary the completion popup and browser offer, in order.
// Tiers: curated (emojidata.Curated), standard (+ every single-glyph Unicode emoji),
// complete (+ ZWJ sequences and flags, which mis-measure without font ligatures).
// Curated names win every collision.
type emojiSet struct {
	// byName maps a shortcode to its emoji; nameOf is each emoji's display name.
	byName map[string]string
	nameOf map[string]string
	// names is every shortcode sorted; all is every distinct emoji by display name.
	names []string
	all   []string
}

// Emoji tiers, as written in the config.
const (
	emojiCurated  = config.EmojiCurated
	emojiStandard = config.EmojiStandard
	emojiComplete = config.EmojiComplete
)

// newEmojiSet builds the vocabulary for a tier plus the user's own additions; an
// unrecognized tier (refused at startup by setup.EmojiTier) means curated.
func newEmojiSet(tier string, extra map[string]string) emojiSet {
	byName := make(map[string]string, len(emojidata.Curated)+len(emojidata.Standard))
	// Widest first: later writes win, so curated and then user names override.
	if tier == emojiComplete {
		add(byName, emojidata.Sequences)
	}
	if tier == emojiComplete || tier == emojiStandard {
		add(byName, emojidata.Standard)
	}
	add(byName, emojidata.Curated)
	add(byName, extra)

	set := emojiSet{byName: byName}
	set.nameOf = buildShortcodeIndex(byName)
	set.names = slices.Sorted(maps.Keys(byName))
	set.all = distinctEmoji(set)
	return set
}

func add(dst, src map[string]string) {
	for name, emoji := range src {
		if name != "" && emoji != "" {
			dst[name] = emoji
		}
	}
}

// buildShortcodeIndex maps each emoji to its display name: the shortest, then
// alphabetically first, so it is stable between runs.
func buildShortcodeIndex(byName map[string]string) map[string]string {
	out := make(map[string]string, len(byName))
	for name, emoji := range byName {
		best, seen := out[emoji]
		if !seen || len(name) < len(best) || (len(name) == len(best) && name < best) {
			out[emoji] = name
		}
	}
	return out
}

// distinctEmoji is every emoji in the set, ordered by its display shortcode.
func distinctEmoji(set emojiSet) []string {
	seen := make(map[string]bool, len(set.byName))
	out := make([]string, 0, len(set.byName))
	for _, name := range set.names {
		emoji := set.byName[name]
		if set.nameOf[emoji] != name || seen[emoji] {
			continue
		}
		seen[emoji] = true
		out = append(out, emoji)
	}
	return out
}

// EmojiCells is every cell the browser would draw for a display configuration, toned
// and presentation-reconciled. Exported for cmd/emoji-probe, which asks the terminal
// how wide each really is.
func EmojiCells(display config.Display) ([]string, error) {
	tone, err := setup.SkinTone(display.SkinTone)
	if err != nil {
		return nil, err
	}
	tier, err := setup.EmojiTier(display.Emoji.Set)
	if err != nil {
		return nil, err
	}
	set := newEmojiSet(tier, display.Emoji.Extra)
	cells := make([]string, 0, len(set.all))
	for _, emoji := range set.all {
		cells = append(cells, emojiCell(toned(emoji, tone)))
	}
	return cells, nil
}
