package theme

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/EugeneShtoka/kith/internal/themespec"
)

// The TUI's sender colors are themespec's, the ones the desktop's relay hands its
// window, pinned colors avoided alike.
func TestSpreadColorsAreThemespecs(t *testing.T) {
	t.Parallel()

	pinned := []color.Color{lipgloss.Color("#3ad900"), lipgloss.Color("#00aaff")}
	got := New(Default()).SpreadColors(40, pinned)
	want := themespec.SenderColors(40, []string{"#3ad900", "#00aaff"})
	if len(got) != 40 || len(got) != len(want) {
		t.Fatalf("got %d colors, want 40", len(got))
	}
	for i := range want {
		if got[i] != lipgloss.Color(want[i]) {
			t.Errorf("color %d = %v, want %s", i, got[i], want[i])
		}
	}
}

// ParseColor accepts hex and the named colors, nothing that merely looks like one.
func TestParseColor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want string // "" means it must be refused
	}{
		{"#00aaff", "#00aaff"},
		{"  #00aaff  ", "#00aaff"},
		{"#ABCDEF", "#ABCDEF"},
		{"green", "#3ad900"},
		{"GREEN", "#3ad900"},
		{"teal", "#1de9b6"},
		{" Orange ", "#ff9d00"},
		{"", ""},
		{"   ", ""},
		{"#00aaf", ""},   // one short
		{"#00aafff", ""}, // one long
		{"00aaff", ""},   // no hash
		{"#00aagg", ""},  // not hex
		{"chartreuse", ""},
		{"0x00aaff", ""},
	} {
		got, ok := ParseColor(tc.in)
		if tc.want == "" {
			if ok {
				t.Errorf("ParseColor(%q) = %v, want refused", tc.in, got)
			}
			continue
		}
		if !ok {
			t.Errorf("ParseColor(%q) was refused, want %s", tc.in, tc.want)
		} else if got != lipgloss.Color(tc.want) {
			t.Errorf("ParseColor(%q) = %v, want %s", tc.in, got, tc.want)
		}
	}
}

// A PaneRow selection keeps its (dimmer) fill when the pane loses focus.
func TestPaneRowKeepsItsFillWhenUnfocused(t *testing.T) {
	t.Parallel()

	th := New(Default())
	focused := th.PaneRow(true, true)
	unfocused := th.PaneRow(true, false)
	plain := th.PaneRow(false, false)

	if focused.GetBackground() != th.Palette.SelectedBG {
		t.Error("a focused selected row is not filled with SelectedBG")
	}
	if unfocused.GetBackground() != th.Palette.SelectedDimBG {
		t.Error("an unfocused selected row lost its fill; the cursor becomes unfindable")
	}
	if focused.GetBackground() == unfocused.GetBackground() {
		t.Error("both fills are the same, so two selections on screen compete")
	}
	if plain.GetBackground() != unset {
		t.Errorf("an unselected row is filled with %v", plain.GetBackground())
	}
}

// Overlay rows show selection in the foreground, never a fill.
func TestRowNeverFills(t *testing.T) {
	t.Parallel()

	th := New(Default())
	for _, tc := range []struct{ selected, focused bool }{
		{true, true}, {true, false}, {false, true}, {false, false},
	} {
		if home := th.Row(tc.selected, tc.focused).GetBackground(); home != unset {
			t.Errorf("Row(%v, %v) has background %v; overlay rows are foreground-only",
				tc.selected, tc.focused, home)
		}
	}
	if th.Row(true, true).GetForeground() == th.Row(false, false).GetForeground() {
		t.Error("a selected overlay row looks exactly like an unselected one")
	}
}

// The marker carries the row's fill and the cursor color, focused or not.
func TestMarkerSitsOnTheRowsFill(t *testing.T) {
	t.Parallel()

	th := New(Default())
	if got := th.Marker(true, true); got.GetBackground() != th.Palette.SelectedBG {
		t.Error("the marker punches a hole in the focused row's fill")
	}
	if got := th.Marker(true, true); got.GetForeground() != th.Palette.Cursor {
		t.Error("the focused marker is not the cursor color")
	}
	if got := th.Marker(true, false); got.GetForeground() != th.Palette.Muted {
		t.Error("the unfocused marker should recede, not disappear")
	}
	// Unselected, the marker is just the row's style.
	marker, row := th.Marker(false, true), th.PaneRow(false, true)
	if marker.GetForeground() != row.GetForeground() ||
		marker.GetBackground() != row.GetBackground() {
		t.Error("an unselected marker differs from the row it sits in")
	}
}

// A badge is repainted onto the row's fill so it doesn't punch a hole in it.
func TestOnPaneRowRepaintsOntoTheFill(t *testing.T) {
	t.Parallel()

	th := New(Default())
	badge := th.Badge(false)
	if got := th.OnPaneRow(badge, true, true); got.GetBackground() != th.Palette.SelectedBG {
		t.Error("a badge on a focused selected row did not take its fill")
	}
	if got := th.OnPaneRow(badge, true, false); got.GetBackground() != th.Palette.SelectedDimBG {
		t.Error("a badge on an unfocused selected row did not take its fill")
	}
	if got := th.OnPaneRow(badge, false, false); got.GetBackground() != unset {
		t.Error("a badge on an unselected row was given a background")
	}
	if th.OnPaneRow(badge, true, true).GetForeground() != badge.GetForeground() {
		t.Error("repainting the background changed the badge's own color")
	}
}

// A highlight (mention) badge differs from an ordinary unread count.
func TestHighlightBadgeIsItsOwnColor(t *testing.T) {
	t.Parallel()

	th := New(Default())
	if th.Badge(true).GetForeground() == th.Badge(false).GetForeground() {
		t.Error("a highlight badge is the same color as a plain one")
	}
	if th.Badge(true).GetForeground() != th.Palette.BadgeAlert {
		t.Error("the highlight badge is not BadgeAlert")
	}
}

// Focused and unfocused frames differ; it is the only focus cue.
func TestFocusIsVisible(t *testing.T) {
	t.Parallel()

	th := New(Default())
	if th.Pane.GetBorderTopForeground() == th.PaneActive.GetBorderTopForeground() {
		t.Error("the focused and unfocused pane frames are the same color")
	}
	if th.Title.GetForeground() == th.TitleActive.GetForeground() {
		t.Error("the focused and unfocused pane titles are the same color")
	}
	if th.PaneActive.GetBorderTopForeground() != th.Palette.Accent {
		t.Error("the focused frame is not the accent color")
	}
	if th.Palette != Default() {
		t.Error("New did not carry the palette through")
	}
}

// unset is what lipgloss reports for a style with no background.
var unset = lipgloss.NewStyle().GetBackground()
