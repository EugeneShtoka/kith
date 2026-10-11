// Package vocab ranks word completions from in-memory counts of recent messages:
// windows over this room, its space, your own, and everyone's. A lookup costs the words
// sharing the prefix, not the size of the cache.
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

// Score weights per window.
const (
	RoomWeight  = 10
	SpaceWeight = 3
	MineWeight  = 5
)

// Fold lowercases word and strips diacritics from Latin letters, matching the cache's
// full-text index (unicode61 remove_diacritics 2).
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
			continue // é is e
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

// Window counts the words of about limit messages; Stale reports when it should be
// rebuilt. Not safe for concurrent use: lookups sort lazily.
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

// Words is spell.Words' words (no code, URLs or numbers), split at inner apostrophes
// and hyphens like the full-text index, and folded.
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

// Stale reports whether the window has grown a quarter past its limit.
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

// Scope is the windows a completion is scored over; nil ones are skipped.
type Scope struct {
	Room, Space, Mine, Global *Window
}

// Rank returns up to limit words starting with the folded prefix that are worth
// offering, best first.
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
			// LongEnough also excludes the prefix itself.
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
