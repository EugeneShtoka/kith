package theme

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/EugeneShtoka/kith/internal/themespec"
)

// Every shipped preset fills every role.
func TestEveryPresetFillsEveryRole(t *testing.T) {
	t.Parallel()

	for _, name := range themespec.Names() {
		p, err := Resolve(name, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for role, c := range map[string]any{
			"accent": p.Accent, "border": p.Border, "text": p.Text, "muted": p.Muted,
			"cursor": p.Cursor, "badge": p.Badge, "badge_alert": p.BadgeAlert,
			"selected_bg": p.SelectedBG, "selected_dim_bg": p.SelectedDimBG,
		} {
			if c == nil {
				t.Errorf("%s: %s is unset", name, role)
			}
		}
	}
}

// An override changes one role and leaves the other eight where the preset put them.
func TestAnOverrideChangesOneRole(t *testing.T) {
	t.Parallel()

	base, err := Resolve("nord", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("nord", map[string]string{"accent": "#ff0000"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Accent != lipgloss.Color("#ff0000") {
		t.Errorf("accent = %v, want the override", got.Accent)
	}
	if got.Text != base.Text || got.Badge != base.Badge {
		t.Error("an override changed roles it did not name")
	}
}

// A misspelled preset or a malformed color is refused rather than falling back.
func TestBadThemeConfigIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := Resolve("gruvbocks", nil); err == nil {
		t.Error("an unknown preset should be refused")
	} else if !strings.Contains(err.Error(), "gruvbox") {
		t.Errorf("the refusal should list what is available: %v", err)
	}
	for _, bad := range []string{"0x00aaff", "red", "#gg0000", "#0000"} {
		if _, err := Resolve("", map[string]string{"accent": bad}); err == nil {
			t.Errorf("%q should not be accepted as a color", bad)
		}
	}
	if _, err := Resolve("", map[string]string{"backdrop": "#000000"}); err == nil {
		t.Error("an unknown color role should be refused")
	}
}

// An empty preset means the default, and empty overrides mean "leave it alone" — which
// is what makes a config naming one color change one color.
func TestEmptyMeansDefault(t *testing.T) {
	t.Parallel()

	got, err := Resolve("", map[string]string{"accent": ""})
	if err != nil {
		t.Fatal(err)
	}
	if got != Default() {
		t.Errorf("empty config = %+v, want the default palette", got)
	}
}
