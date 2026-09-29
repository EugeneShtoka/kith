package themespec

import (
	"reflect"
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
