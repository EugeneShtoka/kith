package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/emoji"
	"github.com/EugeneShtoka/kith/internal/richtext"
	"github.com/EugeneShtoka/kith/internal/setup"
)

const light = "\U0001F3FB"

// The tone goes only on emoji that take one (else 🍎🏻 draws as two glyphs).
func TestTonedOnlyAppliesToBases(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"👍", "👋", "🙏", "💪", "✌️", "☝️"} {
		got := toned(base, light)
		if got == base {
			t.Errorf("toned(%q) unchanged, want the modifier applied", base)
		}
		if !strings.HasSuffix(got, light) {
			t.Errorf("toned(%q) = %q, want it to end in the modifier", base, got)
		}
		if strings.Contains(got, presentationSelector) {
			t.Errorf("toned(%q) = %q, want no U+FE0F alongside the modifier", base, got)
		}
	}
	for _, other := range []string{"🍎", "🎉", "❤️", "✅", "🌧️"} {
		if got := toned(other, light); got != other {
			t.Errorf("toned(%q) = %q, want it untouched — it takes no modifier", other, got)
		}
	}
	if got := toned("👍", ""); got != "👍" {
		t.Errorf("with no tone configured, toned(👍) = %q", got)
	}
}

// Idempotent: history may return emoji already wearing a tone.
func TestTonedReplacesAnExistingTone(t *testing.T) {
	t.Parallel()

	dark := "\U0001F3FF"
	if got := toned("👍"+dark, light); got != "👍"+light {
		t.Errorf("toned(👍🏿, light) = %q, want the light one", got)
	}
	if got := toned(toned("👍", light), light); got != "👍"+light {
		t.Errorf("applying twice = %q, want one modifier", got)
	}
}

// ":+1:" finds 👍 and inserts 👍🏻, labeled with the untoned shortcode.
func TestTonedEmojiKeepsItsShortcode(t *testing.T) {
	t.Parallel()

	m := newModel()
	m.glyphs.skin = light
	rows := m.emojiRows([]string{"👍", "❤️"})
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].text != "👍"+light {
		t.Errorf("inserted text = %q, want the toned emoji", rows[0].text)
	}
	if !strings.Contains(rows[0].label, ":+1:") {
		t.Errorf("label = %q, want the shortcode of the untoned form", rows[0].label)
	}
	if rows[1].text != "❤️" || !strings.Contains(rows[1].label, ":heart:") {
		t.Errorf("row = %+v, want ❤️ untoned and named", rows[1])
	}
}

