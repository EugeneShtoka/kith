package domain

import (
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Next-word suggestions learned from a room's own messages.

// Phrases is one room's trigram index: for each pair of words, what followed it.
type Phrases struct {
	// after maps a two-word key to the counts of what came next.
	after map[phraseKey]map[string]int
	// afterOne is the same over one word of context.
	afterOne map[string]map[string]int
	// words counts each word; the backoff when neither context was seen.
	words map[string]int
}

type phraseKey struct{ first, second string }

// phraseMineWeight is how much one of your own uses counts against someone else's.
const phraseMineWeight = 3

// PhrasesOf indexes a room's messages, weighting the ones me (every ID that is this
// person) sent.
func PhrasesOf(messages []Message, me []string) Phrases {
	index := Phrases{
		after:    make(map[phraseKey]map[string]int),
		afterOne: make(map[string]map[string]int),
		words:    make(map[string]int),
	}
	for i := range messages {
		index.Add(&messages[i], me)
	}
	return index
}

// Add counts one more message into the index in place.
func (p Phrases) Add(msg *Message, me []string) {
	if msg.Redacted || msg.Body == "" {
		return
	}
	weight := 1
	if slices.Contains(me, msg.Sender) {
		weight = phraseMineWeight
	}
	words := phraseWords(msg.Body)
	for at := range words {
		p.words[words[at]] += weight
		if at >= 1 {
			add(p.afterOne, words[at-1], words[at], weight)
		}
		if at >= 2 {
			key := phraseKey{words[at-2], words[at-1]}
			next := p.after[key]
			if next == nil {
				next = make(map[string]int, 2)
				p.after[key] = next
			}
			next[words[at]] += weight
		}
	}
}

// add counts one continuation in a one-word index.
func add(index map[string]map[string]int, key, word string, weight int) {
	next := index[key]
	if next == nil {
		next = make(map[string]int, 2)
		index[key] = next
	}
	next[word] += weight
}

// Next is the word that usually follows these two, if it leads the runner-up by ratio
// and has at least least uses.
func (p Phrases) Next(first, second string, ratio, least int) (string, bool) {
	if p.after == nil || ratio < 1 {
		return "", false
	}
	next := p.after[phraseKey{strings.ToLower(first), strings.ToLower(second)}]
	if len(next) == 0 {
		return "", false
	}
	lead, leadCount, runnerUp := "", 0, 0
	for word, count := range next {
		switch {
		case count > leadCount:
			// Alphabetical tie-break: map order is unstable and the suggestion must not
			// flicker between keystrokes.
			lead, leadCount, runnerUp = word, count, leadCount
		case count == leadCount && word < lead:
			lead, runnerUp = word, count
		case count > runnerUp:
			runnerUp = count
		}
	}
	if leadCount < least || (runnerUp > 0 && leadCount < runnerUp*ratio) {
		return "", false
	}
	return lead, true
}

// NextWords is what to offer after this draft, best first, up to n of them.
func (p Phrases) NextWords(draft string, n int) []string {
	if n <= 0 {
		return nil
	}
	words := phraseWords(draft)
	if len(words) == 0 || !strings.HasSuffix(draft, " ") {
		// Mid-word completion belongs to the vocabulary.
		return nil
	}
	out := make([]string, 0, n)
	seen := map[string]bool{}
	// Never the word just typed.
	seen[words[len(words)-1]] = true
	take := func(counts map[string]int) {
		for _, word := range rank(counts) {
			if len(out) == n {
				return
			}
			if seen[word] {
				continue
			}
			seen[word] = true
			out = append(out, word)
		}
	}
	if len(words) >= 2 {
		take(p.after[phraseKey{words[len(words)-2], words[len(words)-1]}])
	}
	take(p.afterOne[words[len(words)-1]])
	take(p.words)
	return out
}

// rank orders continuations by weight, then alphabetically so the list is stable.
func rank(counts map[string]int) []string {
	if len(counts) == 0 {
		return nil
	}
	words := make([]string, 0, len(counts))
	for word := range counts {
		words = append(words, word)
	}
	sort.Slice(words, func(i, j int) bool {
		if counts[words[i]] != counts[words[j]] {
			return counts[words[i]] > counts[words[j]]
		}
		return words[i] < words[j]
	})
	return words
}

// PhraseContext is the last two words of a draft, and false when there are not two.
func PhraseContext(draft string) (first, second string, ok bool) {
	if !strings.HasSuffix(draft, " ") {
		return "", "", false
	}
	words := phraseWords(draft)
	if len(words) < 2 {
		return "", "", false
	}
	return words[len(words)-2], words[len(words)-1], true
}

// phraseWords splits text into lowercased runs of letters.
func phraseWords(text string) []string {
	var out []string
	var word strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) {
			word.WriteRune(unicode.ToLower(r))
			continue
		}
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	if word.Len() > 0 {
		out = append(out, word.String())
	}
	return out
}
