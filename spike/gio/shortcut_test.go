package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	room := domain.JumpTarget{Kind: domain.JumpRoom, Name: "matrix:0123456789abcdef/!a:x"}
	u.rooms = []domain.Room{{ID: "matrix:0123456789abcdef/!a:x", Name: "Ops"}, {ID: "matrix:0123456789abcdef/!b:x", Name: "Dev"}}
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
	other := domain.JumpTarget{Kind: domain.JumpRoom, Name: "matrix:0123456789abcdef/!b:x"}
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

// /shortcut binds the open room, and Tab in its prompt the rail's selected space or tag,
// whose jump selects that row; a room alone has nothing to switch to.
func TestAShortcutBindsTheRailsSpaceOrTag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.toml")
	var keys config.Keys
	keys.FillDefaults()
	u := &ui{devicePath: path, keyConf: keys}
	open := domain.Room{ID: "matrix:0123456789abcdef/!a:x", Name: "Ops"}
	u.rooms = []domain.Room{open}
	u.rail.rows = []domain.RailRow{{Key: "tag:Work", Label: "Work"}, {Key: "Friends", Label: "Friends", Space: "!s:x"}}
	for _, c := range []struct {
		sel          int
		title, entry string
	}{{1, "Shortcut for Friends", "space:Friends"}, {0, "Shortcut for Work", "tag:Work"}} {
		u.rail.sel = c.sel
		u.shortcut(open, "")
		if u.prompting.title != "Shortcut for Ops" || u.prompting.tab == nil {
			t.Fatalf("asked %q first, tab %v; want the room's, then the place's", u.prompting.title, u.prompting.tab != nil)
		}
		u.prompting.field.SetText("ctrl+g " + c.entry[:1])
		u.prompting.tab()
		if u.prompting.title != c.title || u.prompting.field.Text() != "ctrl+g "+c.entry[:1] {
			t.Fatalf("tab asked %q with %q, want %q keeping the keys", u.prompting.title, u.prompting.field.Text(), c.title)
		}
		u.prompting.take(u.prompting.field.Text())
		last := u.jumps[len(u.jumps)-1]
		if last.Target != c.entry {
			t.Fatalf("bound %+v (said %q), want %s", last, u.note, c.entry)
		}
		u.rail.sel = 1 - c.sel
		if !u.jump(context.Background(), strconv.Itoa(len(u.jumps)-1)) || u.rail.sel != c.sel {
			t.Errorf("the jump to %s selects row %d", c.entry, u.rail.sel)
		}
	}
	u.rail.sel = 1
	u.bindShortcut(domain.JumpTarget{Kind: domain.JumpHome, Name: "space:Friends"}, "ctrl+g z")
	if n := len(slices.DeleteFunc(slices.Clone(u.jumps), func(j config.Jump) bool { return j.Target != "space:Friends" })); n != 1 {
		t.Errorf("%d jumps to Friends after binding it again, want its earlier one replaced", n)
	}
	u.rail.rows = nil
	u.shortcut(open, "")
	if u.prompting.tab != nil {
		t.Error("tab offered with only the room to bind")
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
