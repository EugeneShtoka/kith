package main

import (
	"slices"
	"testing"

	"gioui.org/io/key"

	"github.com/EugeneShtoka/kith/internal/config"
)

// Gio's presses as its X11 backend reports them (the keysym with the state applied, a
// letter in upper case), named as the desktop's keyName names the same press.
func TestKeyNameIsTheDesktops(t *testing.T) {
	for _, c := range []struct {
		name key.Name
		mods key.Modifiers
		want string
	}{
		{"K", 0, "k"},
		{"G", key.ModShift, "G"},
		{"?", key.ModShift, "?"},
		{",", 0, ","},
		{":", key.ModShift, ":"},
		{"[", 0, "["},
		{"K", key.ModCtrl, "ctrl+k"},
		{"K", key.ModCtrl | key.ModShift, "ctrl+shift+k"},
		{"O", key.ModAlt, "alt+o"},
		{"!", key.ModCtrl | key.ModShift, "ctrl+shift+1"},
		{key.NameReturn, key.ModCtrl, "ctrl+enter"},
		{key.NameTab, key.ModShift, "shift+tab"},
		{key.NameEscape, 0, "esc"},
		{key.NamePageDown, 0, "pgdown"},
		{"F5", 0, "f5"},
		{key.NameCtrl, key.ModCtrl, ""},
	} {
		if got := keyName(key.Event{Name: c.name, Modifiers: c.mods}); got != c.want {
			t.Errorf("%q with %v is %q, want %q", c.name, c.mods, got, c.want)
		}
	}
}

func TestKeymapHoldsSequencesCountsAndYieldsToTyping(t *testing.T) {
	keys := config.DefaultKeys()
	for path, b := range map[string]string{"jump_to": "ctrl+k,g j", "nav.down": "j", "timeline.copy_text": "yy", "quit": "q"} {
		if err := keys.SetBinding(path, b); err != nil {
			t.Fatal(err)
		}
	}
	k := newKeymap(keys, []action{{"jump_to", true}, {"nav.down", false}, {"timeline.copy_text", false}})
	step := func(name string, typing bool, want ...string) {
		t.Helper()
		if got := k.press(name, typing); !slices.Equal(got, want) {
			t.Fatalf("%q (typing %v) is %q, want %q", name, typing, got, want)
		}
	}
	step("ctrl+k", false, "jump_to")
	step("g", false)
	if k.pending() != "g" {
		t.Fatalf("pending %q, want g", k.pending())
	}
	step("j", false, "jump_to")
	step("y", false)
	step("j", false, "nav.down") // breaks the sequence and is judged alone
	step("1", false)
	step("2", false)
	step("j", false, "nav.down")
	if k.count != 12 {
		t.Fatalf("count %d, want 12", k.count)
	}
	step("q", false) // bound, but to an action the window does not take
	step("j", true)  // types itself
	step("ctrl+k", true, "jump_to")
}

func TestMovedClampsAndCounts(t *testing.T) {
	for _, c := range []struct {
		path               string
		count, at, page, n int
		want               int
	}{
		{"nav.down", 0, 3, 10, 5, 4},
		{"nav.down", 3, 3, 10, 5, 4},
		{"nav.up", 2, 3, 10, 5, 1},
		{"nav.half_page_up", 0, 9, 10, 20, 4},
		{"nav.select_oldest", 0, 9, 10, 20, 0},
		{"nav.select_newest", 12, 3, 10, 20, 11},
		{"nav.select_newest", 0, 3, 10, 20, 19},
	} {
		if got, ok := moved(c.path, c.count, c.at, c.page, c.n); !ok || got != c.want {
			t.Errorf("%s ×%d from %d of %d is %d, want %d", c.path, c.count, c.at, c.n, got, c.want)
		}
	}
	if _, ok := moved("timeline.reply", 0, 0, 1, 1); ok {
		t.Error("a path that is no motion moved")
	}
}
