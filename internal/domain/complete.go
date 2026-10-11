package domain

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Word completion from what has been said.
const (
	// CompleteMinPrefix is how much must be typed before anything is offered.
	CompleteMinPrefix = 3
	// CompleteMinSaving is how many characters a candidate must save to be offered.
	CompleteMinSaving = 2
	// CompleteChoices is how many a chooser offers.
	CompleteChoices = 3
)

// LongEnough reports whether word saves at least CompleteMinSaving runes over prefix.
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
	// Prefix is lowercased by the caller: the full-text index, and so the vocabulary,
	// is case-folded.
	Prefix string
	// RoomIDs is the open room and any room it was upgraded from.
	RoomIDs []RoomID
	// SpaceRooms is every room in this room's spaces, including this one.
	SpaceRooms []RoomID
	// Scope is how wide to look: "room", "space" or "global".
	Scope string
	// Sources names which corpora may answer; empty means all.
	Sources []string
	// Limit bounds the candidates returned.
	Limit int
}

// Wants reports whether one source may answer this request; an empty list means all.
func (r CompleteRequest) Wants(source string) bool {
	return len(r.Sources) == 0 || slices.Contains(r.Sources, source)
}

// Dominant is the first candidate when its score is at least ratio times the second's.
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

// MergeCandidates appends extra's new words to ranked, scored below ranked's last.
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

// RecaseLike applies the typed prefix's case (upper or capitalized) to word.
func RecaseLike(typed, word string) string {
	if typed == "" || word == "" {
		return word
	}
	// All caps needs two letters: a single capital is just a capitalized word.
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
