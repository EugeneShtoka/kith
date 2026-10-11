package tui

import (
	"strings"
	"unicode"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/richtext"
	"github.com/charmbracelet/x/ansi"
)

// [display] skin_tone applies a Fitzpatrick modifier (👍 → 👍🏻) wherever kith offers
// an emoji: the browser, the `:shortcode:` popup, the reaction palette.

func skinToneModifier(name string) string {
	mod, _ := config.SkinToneModifier(name)
	return mod
}

func skinToneNames() []string { return config.SkinToneNames() }

// toneBases are the palette emoji that accept a modifier (Emoji_Modifier_Base, narrowed
// to what kith offers); the stdlib has no emoji properties. A wrong entry draws the
// modifier as a second glyph (🍎🏻).
var toneBases = buildToneBases()

func buildToneBases() map[string]bool {
	const bases = "☝✊✋✌✍🏃👂👇👈👉👊👋👌👍👎👏👶💃💪🕺🖕🖖🙌🙏🚶🤏🤙🤝🤞🤦🤷🫶"
	out := make(map[string]bool, len([]rune(bases)))
	for _, r := range bases {
		out[string(r)] = true
	}
	return out
}

const modifierRunes = "\U0001F3FB\U0001F3FC\U0001F3FD\U0001F3FE\U0001F3FF"

// presentationSelector is U+FE0F, which a toned sequence omits.
const presentationSelector = "️"

// zeroWidthJoiner joins emoji into one (❤‍🔥, 🤷‍♀); scripts join letters with it too.
const zeroWidthJoiner = '\u200d'

// presented spells text as the terminal draws it. An emoji sequence starting with a
// text-presentation symbol and lacking U+FE0F, toned (☝🏼) or joined (❤‍🔥, Telegram's
// reaction), draws two wide but measures one, shifting every later row a column; adding
// U+FE0F after the symbol makes them agree. A letter before a joiner is a script's and
// is left alone. spans (byte offsets) move with the inserts.
func presented(text string, spans []richtext.Span) (string, []richtext.Span) {
	if !strings.ContainsAny(text, modifierRunes+string(zeroWidthJoiner)) {
		return text, spans
	}
	var b strings.Builder
	var at []int // where each selector went, in text's offsets
	prev := rune(-1)
	for i, r := range text {
		if (strings.ContainsRune(modifierRunes, r) || r == zeroWidthJoiner && unicode.Is(unicode.So, prev)) &&
			prev >= 0 && string(prev) != presentationSelector && ansi.StringWidth(string(prev)) == 1 {
			b.WriteString(presentationSelector)
			at = append(at, i)
		}
		b.WriteRune(r)
		prev = r
	}
	if len(at) == 0 {
		return text, spans
	}
	shift := func(offset int, through bool) int {
		n := 0
		for _, i := range at {
			if i < offset || (through && i == offset) {
				n++
			}
		}
		return offset + n*len(presentationSelector)
	}
	moved := make([]richtext.Span, len(spans))
	for i, s := range spans {
		s.Start, s.End = shift(s.Start, true), shift(s.End, false)
		moved[i] = s
	}
	return b.String(), moved
}

// withoutTone removes any tone modifier but keeps U+FE0F, the spelling the shortcode
// tables are keyed on (❤️). Emoji from history may carry a tone.
func withoutTone(emoji string) string {
	for _, mod := range modifierRunes {
		emoji = strings.ReplaceAll(emoji, string(mod), "")
	}
	return emoji
}

// bareEmoji also strips U+FE0F: the form a toned sequence is built from, and toneBases' key.
func bareEmoji(emoji string) string {
	return strings.ReplaceAll(withoutTone(emoji), presentationSelector, "")
}

// toned applies tone to emoji when it is a modifier base, replacing any tone already
// there (history may return 👍🏻).
func toned(emoji, tone string) string {
	if tone == "" {
		return emoji
	}
	bare := bareEmoji(emoji)
	if !toneBases[bare] {
		return emoji
	}
	return bare + tone
}

func splitTone(emoji string) (base, modifier string, ok bool) {
	for _, mod := range modifierRunes {
		if trimmed, found := strings.CutSuffix(emoji, string(mod)); found && trimmed != "" {
			return trimmed, string(mod), true
		}
	}
	return emoji, "", false
}

// shortcodeFor looks up an emoji's name under both spellings (with and without
// U+FE0F); one alone labels toned emoji "::".
func (m Model) shortcodeFor(emoji string) string {
	if name := m.glyphs.set.nameOf[withoutTone(emoji)]; name != "" {
		return name
	}
	return m.glyphs.set.nameOf[bareEmoji(emoji)+presentationSelector]
}

func (m Model) tone(emoji string) string { return toned(emoji, m.glyphs.skin) }

func (m Model) toneAll(emojis []string) []string { return toneEach(emojis, m.glyphs.skin) }

// toneEach applies a tone to a list, for callers without a model yet.
func toneEach(emojis []string, tone string) []string {
	if tone == "" {
		return emojis
	}
	out := make([]string, len(emojis))
	for i, e := range emojis {
		out[i] = toned(e, tone)
	}
	return out
}
