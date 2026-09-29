package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/config"
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
