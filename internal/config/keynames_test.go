package config

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestValidKey(t *testing.T) {
	t.Parallel()

	good := []string{
		"a", "Z", "?", "1", "·", // any single character
		"enter", "esc", "tab", "space", "backspace", "up", "pgdown", "home", "end",
		"f1", "f9", "f20",
		"ctrl+c", "shift+tab", "alt+enter", "ctrl+alt+delete", "super+k", "meta+x",
	}
	for _, key := range good {
		if !ValidKey(key) {
			t.Errorf("ValidKey(%q) = false, want true", key)
		}
	}
	bad := []string{
		"kk", "escape", "pageup", "pgdn", "return", "ctrl", "ctrl+", "f0", "f21",
		"control+c", "cmd+k", "arrowup", "",
	}
	for _, key := range bad {
		if ValidKey(key) {
			t.Errorf("ValidKey(%q) = true, want false", key)
		}
	}
}

func TestSplitKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want []string
	}{
		{"k,up", []string{"k", "up"}},
		{" k , up ", []string{"k", "up"}}, // whitespace around each name is trimmed
		{"k,,up,", []string{"k", "up"}},   // blanks and a trailing comma are dropped
		{"enter", []string{"enter"}},
		{"", nil},
		{",", nil},
		{"shift+tab", []string{"shift+tab"}},
		{"comma,gg", []string{",", "g g"}},
	}
	for _, tc := range tests {
		if got := SplitKeys(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("SplitKeys(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// A three-step chord needs the spaced spelling, and a mistyped key name stays a reported
// mistake.
func TestSequenceShorthandIsNarrow(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"gg":       "g g",
		"esc":      "esc",
		"pgup":     "pgup",
		"escape":   "escape", // left for validation to reject
		"ctrl+w h": "ctrl+w h",
	} {
		if got := NormalizeSequence(in); got != want {
			t.Errorf("NormalizeSequence(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every name the window's keyName is held to is one [keys] takes (testdata/keynames.json).
func TestTheWindowsKeyNamesAreKithsKeyNames(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/keynames.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Cases []struct{ Name string } `json:"cases"`
	}
	if err := json.Unmarshal(data, &table); err != nil || len(table.Cases) == 0 {
		t.Fatalf("keynames.json: %v, %d cases", err, len(table.Cases))
	}
	for _, c := range table.Cases {
		if !ValidKey(c.Name) {
			t.Errorf("%q is not a key name [keys] takes", c.Name)
		}
	}
}
