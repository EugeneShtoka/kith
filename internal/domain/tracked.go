package domain

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tracked words: words you want to see wherever they are said.

// Tracked is the configured word list.
type Tracked struct {
	// Words are the entries, each optionally wrapped in `*` to widen how it matches.
	Words []string
}

// Hit is one occurrence, in byte offsets into the searched text.
type Hit struct {
	Start, End int
	// Word is the entry as configured (`*deploy*`), not the matched text.
	Word string
}

// Any reports whether anything is tracked, so an empty list costs no scan.
func (t Tracked) Any() bool { return len(t.Words) > 0 }

// Find returns every occurrence of every tracked word in body, in order per entry.
func (t Tracked) Find(body string) []Hit {
	if !t.Any() || body == "" {
		return nil
	}
	folded := strings.ToLower(body)
	var hits []Hit
	for _, entry := range t.Words {
		needle, left, right := trackedGlob(entry)
		if needle == "" {
			continue
		}
		last := Hit{Start: -1, End: -1}
		for at := 0; at < len(folded); {
			i := strings.Index(folded[at:], needle)
			if i < 0 {
				break
			}
			start := at + i
			end := start + len(needle)
			if startsCleanly(folded, start, left) && endsCleanly(folded, end, right) {
				hit := Hit{Start: wordStart(folded, start), End: wordEnd(folded, end), Word: entry}
				if hit != last {
					hits = append(hits, hit)
					last = hit
				}
			}
			// Advance one rune, not past the match, so overlapping matches are seen; the
			// dedup above collapses those in one word.
			at = start + runeLenAt(folded, start)
		}
	}
	return hits
}

// wordStart and wordEnd widen a match to the word containing it.
func wordStart(text string, at int) int {
	for at > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:at])
		if !isWordRune(r) {
			break
		}
		at -= size
	}
	return at
}

func wordEnd(text string, at int) int {
	for at < len(text) {
		r, size := utf8.DecodeRuneInString(text[at:])
		if !isWordRune(r) {
			break
		}
		at += size
	}
	return at
}

// trackedGlob splits an entry into its needle and whether each end is open: a `*`
// on an end means no word-boundary anchor there.
func trackedGlob(entry string) (needle string, leftOpen, rightOpen bool) {
	needle = strings.ToLower(strings.TrimSpace(entry))
	if leftOpen = strings.HasPrefix(needle, "*"); leftOpen {
		needle = needle[1:]
	}
	if rightOpen = strings.HasSuffix(needle, "*"); rightOpen {
		needle = needle[:len(needle)-1]
	}
	return needle, leftOpen, rightOpen
}

// startsCleanly checks the rune before start; endsCleanly the rune at end.
func startsCleanly(text string, start int, open bool) bool {
	if open || start == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:start])
	return !isWordRune(r)
}

func endsCleanly(text string, end int, open bool) bool {
	if open || end == len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[end:])
	return !isWordRune(r)
}

// isWordRune reports whether r is a letter or digit.
func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// runeLenAt is the byte length of the rune at i, at least one so a scan cannot
// stall on invalid input.
func runeLenAt(text string, i int) int {
	_, size := utf8.DecodeRuneInString(text[i:])
	return max(size, 1)
}

// TrackedRule is a set of words scoped by place and sender, with its own notify setting.
type TrackedRule struct {
	// Words are this rule's entries, each with its own glob width. See Tracked.Words.
	Words []string
	// Where is the places the rule applies to (RoomFacts entries).
	Where PlaceFilter
	// From narrows the rule to these senders' messages, by MXID. Empty is anybody.
	From []string
	// Notify overrides the global `[tracked] notify` for this rule's words.
	Notify *bool
}

// Admits reports whether this rule applies to a message in this place from this
// sender.
func (r TrackedRule) Admits(room RoomFacts, sender string) bool {
	return r.Where.Admits(room) && (len(r.From) == 0 || slices.Contains(r.From, sender))
}

// TrackedFor is the union of the words of every rule admitting this place and sender.
func TrackedFor(rules []TrackedRule, room RoomFacts, sender string) Tracked {
	var words []string
	seen := map[string]bool{}
	for i := range rules {
		if !rules[i].Admits(room, sender) {
			continue
		}
		for _, word := range rules[i].Words {
			if word != "" && !seen[word] {
				seen[word] = true
				words = append(words, word)
			}
		}
	}
	return Tracked{Words: words}
}

// TrackedNotifies reports whether hits may notify, asking only the admitting rules
// whose words actually hit; fallback applies where a rule sets no Notify.
func TrackedNotifies(rules []TrackedRule, hits []Hit, fallback bool, room RoomFacts, sender string) bool {
	if len(hits) == 0 {
		return false
	}
	hit := map[string]bool{}
	for _, h := range hits {
		hit[h.Word] = true
	}
	for i := range rules {
		if !rules[i].Admits(room, sender) {
			continue
		}
		for _, word := range rules[i].Words {
			if !hit[word] {
				continue
			}
			if rules[i].Notify != nil && *rules[i].Notify {
				return true
			}
			if rules[i].Notify == nil && fallback {
				return true
			}
		}
	}
	return false
}
