package spell

import "strings"

// The ispell pipe protocol (hunspell, enchant, nuspell, aspell). A word goes in on a
// line; result lines come back, then a blank line:
//
//	*                       correct
//	+ ROOT                  correct via an affix rule
//	-                       correct as a compound
//	& word 3 12: a, b, c    wrong, with suggestions
//	# word 12               wrong, no suggestions
//
// Input lines starting `*` add to the personal dictionary and `@` accept for the
// session, so every word is sent with the `^` text escape.

// Verdict is what the engine said about one word.
type Verdict struct {
	// OK is true for a dictionary word by any route (plain, affix, compound).
	OK bool
	// Suggestions are corrections, best first; often empty.
	Suggestions []string
}

// parseVerdict reads the engine's reply to one word. Unrecognized lines count as
// correct: a spurious underline costs more than a missed misspelling.
func parseVerdict(lines []string) Verdict {
	for _, line := range lines {
		if line == "" {
			continue
		}
		switch line[0] {
		case '&', '?':
			return Verdict{Suggestions: suggestionsIn(line)}
		case '#':
			return Verdict{}
		}
	}
	return Verdict{OK: true}
}

// suggestionsIn pulls the corrections out of an `&` line ("& word 3 12: a, b").
func suggestionsIn(line string) []string {
	_, list, found := strings.Cut(line, ": ")
	if !found {
		return nil
	}
	parts := strings.Split(list, ", ")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// escape prepares a word for the wire: `^` so it is read as text, and no newline
// inside it (the rest would be read as commands).
func escape(word string) string {
	return "^" + strings.NewReplacer("\n", " ", "\r", " ").Replace(word)
}
