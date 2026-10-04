package tui

import (
	"strings"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/richtext"
	"github.com/charmbracelet/x/ansi"
)

// Skin tone: [display] skin_tone applies a Fitzpatrick modifier (👍 → 👍🏻) wherever
// kith offers an emoji — the browser, the `:shortcode:` popup, the reaction palette.

// skinToneModifier is the modifier for a configured tone name ("" if unknown).
func skinToneModifier(name string) string {
	mod, _ := config.SkinToneModifier(name)
	return mod
}

// skinToneNames is the accepted spellings, in chart order, for the settings screen.
func skinToneNames() []string { return config.SkinToneNames() }

// toneBases are the palette emoji that accept a modifier (Unicode Emoji_Modifier_Base,
// narrowed to what kith offers). A list because the stdlib has no emoji properties;
// a wrong entry draws the modifier as a second glyph (🍎🏻).
var toneBases = buildToneBases()

func buildToneBases() map[string]bool {
	const bases = "☝✊✋✌✍🏃👂👇👈👉👊👋👌👍👎👏👶💃💪🕺🖕🖖🙌🙏🚶🤏🤙🤝🤞🤦🤷🫶"
	out := make(map[string]bool, len([]rune(bases)))
	for _, r := range bases {
		out[string(r)] = true
	}
	return out
}

// modifierRunes are the five tone modifiers, for recognizing one that is already there.
const modifierRunes = "\U0001F3FB\U0001F3FC\U0001F3FD\U0001F3FE\U0001F3FF"

// presentationSelector is U+FE0F, which a toned sequence omits.
const presentationSelector = "️"

// presented is text as the terminal draws it: a toned text-presentation emoji (☝🏼,
// as senders write it) is drawn two wide but measures one, which paints every row
// after it a column off; U+FE0F between base and tone makes both agree (as emojiCell
// does in the picker). spans, byte offsets into text, move with what is inserted.
func presented(text string, spans []richtext.Span) (string, []richtext.Span) {
	if !strings.ContainsAny(text, modifierRunes) {
		return text, spans
	}
	var b strings.Builder
	var at []int // where each selector went, in text's offsets
	prev := rune(-1)
	for i, r := range text {
		if strings.ContainsRune(modifierRunes, r) && prev >= 0 && string(prev) != presentationSelector &&
			ansi.StringWidth(string(prev)) == 1 {
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

// withoutTone removes any tone modifier but keeps U+FE0F — the spelling the shortcode
// tables are keyed on (❤️). Emoji from history may carry a tone, so lookups start here.
func withoutTone(emoji string) string {
	for _, mod := range modifierRunes {
		emoji = strings.ReplaceAll(emoji, string(mod), "")
	}
	return emoji
}

// bareEmoji strips U+FE0F as well: the form a toned sequence is built from, and
// toneBases' key.
func bareEmoji(emoji string) string {
	return strings.ReplaceAll(withoutTone(emoji), presentationSelector, "")
}

// toned applies tone to emoji when it is a modifier base; idempotent, since the tone
// replaces any already there (history may return 👍🏻).
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

// splitTone divides a toned emoji into its base and modifier, or reports false.
func splitTone(emoji string) (base, modifier string, ok bool) {
	for _, mod := range modifierRunes {
		if trimmed, found := strings.CutSuffix(emoji, string(mod)); found && trimmed != "" {
			return trimmed, string(mod), true
		}
	}
	return emoji, "", false
}

// shortcodeFor is an emoji's display name under either spelling (with or without
// U+FE0F) — looking up only one labeled every toned emoji "::".
func (m Model) shortcodeFor(emoji string) string {
	if name := m.glyphs.set.nameOf[withoutTone(emoji)]; name != "" {
		return name
	}
	return m.glyphs.set.nameOf[bareEmoji(emoji)+presentationSelector]
}

// tone applies the model's configured skin tone.
func (m Model) tone(emoji string) string { return toned(emoji, m.glyphs.skin) }

// toneAll applies the model's tone to a list, for the reaction palette.
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
