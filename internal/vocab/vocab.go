// Package vocab ranks word completions from what was said recently, counted in memory.
//
// Each window counts the words of a bounded set of recent messages: this room's, its
// space's, your own, everyone's. A completion is scored over those windows, so its cost
// is the words sharing the prefix, not the size of the cache.
package vocab

import (
	"slices"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/spell"
)

// The score's weights: a use in this room outranks one in a sibling room, and your own
// use sits between them.
const (
	RoomWeight  = 10
	SpaceWeight = 3
	MineWeight  = 5
)

// Fold is how a word is compared and offered: lower case, with diacritics removed from
// Latin letters, as the cache's full-text index folds (unicode61 remove_diacritics 2).
// A prefix typed with or without an accent then finds the same word.
func Fold(word string) string {
	word = strings.ToLower(word)
	if isASCII(word) {
		return word
	}
	decomposed := norm.NFD.String(word)
	var b strings.Builder
	b.Grow(len(decomposed))
	lastLatin := false
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) && lastLatin {
			continue // a mark on a Latin letter: é is e
		}
		lastLatin = unicode.Is(unicode.Latin, r)
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Window counts the words of up to about limit messages. Past a quarter over, Stale
// says it should be rebuilt from the newest limit. Not safe for concurrent use: lookups
// sort lazily.
type Window struct {
	counts   map[string]int
	sorted   []string // counts' keys in order, rebuilt on first lookup after an Add
	dirty    bool
	messages int
	limit    int
}

// NewWindow is an empty window meant to hold about limit messages.
func NewWindow(limit int) *Window {
	return &Window{counts: map[string]int{}, limit: limit}
}

// Add counts one message's words.
func (w *Window) Add(body string) {
	w.messages++
	for _, word := range Words(body) {
		if w.counts[word] == 0 {
			w.dirty = true
		}
		w.counts[word]++
	}
}

// Words is a message's words as completion counts them, folded: spell.Words' words
// (so code, URLs and numbers are left out), split at inner apostrophes and hyphens as
// the full-text index splits them ("well-known" is "well" and "known").
func Words(body string) []string {
	var out []string
	for _, word := range spell.Words(body) {
		for _, part := range strings.FieldsFunc(word.Text, isJoiner) {
			out = append(out, Fold(part))
		}
	}
	return out
}

func isJoiner(r rune) bool { return r == '\'' || r == '’' || r == '-' }

// Stale reports whether the window has grown well past its limit.
func (w *Window) Stale() bool { return w.messages > w.limit+w.limit/4 }

// Count is how many times word was used in the window.
func (w *Window) Count(word string) int { return w.counts[word] }

// withPrefix calls fn with every word starting with prefix, in order.
func (w *Window) withPrefix(prefix string, fn func(word string)) {
	if w.dirty || w.sorted == nil {
		w.sorted = make([]string, 0, len(w.counts))
		for word := range w.counts {
			w.sorted = append(w.sorted, word)
		}
		slices.Sort(w.sorted)
		w.dirty = false
	}
	for i := sort.SearchStrings(w.sorted, prefix); i < len(w.sorted) && strings.HasPrefix(w.sorted[i], prefix); i++ {
		fn(w.sorted[i])
	}
}

// Scope is the windows a completion is scored over; a nil one has no term, as a scope
// that leaves the room or the space out.
type Scope struct {
	Room, Space, Mine, Global *Window
}

// Rank is the words starting with prefix (already folded) that are long enough to be
// worth offering, best first, at most limit.
func Rank(prefix string, s Scope, limit int) []domain.WordCandidate {
	if prefix == "" || limit <= 0 {
		return nil
	}
	scores := map[string]int{}
	add := func(w *Window, weight int) {
		if w == nil {
			return
		}
		w.withPrefix(prefix, func(word string) {
			// Only what saves some typing is offered (domain.LongEnough), which also
			// leaves out the prefix itself.
			if domain.LongEnough(prefix, word) {
				scores[word] += weight * w.Count(word)
			}
		})
	}
	add(s.Room, RoomWeight)
	add(s.Space, SpaceWeight)
	add(s.Mine, MineWeight)
	add(s.Global, 1)

	out := make([]domain.WordCandidate, 0, len(scores))
	for word, score := range scores {
		out = append(out, domain.WordCandidate{Word: word, Score: score})
	}
	slices.SortFunc(out, func(a, b domain.WordCandidate) int {
		if a.Score != b.Score {
			return b.Score - a.Score
		}
		return strings.Compare(a.Word, b.Word)
	})
	return out[:min(limit, len(out))]
}