// An unknown tone is refused at startup; case and whitespace are forgiven.
func TestSkinToneValidation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, want string }{
		{"", ""},
		{"none", ""},
		{"light", "\U0001F3FB"},
		{"  Medium-Dark  ", "\U0001F3FE"},
		{"DARK", "\U0001F3FF"},
	} {
		name, want := tc.name, tc.want
		got, err := setup.SkinTone(name)
		if err != nil || got != want {
			t.Errorf("SkinTone(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := setup.SkinTone("pale"); err == nil {
		t.Error("SkinTone(pale) should be refused, not read as none")
	}
	if err := setup.Validate(config.Config{Display: config.Display{SkinTone: "pale"}}); err == nil {
		t.Error("Validate should reject a bad tone at startup")
	}
}

// Every grid cell measures two columns, as the terminal draws it; toned
// text-presentation emoji (☝🏻 ✌🏻 ✍🏻) once measured one and wrapped the row.
func TestEveryTonedCellIsTwoColumns(t *testing.T) {
	t.Parallel()

	for _, tone := range []string{"", light, "\U0001F3FF"} {
		for _, base := range []string{"☝️", "✌️", "✍️", "👍", "🙏", "🤝", "❤️", "🎉", "🌧️"} {
			cell := emojiCell(toned(base, tone))
			if got := ansi.StringWidth(cell); got != 2 {
				t.Errorf("tone %+q on %s: cell %q measures %d, want 2", tone, base, cell, got)
			}
		}
	}
}

// Every toned emoji in the browser keeps its name (not "::").
func TestEveryTonedEmojiKeepsItsName(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.glyphs.skin = light
	next, _ := m.openEmojiPalette()
	m = next

	for _, item := range m.picker.items {
		if item.detail == "::" || item.detail == "" {
			t.Errorf("%s has no name", item.label)
		}
	}
}

// Every frame line is exactly the terminal width with each emoji selected in turn
// (the header shows the selection); one column over wraps and duplicates rows.
func TestNoFrameLineOverflowsWithAnyEmojiSelected(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.width, m.height = 190, 50
	m.glyphs.skin = light
	m.focus = paneTimeline
	next, _ := m.openEmojiPalette()
	m = next

	for i, item := range m.picker.items {
		m.picker.cursor = i
		for row, line := range strings.Split(m.frameView(), "\n") {
			if row == m.height-1 {
				continue
			}
			if got := ansi.StringWidth(line); got != m.width {
				t.Fatalf("with %s selected, row %d is %d columns, want %d",
					item.detail, row, got, m.width)
			}
		}
	}
}

// A toned text-presentation emoji in a message (☝🏼, as senders write it) is drawn
// two wide but measured one, which painted every row after it a column off; it is
// spelled with U+FE0F, as the terminal draws it, and formatting moves with it.
func TestTonedEmojiInTextMeasureAsDrawn(t *testing.T) {
	t.Parallel()
	const pointing = "☝\U0001F3FC"
	got, spans := presented("me! "+pointing+" *b*", []richtext.Span{
		{Start: 0, End: 2, Bold: true},    // before it
		{Start: 4, End: 11, Italic: true}, // around it
		{Start: 12, End: 15, Code: true},  // after it
	})
	if want := "me! ☝" + presentationSelector + "\U0001F3FC *b*"; got != want {
		t.Fatalf("presented = %+q, want %+q", got, want)
	}
	if w := ansi.StringWidth("☝" + presentationSelector + "\U0001F3FC"); w != 2 {
		t.Errorf("the spelled emoji measures %d, want 2", w)
	}
	words := []string{"me", "☝" + presentationSelector + "\U0001F3FC", "*b*"}
	for i, s := range spans {
		if got[s.Start:s.End] != words[i] {
			t.Errorf("span %d covers %q, want %q", i, got[s.Start:s.End], words[i])
		}
	}
	for _, unchanged := range []string{"👍\U0001F3FC", "☝" + presentationSelector + "\U0001F3FC", "no emoji", "🏼 alone"} {
		if got, _ := presented(unchanged, nil); got != unchanged {
			t.Errorf("presented(%+q) = %+q, want it as it was", unchanged, got)
		}
	}
}

// Nothing the timeline draws measures other than as drawn: no toned emoji in a body
// or a reply's quote is left measuring one column.
func TestTheTimelineDrawsNoTonedEmojiAColumnShort(t *testing.T) {
	t.Parallel()
	m, _ := attaching(t)
	m = update(t, m, cachedTimelineMsg{roomID: "!a:x", messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@ann:x", Body: "count me in! ☝\U0001F3FC"},
		{ID: "$2", RoomID: "!a:x", Sender: "@cy:x", Body: "in between, so the reply quotes"},
		{ID: "$3", RoomID: "!a:x", Sender: "@bo:x", Body: "same ✌\U0001F3FF", ReplyTo: "$1"},
	}})
	view := m.View().Content
	if !strings.Contains(view, "count me in") {
		t.Fatal("the message is not drawn")
	}
	prev := rune(-1)
	for _, r := range view {
		if strings.ContainsRune(modifierRunes, r) && prev >= 0 && string(prev) != presentationSelector && ansi.StringWidth(string(prev)) == 1 {
			t.Errorf("%+q then a tone is drawn two wide but measures one", prev)
		}
		prev = r
	}
}

// A joined emoji sent without U+FE0F (Telegram's ❤‍🔥 reaction) begins with a
// text-presentation symbol, so it measured one column while the terminal drew two:
// the row overflowed and its border wrapped onto the next. Every joined sequence kith
// knows, stripped of its selectors as a sender may send it, measures two as a reaction
// chip and in a message.
func TestJoinedEmojiWithoutSelectorMeasureAsDrawn(t *testing.T) {
	t.Parallel()
	seen := 0
	for name, seq := range emoji.Sequences {
		if !strings.ContainsRune(seq, zeroWidthJoiner) {
			continue
		}
		bare := strings.ReplaceAll(seq, presentationSelector, "")
		seen++
		if w := ansi.StringWidth(emojiCell(bare)); w != 2 {
			t.Errorf("%s %+q as a reaction measures %d, want 2", name, bare, w)
		}
		if got, _ := presented("a "+bare+" b", nil); ansi.StringWidth(got) != 6 {
			t.Errorf("%s %+q in a message measures %d, want 6", name, bare, ansi.StringWidth(got))
		}
	}
	if seen == 0 {
		t.Fatal("no joined sequences to try")
	}
}

// A joiner after a letter is a script's (Arabic, Devanagari conjuncts), and an emoji
// already spelled with its selector is as drawn: neither is touched.
func TestJoinersOutsideEmojiAreLeftAlone(t *testing.T) {
	t.Parallel()
	for _, unchanged := range []string{
		"\u0644\u200d\u0627",           // Arabic lam, joiner, alef
		"\u0915\u094d\u200d\u0937",     // Devanagari k, virama, joiner, ssa
		"\u2764\ufe0f\u200d\U0001F525", // ❤️‍🔥 as written
		"\U0001F468\u200d\U0001F4BB",   // 👨‍💻, wide from its first rune
	} {
		if got, _ := presented(unchanged, nil); got != unchanged {
			t.Errorf("presented(%+q) = %+q, want it as it was", unchanged, got)
		}
		if got := emojiCell(unchanged); got != unchanged {
			t.Errorf("emojiCell(%+q) = %+q, want it as it was", unchanged, got)
		}
	}
}
