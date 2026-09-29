// Package spell checks what you are writing against real dictionaries.
package spell

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// One tokenizer serves both the checker and the language detector so they agree on
// word boundaries. A word belongs to the script most of its letters are in, and skip
// regions (code, URLs, MXIDs, shortcodes) are computed per text, never across a join.

// Script is the writing system a word is in.
type Script uint8

// The scripts worth telling apart; everything else is Other.
const (
	Other Script = iota
	Latin
	Hebrew
	Cyrillic
	Arabic
	Greek
)

func (s Script) String() string {
	switch s {
	case Latin:
		return "Latin"
	case Hebrew:
		return "Hebrew"
	case Cyrillic:
		return "Cyrillic"
	case Arabic:
		return "Arabic"
	case Greek:
		return "Greek"
	default:
		return "other"
	}
}

// Word is one checkable word with logical byte offsets into its text (the form that
// survives a later bidi reorder).
type Word struct {
	Text   string
	Start  int
	End    int
	Script Script
}

// Words returns the checkable words in text, in order: code, links, Matrix
// identifiers, shortcodes and tokens with digits are skipped.
func Words(text string) []Word {
	var out []Word
	for i := 0; i < len(text); {
		if n := skipAt(text, i); n > 0 {
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if !isWordRune(r) {
			i += size
			continue
		}
		start := i
		i = tokenEnd(text, i)
		if word, ok := checkable(text[start:i]); ok {
			out = append(out, Word{
				Text:   word,
				Start:  start + strings.Index(text[start:i], word),
				End:    start + strings.Index(text[start:i], word) + len(word),
				Script: scriptOf(word),
			})
		}
	}
	return out
}

// skipAt is how many bytes to skip when text[i:] starts something a dictionary should
// not see, else 0. Fences are tried before inline code.
func skipAt(text string, i int) int {
	switch {
	case strings.HasPrefix(text[i:], "```"):
		return spanTo(text, i, "```")
	case text[i] == '`':
		return spanTo(text, i, "`")
	case hasPrefixFold(text[i:], "http://"), hasPrefixFold(text[i:], "https://"),
		hasPrefixFold(text[i:], "www."):
		return runTo(text, i, unicode.IsSpace)
	// MXID, room alias or room ID.
	case text[i] == '@' || text[i] == '#' || text[i] == '!':
		return runTo(text, i, unicode.IsSpace)
	case text[i] == ':':
		return shortcodeAt(text, i)
	}
	return 0
}

// spanTo skips a delimited span including both delimiters; unclosed runs to the end.
func spanTo(text string, i int, delim string) int {
	rest := text[i+len(delim):]
	if end := strings.Index(rest, delim); end >= 0 {
		return len(delim) + end + len(delim)
	}
	return len(text) - i
}

// runTo skips from i to the first rune satisfying stop, or to the end.
func runTo(text string, i int, stop func(rune) bool) int {
	for j := i; j < len(text); {
		r, size := utf8.DecodeRuneInString(text[j:])
		if stop(r) {
			return j - i
		}
		j += size
	}
	return len(text) - i
}

// shortcodeAt skips `:shortcode:`; 0 for a bare colon.
func shortcodeAt(text string, i int) int {
	rest := text[i+1:]
	for j, r := range rest {
		switch {
		case r == ':':
			if j == 0 {
				return 0 // "::" is not a shortcode
			}
			return 1 + j + 1
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '+' || r == '-':
			// still inside a plausible name
		default:
			return 0
		}
	}
	return 0
}

// isWordRune reports whether r can be in a token. Digits and underscores count so a
// token containing one is discarded whole ("sha256" is not checked as "sha").
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' ||
		r == '\'' || r == '’' || r == '-'
}

// tokenEnd finds the end of the token starting at i.
func tokenEnd(text string, i int) int {
	return i + runTo(text, i, func(r rune) bool { return !isWordRune(r) })
}

// checkable reports whether a token is worth looking up (no digit or underscore, some
// letter) and trims outer apostrophes and hyphens.
func checkable(token string) (string, bool) {
	for _, r := range token {
		if unicode.IsDigit(r) || r == '_' {
			return "", false
		}
	}
	word := strings.Trim(token, "'’-")
	if word == "" {
		return "", false
	}
	if !strings.ContainsFunc(word, unicode.IsLetter) {
		return "", false
	}
	return word, true
}

// scriptOf is the script most of a word's letters belong to; ties go to the first.
func scriptOf(word string) Script {
	var counts [Greek + 1]int
	first := Other
	for _, r := range word {
		s := scriptOfRune(r)
		if s == Other {
			continue
		}
		if first == Other {
			first = s
		}
		counts[s]++
	}
	best, bestN := Other, 0
	for s, n := range counts {
		if n > bestN {
			best, bestN = Script(s), n
		}
	}
	if bestN > 0 && counts[first] == bestN {
		return first
	}
	return best
}

// scriptOfRune places one rune coarsely (which dictionary).
func scriptOfRune(r rune) Script {
	switch {
	case r >= 0x0590 && r <= 0x05FF:
		return Hebrew
	case r >= 0x0400 && r <= 0x04FF, r >= 0x0500 && r <= 0x052F:
		return Cyrillic
	case r >= 0x0600 && r <= 0x06FF, r >= 0x0750 && r <= 0x077F:
		return Arabic
	case r >= 0x0370 && r <= 0x03FF, r >= 0x1F00 && r <= 0x1FFF:
		return Greek
	case unicode.IsLetter(r):
		return Latin
	}
	return Other
}

// hasPrefixFold is strings.HasPrefix ignoring ASCII case.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
