package domain

import (
	"slices"
	"testing"
)

func said(sender string, bodies ...string) []Message {
	out := make([]Message, 0, len(bodies))
	for _, body := range bodies {
		out = append(out, Message{Sender: sender, Body: body})
	}
	return out
}

func TestPhrasesNeedsTwoWordsAndAClearLeader(t *testing.T) {
	t.Parallel()

	msgs := said("@dana:x",
		"I will be there shortly",
		"I will be there shortly",
		"I will be late again",
	)
	index := PhrasesOf(msgs, "@me:x")

	// "I will" → "be" three times against nothing else: a clear leader.
	if got, ok := index.Next("I", "will", 3, 2); !ok || got != "be" {
		t.Fatalf("Next(I, will) = %q, %v; want be", got, ok)
	}
	// "will be" → "there" twice, "late" once: twice is not three times, so nothing is
	// drawn.
	if got, ok := index.Next("will", "be", 3, 2); ok {
		t.Fatalf("Next(will, be) = %q, want nothing while the leader is not clear", got)
	}
	// At a lower ratio the same pair resolves, which is what makes the setting a setting.
	if got, ok := index.Next("will", "be", 2, 2); !ok || got != "there" {
		t.Fatalf("Next(will, be) at 2x = %q, %v; want there", got, ok)
	}
	// Seen once is a coincidence, not a phrase.
	if got, ok := index.Next("be", "late", 3, 2); ok {
		t.Fatalf("Next(be, late) = %q, want nothing for a continuation seen once", got)
	}
	if got, ok := index.Next("be", "late", 3, 1); !ok || got != "again" {
		t.Fatalf("Next(be, late) with least=1 = %q, %v; want again", got, ok)
	}
	// A pair nobody has said.
	if _, ok := index.Next("never", "said", 1, 1); ok {
		t.Fatal("Next() answered for a pair with no history")
	}
	// And the zero value answers nothing rather than panicking: an empty room is the
	// state every room starts in.
	var empty Phrases
	if _, ok := empty.Next("I", "will", 1, 1); ok {
		t.Fatal("the zero value answered")
	}
}

// Your own phrasing outweighs somebody else's, because a continuation is meant to sound
// like you — but theirs still counts, because it is evidence about the room.
func TestPhrasesWeighYourOwnWords(t *testing.T) {
	t.Parallel()

	const me = "@me:x"
	msgs := append(
		said("@dana:x", "let us go north", "let us go north"),
		said(me, "let us go south")...,
	)
	index := PhrasesOf(msgs, me)
	// Theirs: 2. Yours: 3 (one use, weighted).
	if got, ok := index.Next("us", "go", 1, 1); !ok || got != "south" {
		t.Fatalf("Next(us, go) = %q, %v; want your own word", got, ok)
	}
	// With no account given, the weighting drops out and theirs leads on count.
	if got, ok := PhrasesOf(msgs, "").Next("us", "go", 1, 1); !ok || got != "north" {
		t.Fatalf("Next(us, go) with no account = %q, %v; want theirs", got, ok)
	}
}

func TestPhrasesSkipsWhatWasTakenBack(t *testing.T) {
	t.Parallel()

	msgs := []Message{
		{Sender: "@dana:x", Body: "the secret is hunter two", Redacted: true},
		{Sender: "@dana:x", Body: "the secret is hunter two", Redacted: true},
		{Sender: "@dana:x", Body: "the weather is fine today"},
		{Sender: "@dana:x", Body: "the weather is fine today"},
	}
	index := PhrasesOf(msgs, "@me:x")
	if got, ok := index.Next("secret", "is", 1, 1); ok {
		t.Fatalf("Next() = %q from a deleted message, want nothing", got)
	}
	if got, ok := index.Next("weather", "is", 3, 2); !ok || got != "fine" {
		t.Fatalf("Next(weather, is) = %q, %v; want fine", got, ok)
	}
}

func TestPhraseContextNeedsAFinishedWord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		draft         string
		first, second string
		ok            bool
	}{
		{"I will ", "i", "will", true},
		// Mid-word is the *word* ghost's question, not this one.
		{"I wil", "", "", false},
		{"will ", "", "", false},
		{"", "", "", false},
		// Punctuation separates words, so a sentence that ended still has two behind it.
		{"is it true? ", "it", "true", true},
		// Case is folded, because the index is.
		{"I WILL ", "i", "will", true},
	}
	for _, tc := range cases {
		first, second, ok := PhraseContext(tc.draft)
		if ok != tc.ok || first != tc.first || second != tc.second {
			t.Errorf("PhraseContext(%q) = %q, %q, %v; want %q, %q, %v",
				tc.draft, first, second, ok, tc.first, tc.second, tc.ok)
		}
	}
}

