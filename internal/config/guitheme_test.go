package config

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
)

// themePaths are keys of [gui.theme] a generated window writes: one of each kind, an account
// as kith names one (a colon and a slash in it) and a person whose ID has dots.
var themePaths = []string{
	"mode", "light.accent", "dark.text", "dark.selection",
	"accounts.whatsapp:main/", "accounts.matrix:0123456789abcdef/", "people.matrix:@a.b:x.org", "people.whatsapp:15550100@s.whatsapp.net",
}

// themeValueOf is a value the key at path takes, "" (unset) among them.
func themeValueOf(rng *rand.Rand, path string) string {
	if rng.IntN(4) == 0 {
		return ""
	}
	if path == "mode" {
		return GUIThemeModes()[rng.IntN(len(GUIThemeModes()))]
	}
	return fmt.Sprintf("#%06x", rng.IntN(1<<24))
}

// genTheme is a [gui.theme] with some of themePaths set.
func genTheme(rng *rand.Rand) GUITheme {
	var l GUITheme
	for _, path := range themePaths {
		if rng.IntN(2) == 0 && !l.Set(path, themeValueOf(rng, path)) {
			panic("no theme key " + path)
		}
	}
	return l
}

// themeText is l as a file writes it, the table [gui.theme] and its tables.
func themeText(l GUITheme) string {
	return encodeDiff(reflect.ValueOf(struct {
		GUI GUI `toml:"gui"`
	}{GUI{Theme: l}}), reflect.ValueOf(struct {
		GUI GUI `toml:"gui"`
	}{}), nil)
}

// Every key of [gui.theme] reads back as it was set, in either file, through a save and a
// load; an unset one is no key, and a path naming none is refused.
func TestGUIThemeKeysRoundTripThroughEitherFile(t *testing.T) {
	t.Parallel()
	for seed := range uint64(100) {
		rng := rand.New(rand.NewPCG(seed, 0x100c)) // #nosec G404 -- reproducible
		l := genTheme(rng)
		var want []string
		for _, path := range themePaths {
			if v, _ := l.Get(path); v != "" {
				want = append(want, path)
			}
		}
		slices.Sort(want)
		if !slices.Equal(l.Keys(), want) {
			t.Fatalf("seed %d: Keys = %v, want %v", seed, l.Keys(), want)
		}
		cfg, err := Decode(Encode(Config{GUI: GUI{Theme: l}}))
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		onDevice := deviceBaseline()
		onDevice.GUI.Theme = l
		dev, err := decodeDevice("the device file", encodeDevice(onDevice))
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		for _, got := range []GUITheme{cfg.GUI.Theme, dev.GUI.Theme} {
			if !reflect.DeepEqual(got, l) {
				t.Fatalf("seed %d: read back %+v, want %+v", seed, got, l)
			}
		}
	}
	var l GUITheme
	for _, path := range []string{"", "moda", "light", "light.", "light.border", "sepia.accent", "accounts.", "people", "accounts"} {
		if l.Set(path, "#000000") {
			t.Errorf("Set(%q) took a path that names no key", path)
		}
		if _, ok := l.Get(path); ok {
			t.Errorf("Get(%q) read a path that names no key", path)
		}
	}
	if !reflect.DeepEqual(l, GUITheme{}) {
		t.Errorf("refused paths changed the theme: %+v", l)
	}
	l.Set("people.x", "#000000")
	l.Set("people.x", "")
	if l.People != nil {
		t.Errorf("the last person unset leaves %v, want no table, as a file without one reads", l.People)
	}
}
