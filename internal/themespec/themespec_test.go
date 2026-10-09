package themespec

import (
	"reflect"
	"slices"
	"testing"
)

// Every preset sets every role.
func TestEveryPresetSetsEveryRole(t *testing.T) {
	t.Parallel()

	for _, name := range Names() {
		c, err := Resolve(name, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		v := reflect.ValueOf(c)
		for i := range v.NumField() {
			if v.Field(i).String() == "" {
				t.Errorf("%s: %s is unset", name, v.Type().Field(i).Name)
			}
		}
	}
}

// A bad preset, role or color is refused, not silently ignored.
func TestBadThemeIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		preset    string
		overrides map[string]string
	}{
		{"solarized", nil},
		{"", map[string]string{"sparkle": "#ffffff"}},
		{"", map[string]string{"accent": "blue"}},
	} {
		if _, err := Resolve(tc.preset, tc.overrides); err == nil {
			t.Errorf("Resolve(%q, %v) accepted it", tc.preset, tc.overrides)
		}
	}
}

// A color a config writes is "#rrggbb" or a known name, in any case and spacing; a
// name reads as its hex; anything else, empty included, is no color. The names are
// listed sorted, each one a color.
func TestAConfigColor(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{"#3AD900", "#3AD900"}, {" Green ", "#3ad900"}, {"magenta", "#967efb"}, {"#00aaff", "#00aaff"},
	} {
		if got, ok := Color(tc.in); !ok || got != tc.want {
			t.Errorf("Color(%q) = %q, %v, want %q", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []string{"", "chartreuse", "#12345", "#1234567", "3ad900", "#gg0000"} {
		if got, ok := Color(in); ok {
			t.Errorf("Color(%q) = %q, want no color", in, got)
		}
	}
	names := ColorNames()
	if !slices.IsSorted(names) || len(names) != len(namedColors) {
		t.Errorf("ColorNames() = %v, want every name, sorted", names)
	}
	for _, name := range names {
		if _, ok := Color(name); !ok {
			t.Errorf("%q is listed but is no color", name)
		}
	}
}