// The same index answers two questions with different bars, and that is the point: a
// ghost is drawn without being asked for and has to be right, while a list of four is
// asked for by the eye and answered by a number key — so a wrong option beside a right
// one costs nothing.
func TestNextWordsOffersWhatTheGateWouldRefuse(t *testing.T) {
	t.Parallel()

	// Two continuations, neither dominant: the ghost stays silent and the list does not.
	index := PhrasesOf([]Message{
		{Sender: "@me:x", Body: "let us ship it"},
		{Sender: "@me:x", Body: "let us ship it"},
		{Sender: "@me:x", Body: "let us wait here"},
		{Sender: "@me:x", Body: "let us wait here"},
	}, "@me:x")

	if word, ok := index.Next("let", "us", 3, 2); ok {
		t.Errorf("the ghost offered %q where nothing dominates", word)
	}
	got := index.NextWords("let us ", 4)
	if len(got) < 2 || !has(got, "ship") || !has(got, "wait") {
		t.Fatalf("NextWords = %v, want both continuations", got)
	}
}

// The ladder: two words of context where there are two, one word where the pair has
// never been seen, and this account's own commonest words where neither has.
func TestNextWordsWalksTheLadder(t *testing.T) {
	t.Parallel()

	index := PhrasesOf([]Message{
		{Sender: "@me:x", Body: "i will check the logs"},
		{Sender: "@me:x", Body: "i will check the logs"},
		{Sender: "@me:x", Body: "please check the config"},
		{Sender: "@me:x", Body: "the deploy is ready"},
	}, "@me:x")

	// The pair has been seen: its continuations come first.
	if got := index.NextWords("i will check the ", 4); len(got) == 0 || got[0] != "logs" {
		t.Errorf("NextWords = %v, want the pair's own continuation first", got)
	}
	// This pair never has, but the last word has: the one-word rung answers.
	if got := index.NextWords("nobody would check the ", 4); !has(got, "logs") || !has(got, "config") {
		t.Errorf("NextWords = %v, want what follows the last word", got)
	}
	// Neither has: the account's own commonest words, which is better than silence.
	got := index.NextWords("xylophone zither ", 4)
	if len(got) == 0 {
		t.Fatal("NextWords offered nothing at all, which is the one answer a keyboard never gives")
	}
	if !has(got, "check") && !has(got, "the") {
		t.Errorf("NextWords = %v, want this account's own commonest words", got)
	}
}

// Function words are offered like any other, because they are the most predictable words
// there are: 30% of what this account actually types next is one of them.
func TestNextWordsOffersFunctionWords(t *testing.T) {
	t.Parallel()

	index := PhrasesOf([]Message{
		{Sender: "@me:x", Body: "i went to the office"},
		{Sender: "@me:x", Body: "i went to the office"},
	}, "@me:x")

	if got := index.NextWords("i went ", 4); len(got) == 0 || got[0] != "to" {
		t.Errorf("NextWords = %v, want the function word that actually follows", got)
	}
}

// The word just typed is never offered back: "the the" is a stutter rather than a
// prediction, and it is what a bare count produces after a common word.
func TestNextWordsDoesNotStutter(t *testing.T) {
	t.Parallel()

	index := PhrasesOf([]Message{
		{Sender: "@me:x", Body: "the the the and"},
		{Sender: "@me:x", Body: "the the the and"},
	}, "@me:x")

	for _, word := range index.NextWords("the the ", 4) {
		if word == "the" {
			t.Fatalf("NextWords = %v, want no repeat of the word just typed", index.NextWords("the the ", 4))
		}
	}
}

// Mid-word is the vocabulary's question: this one answers only after a finished word.
func TestNextWordsWaitsForAFinishedWord(t *testing.T) {
	t.Parallel()

	index := PhrasesOf([]Message{{Sender: "@me:x", Body: "i will check the logs"}}, "@me:x")
	if got := index.NextWords("i will che", 4); len(got) != 0 {
		t.Errorf("NextWords = %v mid-word, want nothing — that is the word ghost's question", got)
	}
}

func has(words []string, want string) bool {
	return slices.Contains(words, want)
}
