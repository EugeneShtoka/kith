package emoji

import (
	"testing"
	"unicode/utf8"
)

// A name finds its emoji in the curated set first (the names people type, and Slack
// and GitHub use), then among Unicode's names; an unknown name finds none.
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

// Every curated entry is a name for something that is not letters: an emoji.
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
