package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// /shortcut writes the room's jump to the device file and binds it now, replacing the
// room's earlier one; a sequence a binding holds, starts or is started by is refused;
// none unbinds the room.
func TestAShortcutIsWrittenOrRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.toml")
	var keys config.Keys
	keys.FillDefaults()
	u := &ui{devicePath: path, keyConf: keys}
	room := domain.Room{ID: "matrix:0123456789abcdef/!a:x", Name: "Ops"}
	u.bindShortcut(room, "ctrl+g o")
	u.bindShortcut(room, "ctrl+g p")
	if u.note != "ctrl+g p now goes to Ops" || len(u.jumps) != 1 || u.jumps[0].Chord != "ctrl+g p" {
		t.Fatalf("said %q, jumps %+v", u.note, u.jumps)
	}
	if got := u.keymap.press("ctrl+g", false); got != nil {
		t.Errorf("ctrl+g alone: %q", got)
	}
	if got := u.keymap.press("p", false); len(got) != 1 || got[0] != "jump.0" {
		t.Errorf("ctrl+g p: %q, want the jump", got)
	}
	written, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(written), `"ctrl+g p"`) || strings.Contains(string(written), "ctrl+g o") {
		t.Errorf("device file %q (%v)", written, err)
	}
	other := domain.Room{ID: "matrix:0123456789abcdef/!b:x", Name: "Dev"}
	nav, _ := keys.Binding("nav.down")
	down := config.SplitKeys(nav)[0]
	for _, tc := range []struct{ keys, why string }{
		{"ctrl+g p", "already bound to a jump"},
		{"ctrl+g", "the start of a longer binding"},
		{"ctrl+g p x", "ctrl+g p already means a jump"},
		{down, "already bound to"},
		{"notakey", "is not a key name"},
	} {
		u.bindShortcut(other, tc.keys)
		if !strings.Contains(u.note, tc.why) {
			t.Errorf("%q: said %q, want %q", tc.keys, u.note, tc.why)
		}
	}
	u.bindShortcut(room, "")
	if u.note != "Unbound Ops" || len(u.jumps) != 0 {
		t.Errorf("said %q, jumps %+v", u.note, u.jumps)
	}
}

// The form asks a poll once it has a question and two answers; a blank answer is a row
// not used.
func TestThePollFormAsksOnceItIsAPoll(t *testing.T) {
	if _, _, ok := asked("Lunch?", []string{"pizza", " "}); ok {
		t.Error("one answer asked")
	}
	if _, _, ok := asked(" ", []string{"a", "b"}); ok {
		t.Error("no question asked")
	}
	q, options, ok := asked(" Lunch? ", []string{"pizza", "", " soup "})
	if !ok || q != "Lunch?" || strings.Join(options, ",") != "pizza,soup" {
		t.Errorf("%q %q %v", q, options, ok)
	}
}
