package tui

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files under testdata")

// checkGolden compares got with testdata/name, or rewrites it under -update.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file; diff it with -update and review", name)
	}
}

// The ? overlay is byte-for-byte what it was: order, grouping, labels and keys.
func TestHelpOverlayGolden(t *testing.T) {
	t.Parallel()

	m := withHelp(t, sized(t, withRooms(t, newModel())))
	checkGolden(t, "help_default.golden", strings.Join(m.helpLines(), "\n")+"\n")
}

// A keymap with problems: reserved keys, clashes, bad names, unbound actions and a
// fallback — the issue messages name each action by its config path.
func TestHelpOverlayIssuesGolden(t *testing.T) {
	t.Parallel()

	keys := config.DefaultKeys()
	keys.Nav.Open = "esc"
	keys.Insert.Send = "esc"
	keys.Timeline.React = "enter"
	keys.Timeline.Reply = "notakey"
	keys.Rooms.Join = unbind
	keys.Sort.Recent = "s"
	keys.Picker.Kick = "a"
	keys.Jump = config.Jumps{{Chord: "g x", Target: "room:!r:x"}, {Chord: "q", Target: "space:Work"}}
	m := withHelp(t, sized(t, withRooms(t, newModel())).WithKeys(keys))
	checkGolden(t, "help_issues.golden", strings.Join(m.helpLines(), "\n")+"\n")
}

// Every row names a real [keys] path, so its keys, default and label all resolve, and
// every configurable binding reaches the keymap through at least one row.
func TestKeyActionsNameRealBindings(t *testing.T) {
	t.Parallel()

	used := map[string]bool{}
	for _, row := range keyActions {
		if _, ok := config.DefaultKeys().Binding(row.name); !ok {
			t.Errorf("row %s names no [keys] binding", row.name)
		}
		if row.label() == "" {
			t.Errorf("row %s has no help label", row.name)
		}
		used[row.name] = true
	}
	for s, labels := range helpLabelOverrides {
		for name := range labels {
			if !slices.ContainsFunc(keyActions, func(r keyAction) bool { return r.scope == s && r.name == name }) {
				t.Errorf("help label override for %s matches no row", name)
			}
		}
	}
	for _, path := range keyPaths(reflect.TypeFor[config.Keys](), "") {
		if !used[path] {
			t.Errorf("[keys] %s is configurable but no keymap row reads it", path)
		}
	}
}

// keyPaths is every binding path in Keys ("timeline.reply"), from its toml tags.
func keyPaths(t reflect.Type, prefix string) []string {
	var out []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		switch f.Type.Kind() {
		case reflect.String:
			out = append(out, prefix+name)
		case reflect.Struct:
			out = append(out, keyPaths(f.Type, prefix+name+".")...)
		}
	}
	return out
}
