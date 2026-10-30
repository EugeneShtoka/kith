package emoji

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Curated names win, then Unicode names; an unknown name finds none.
func TestByName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"thumbsup":       "👍", // curated, Slack's name
		"+1":             "👍",
		"grinning_face":  "😀", // Unicode's own name
		"face_in_clouds": "😶‍🌫️",
	} {
		if got, ok := ByName(name); !ok || got != want {
			t.Errorf("ByName(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	if got, ok := ByName("partyparrot"); ok {
		t.Errorf("a custom emoji's name found %q", got)
	}
}

func TestCuratedNamesAreEmoji(t *testing.T) {
	t.Parallel()
	for name, e := range Curated {
		if name == "" || e == "" {
			t.Errorf("an empty entry: %q → %q", name, e)
			continue
		}
		if r, _ := utf8.DecodeRuneInString(e); r < 0x80 {
			t.Errorf("%q → %q is not an emoji", name, e)
		}
	}
}

// Slack's table and Unicode's are of one emoji version: Slack names every emoji kith
// offers, and names no single-glyph emoji kith leaves out (one past the version cap).
func TestSlackNamesTheSameEmoji(t *testing.T) {
	t.Parallel()
	const presentation = "️" // SlackName's keys go without it
	offered := map[string]bool{}
	for _, set := range []map[string]string{Standard, Curated} {
		for _, e := range set {
			offered[strings.TrimSuffix(e, presentation)] = true
		}
	}
	for name, e := range Standard {
		if _, ok := SlackName[strings.TrimSuffix(e, presentation)]; !ok {
			t.Errorf("Slack has no name for %s (%s)", e, name)
		}
	}
	for name, e := range SlackNames {
		e = strings.TrimSuffix(e, presentation)
		if utf8.RuneCountInString(e) == 1 && !offered[e] {
			t.Errorf("Slack's %s (%s) is not an emoji kith offers", e, name)
		}
	}
}

// The desktop's copy of the tables (desktop/src/gen/emoji.ts, `make emoji`) holds
// exactly these maps, so its palette offers and names what the TUI's does.
func TestTheDesktopHoldsTheseTables(t *testing.T) {
	t.Parallel()
	text, err := os.ReadFile(filepath.Join("..", "..", "desktop", "src", "gen", "emoji.ts"))
	if err != nil {
		t.Fatal(err)
	}
	tables := map[string]map[string]string{}
	var in map[string]string
	for line := range strings.Lines(string(text)) {
		if rest, ok := strings.CutPrefix(line, "export const "); ok {
			in = map[string]string{}
			tables[strings.SplitN(rest, ":", 2)[0]] = in
			continue
		}
		row := strings.TrimSuffix(strings.TrimSpace(line), ",")
		if in == nil || !strings.HasPrefix(row, "[") {
			continue
		}
		var pair [2]string
		if err := json.Unmarshal([]byte(row), &pair); err != nil {
			t.Fatalf("%q: %v", row, err)
		}
		in[pair[0]] = pair[1]
	}
	for name, want := range map[string]map[string]string{"curated": Curated, "standard": Standard, "sequences": Sequences} {
		if !maps.Equal(tables[name], want) {
			t.Errorf("emoji.ts's %s differs from the Go map (%d entries, want %d): run make emoji", name, len(tables[name]), len(want))
		}
	}
}
