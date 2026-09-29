package domain

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Finishing a word from what has actually been said.

const (
	// CompleteMinPrefix is how much must be typed before anything is offered.
	CompleteMinPrefix = 3
	// CompleteMinSaving is how many characters a candidate must save to be offered at
	// all.
	CompleteMinSaving = 2
	// CompleteChoices is how many a chooser offers.
	CompleteChoices = 3
)

// LongEnough reports whether a candidate saves enough over what has been typed to be
// worth offering.
func LongEnough(prefix, word string) bool {
	return len([]rune(word)) >= len([]rune(prefix))+CompleteMinSaving
}

// WordCandidate is one completion, with the score that ranked it.
type WordCandidate struct {
	// Word is the whole word, not the part still to be typed.
	Word string
	// Score is the weighted count behind it.
	Score int
}

// CompleteRequest is one lookup: a partly typed word, and where it is being typed.
type CompleteRequest struct {
	// Prefix is what has been typed of the word, already lowercased by the caller when
	// the language has case at all — the full-text index folds, so the vocabulary is
	// folded, and matching it means asking in the same case.
	Prefix string
	// RoomIDs is the conversation being typed in: the open room, and any room it was
	// upgraded from.
	RoomIDs []RoomID
	// SpaceRooms is every room in this room's spaces, including this one.
	SpaceRooms []RoomID
	// Scope is how wide to look: "room", "space" or "global".
	Scope string
	// Sources names which of the two corpora may answer.
	Sources []string
	// Limit bounds the candidates returned.
	Limit int
}

// Wants reports whether one source may answer this request; an empty list means all.
func (r CompleteRequest) Wants(source string) bool {
	return len(r.Sources) == 0 || slices.Contains(r.Sources, source)
}

// Dominant is the leading candidate when it leads clearly enough to be drawn without
// being asked for, and false when the field is too close to call.
func Dominant(candidates []WordCandidate, ratio int) (WordCandidate, bool) {
	if len(candidates) == 0 || ratio < 1 {
		return WordCandidate{}, false
	}
	lead := candidates[0]
	if len(candidates) == 1 {
		return lead, true
	}
	if lead.Score >= candidates[1].Score*ratio {
		return lead, true
	}
	return WordCandidate{}, false
}

// MergeCandidates folds a second source into a ranked list, keeping the first source's
// order and appending only what it did not have.
func MergeCandidates(ranked, extra []WordCandidate, limit int) []WordCandidate {
	seen := make(map[string]struct{}, len(ranked))
	for _, c := range ranked {
		seen[c.Word] = struct{}{}
	}
	floor := 0
	if len(ranked) > 0 {
		floor = ranked[len(ranked)-1].Score
	}
	out := ranked
	for i, c := range extra {
		if _, dup := seen[c.Word]; dup {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		seen[c.Word] = struct{}{}
		out = append(out, WordCandidate{Word: c.Word, Score: max(floor-1-i, 0)})
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// RecaseLike restores the case the typist was using, because the vocabulary has none.
func RecaseLike(typed, word string) string {
	if typed == "" || word == "" {
		return word
	}
	// Two letters before this counts as shouting: a single capital is how a sentence
	// and a name both begin, and reading "C" as SHOUTING would make the commonest
	// capital there is the one that behaves strangely.
	if runes := []rune(typed); len(runes) > 1 && strings.ToUpper(typed) == typed && strings.ToLower(typed) != typed {
		return strings.ToUpper(word)
	}
	first, _ := utf8.DecodeRuneInString(typed)
	if !unicode.IsUpper(first) {
		return word
	}
	head, size := utf8.DecodeRuneInString(word)
	return string(unicode.ToUpper(head)) + word[size:]
}
