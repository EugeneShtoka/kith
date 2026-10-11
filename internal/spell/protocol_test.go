package spell

import (
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		lines []string
		ok    bool
		sug   []string
	}{
		{"plain hit", []string{"*"}, true, nil},
		{"affix hit", []string{"+ WALK"}, true, nil},
		{"compound hit", []string{"-"}, true, nil},
		{"miss with suggestions",
			[]string{"& " + typoReceive + " 3 0: receive, relieve, reprieve"},
			false, []string{"receive", "relieve", "reprieve"}},
		{"miss with one suggestion",
			[]string{"& " + typoThe + " 1 0: the"}, false, []string{"the"}},
		{"miss with none", []string{"# xyzzy 0"}, false, nil},
		{"a guess is still a miss",
			[]string{"? foo 2 0: fool, food"}, false, []string{"fool", "food"}},
		// Unrecognized lines count as correct.
		{"an unknown line reads as correct", []string{"% something new"}, true, nil},
		{"no lines at all reads as correct", nil, true, nil},
		{"blank lines are skipped", []string{"", "*"}, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := parseVerdict(tc.lines)
			if got.OK != tc.ok {
				t.Errorf("OK = %v, want %v", got.OK, tc.ok)
			}
			if len(got.Suggestions) != len(tc.sug) {
				t.Fatalf("suggestions = %q, want %q", got.Suggestions, tc.sug)
			}
			for i := range tc.sug {
				if got.Suggestions[i] != tc.sug[i] {
					t.Errorf("suggestions = %q, want %q", got.Suggestions, tc.sug)
				}
			}
		})
	}
}

// A malformed `&` line must not panic or invent a suggestion out of the bookkeeping.
func TestSuggestionsInHandlesAMalformedLine(t *testing.T) {
	t.Parallel()

	for _, line := range []string{"&", "& word 3 0", "& word 0 0: ", "& word 0 0:"} {
		if got := suggestionsIn(line); len(got) != 0 {
			t.Errorf("suggestionsIn(%q) = %q, want nothing", line, got)
		}
	}
}

// The `^` escape keeps a word starting with `*` or `@` from being a command, and a
// newline from turning the rest of the draft into commands.
func TestEscapeMakesAWordUnreadableAsACommand(t *testing.T) {
	t.Parallel()

	for _, word := range []string{"*add", "@accept", "#save", "~mode", "ordinary"} {
		got := escape(word)
		if !strings.HasPrefix(got, "^") {
			t.Errorf("escape(%q) = %q, want it to lead with the verbatim caret", word, got)
		}
		if strings.Contains(got[1:], "\n") {
			t.Errorf("escape(%q) = %q, want no newline", word, got)
		}
	}
	if got := escape("two\nlines"); strings.Contains(got, "\n") {
		t.Errorf("escape = %q, want the newline neutralized", got)
	}
}
