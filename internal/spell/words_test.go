package spell

import (
	"testing"
)

// texts returns just the words, for the cases where position does not matter.
func texts(ws []Word) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Text)
	}
	return out
}

func same(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestWordsKeepsProse(t *testing.T) {
	t.Parallel()

	same(t, texts(Words("the quick brown fox")), "the", "quick", "brown", "fox")
	same(t, texts(Words("Don't — it's fine.")), "Don't", "it's", "fine")
	same(t, texts(Words("well-known problem")), "well-known", "problem")
}

// Every offset maps back onto its text; the composer underlines by them.
func TestWordOffsetsMapBack(t *testing.T) {
	t.Parallel()

	const text = "שלום world, `code` and https://example.org/x done"
	for _, w := range Words(text) {
		if got := text[w.Start:w.End]; got != w.Text {
			t.Errorf("offsets %d:%d give %q, want %q", w.Start, w.End, got, w.Text)
		}
	}
}

// Code, links, Matrix identifiers and shortcodes are skipped.
func TestWordsSkipsWhatADictionaryCannotJudge(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"inline code", "run `make check` now", []string{"run", "now"}},
		{"fenced block", "before\n```\nnot prose at all\n```\nafter",
			[]string{"before", "after"}},
		{"unclosed fence eats the rest", "before ``` still open",
			[]string{"before"}},
		{"http", "see http://example.org/a_b now", []string{"see", "now"}},
		{"https uppercase", "see HTTPS://Example.ORG now", []string{"see", "now"}},
		{"bare www", "see www.example.org now", []string{"see", "now"}},
		{"mxid", "ask @dana:example.org about it", []string{"ask", "about", "it"}},
		{"room alias", "join #room:example.org today", []string{"join", "today"}},
		{"room id", "the !abcdef:example.org room", []string{"the", "room"}},
		{"shortcode", "nice :thumbs_up: work", []string{"nice", "work"}},
		{"digits make an identifier", "use sha256 and v2 here",
			[]string{"use", "and", "here"}},
		{"underscore too", "the max_width option", []string{"the", "option"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			same(t, texts(Words(tc.text)), tc.want...)
		})
	}
}

// A bare colon is punctuation, not the start of a shortcode.
func TestWordsTreatsOrdinaryColonsAsPunctuation(t *testing.T) {
	t.Parallel()

	same(t, texts(Words("note: this matters")), "note", "this", "matters")
	same(t, texts(Words("ratio 3:1 here")), "ratio", "here")
	same(t, texts(Words("look :: here")), "look", "here")
}

// A word belongs to the script most of its letters are in, so one stray letter
// cannot move it.
func TestScriptIsDecidedPerWordByMajority(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		word string
		want Script
	}{
		{"hello", Latin},
		{"naïve", Latin},
		{"שלום", Hebrew},
		{"привет", Cyrillic},
		{"Ελλάδα", Greek},
		{"مرحبا", Arabic},
		// Scripts with no dictionary here are not Latin, so English never checks them.
		{"你好", Other},
		{"こんにちは", Other},
		{"안녕", Other},
		// One Latin letter loose in a Hebrew word does not make it Latin.
		{"שלוםa", Hebrew},
		// …and the reverse.
		{"helloש", Latin},
	} {
		t.Run(tc.word, func(t *testing.T) {
			t.Parallel()
			if got := scriptOf(tc.word); got != tc.want {
				t.Errorf("scriptOf(%q) = %v, want %v", tc.word, got, tc.want)
			}
		})
	}
}

// A mixed message files each word under its own script.
func TestWordsFilesAMixedSentencePerWord(t *testing.T) {
	t.Parallel()

	got := Words("hello שלום привет")
	if len(got) != 3 {
		t.Fatalf("got %d words, want 3", len(got))
	}
	for i, want := range []Script{Latin, Hebrew, Cyrillic} {
		if got[i].Script != want {
			t.Errorf("%q filed as %v, want %v", got[i].Text, got[i].Script, want)
		}
	}
}

func TestWordsOnEmptyAndPunctuationOnly(t *testing.T) {
	t.Parallel()

	if got := Words(""); len(got) != 0 {
		t.Errorf("got %q for empty text", texts(got))
	}
	if got := Words("— … !?"); len(got) != 0 {
		t.Errorf("got %q for punctuation only", texts(got))
	}
}
