package spell

import (
	"slices"
	"strings"
	"testing"
)

// tags is the offered dictionaries, for the cases where the arithmetic is not the point.
func tags(cs []Candidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Tag)
	}
	return out
}

func has(list []string, want string) bool {
	return slices.Contains(list, want)
}

// repeat builds a corpus big enough to clear the word floor without writing it out.
func repeat(t *testing.T, c *Counts, text string, times int, mine bool) {
	t.Helper()
	for range times {
		c.Add(text, mine)
	}
}

// The shape this account actually has: mostly English, a sixth Hebrew, a twelfth
// Russian — and no Ukrainian at all, which the marker sets have to get right or a
// dictionary nobody needs is offered.
func TestDetectOffersTheLanguagesInTheCorpus(t *testing.T) {
	t.Parallel()

	var c Counts
	repeat(t, &c, "the quick brown fox jumps over the lazy dog", 40, true)
	repeat(t, &c, "שלום וברכה לכולם היום", 20, true)
	repeat(t, &c, "привет как дела у тебя сегодня", 15, true)

	got := tags(c.Candidates())
	for _, want := range []string{"en_US", "he_IL", "ru_RU"} {
		if !has(got, want) {
			t.Errorf("got %v, want it to include %s", got, want)
		}
	}
	if has(got, "uk_UA") {
		t.Errorf("got %v — Ukrainian is not in this corpus", got)
	}
}

// The bug that made the marker sets worth writing as codepoints: an ASCII "I" inside
// the Ukrainian set counted every English *"I"* and reported a language with no words
// in the corpus at all.
func TestUkrainianMarkersDoNotMatchLatinLetters(t *testing.T) {
	t.Parallel()

	var c Counts
	repeat(t, &c, "I think I know what I am doing here I do", 40, true)
	repeat(t, &c, "привет как дела у тебя сегодня было ы", 15, true)

	if got := tags(c.Candidates()); has(got, "uk_UA") {
		t.Errorf("got %v — English capital I is not a Ukrainian і", got)
	}
}

// Cyrillic is a choice between two, decided by the letters only one of them has.
func TestCyrillicPicksTheLanguageItsLettersName(t *testing.T) {
	t.Parallel()

	t.Run("russian", func(t *testing.T) {
		t.Parallel()
		var c Counts
		repeat(t, &c, "это был обычный вопрос ы ъ э", 30, true)
		if got := tags(c.Candidates()); !has(got, "ru_RU") || has(got, "uk_UA") {
			t.Errorf("got %v, want ru_RU and not uk_UA", got)
		}
	})

	t.Run("ukrainian", func(t *testing.T) {
		t.Parallel()
		var c Counts
		repeat(t, &c, "це звичайне питання і ї є ґ", 30, true)
		if got := tags(c.Candidates()); !has(got, "uk_UA") || has(got, "ru_RU") {
			t.Errorf("got %v, want uk_UA and not ru_RU", got)
		}
	})

	// Plenty of Cyrillic sentences contain neither language's distinctive letters, and
	// the answer then has to be the commoner one rather than nothing.
	t.Run("neither, falls back", func(t *testing.T) {
		t.Parallel()
		var c Counts
		repeat(t, &c, "она делала работу на компьютере", 30, true)
		if got := tags(c.Candidates()); !has(got, "ru_RU") {
			t.Errorf("got %v, want the fallback ru_RU", got)
		}
	})
}

// Somebody writing German writes English too, so the accented language is offered
// *alongside* English rather than instead of it.
func TestAnAccentedLanguageIsOfferedBesideEnglish(t *testing.T) {
	t.Parallel()

	var c Counts
	repeat(t, &c, "das ist wirklich schön und größer für über alle", 40, true)

	got := tags(c.Candidates())
	if !has(got, "de_DE") {
		t.Errorf("got %v, want de_DE", got)
	}
	if !has(got, "en_US") {
		t.Errorf("got %v, want en_US beside it", got)
	}
}

// Plain English offers only English: no accents means no accented language, or every
// corpus would collect dictionaries it has no use for.
func TestPlainEnglishOffersOnlyEnglish(t *testing.T) {
	t.Parallel()

	var c Counts
	repeat(t, &c, "the quick brown fox jumps over the lazy dog again", 40, true)

	if got := tags(c.Candidates()); len(got) != 1 || got[0] != "en_US" {
		t.Errorf("got %v, want just en_US", got)
	}
}

// The bars exist to keep an accident out. Two words of Arabic in a large corpus is not
// a language, and — separately — Arabic has no dictionary in the manifest, so it could
// not be offered even if it cleared them.
func TestAStrayQuotationIsNotALanguage(t *testing.T) {
	t.Parallel()

	var c Counts
	repeat(t, &c, "the quick brown fox jumps over the lazy dog", 200, true)
	c.Add("مرحبا بك", false)
	c.Add("Ελλάδα", false)

	for _, unwanted := range []string{"ar", "el_GR"} {
		if has(tags(c.Candidates()), unwanted) {
			t.Errorf("offered %s for two words", unwanted)
		}
	}
}

// A corpus too small to conclude anything from concludes nothing.
func TestATinyCorpusOffersNothing(t *testing.T) {
	t.Parallel()

	var c Counts
	c.Add("hello there", true)
	if got := c.Candidates(); len(got) != 0 {
		t.Errorf("got %v from four words, want nothing", tags(got))
	}
	var empty Counts
	if got := empty.Candidates(); len(got) != 0 {
		t.Errorf("got %v from nothing", tags(got))
	}
}

// What you write counts for more than what you read — but a language you only read is
// still visible, because it is one you will eventually answer in.
func TestOwnMessagesWeighMore(t *testing.T) {
	t.Parallel()

	var mine, theirs Counts
	repeat(t, &mine, "שלום וברכה לכולם היום", 20, true)
	repeat(t, &theirs, "שלום וברכה לכולם היום", 20, false)

	m, o := mine.Candidates(), theirs.Candidates()
	if len(m) == 0 || len(o) == 0 {
		t.Fatalf("both should offer Hebrew; got %v and %v", tags(m), tags(o))
	}
	if m[0].Words <= o[0].Words {
		t.Errorf("own words counted %d, others %d — own should weigh more", m[0].Words, o[0].Words)
	}
}

// The offer shows its reasoning, so the share has to be a real fraction of everything
// classified rather than a number that happens to look like one.
func TestSharesAreFractionsOfTheWholeCorpus(t *testing.T) {
	t.Parallel()

	var c Counts
	repeat(t, &c, "the quick brown fox jumps over the lazy dog", 40, true)
	repeat(t, &c, "שלום וברכה לכולם היום", 40, true)

	total := 0.0
	for _, cand := range c.Candidates() {
		if cand.Share <= 0 || cand.Share > 1 {
			t.Errorf("%s share = %v, want a fraction", cand.Tag, cand.Share)
		}
		total += cand.Share
	}
	if total > 1.0001 {
		t.Errorf("shares sum to %v, which is more than the corpus", total)
	}
}

// Skip regions are per text. A fenced block in one message must not swallow the next.
func TestDetectCountsEachMessageSeparately(t *testing.T) {
	t.Parallel()

	var c Counts
	c.Add("before ``` unclosed fence", true)
	repeat(t, &c, "שלום וברכה לכולם היום", 30, true)

	if got := tags(c.Candidates()); !has(got, "he_IL") {
		t.Errorf("got %v — an unclosed fence in an earlier message ate the corpus", got)
	}
}

// Every marker set has to be letters of its own script, or it matches the wrong words.
func TestMarkerSetsAreCoherent(t *testing.T) {
	t.Parallel()

	for tag, set := range distinctive {
		script, known := markerScript[tag]
		if !known {
			t.Errorf("%s has markers but no script", tag)
			continue
		}
		if _, ok := dictionarySources[tag]; !ok {
			t.Errorf("%s has markers but no dictionary to offer", tag)
		}
		for _, r := range set {
			if got := scriptOfRune(r); got != script {
				t.Errorf("%s marker %q is %v, want %v", tag, string(r), got, script)
			}
		}
	}
	// The two Cyrillic sets must not overlap, or neither can decide anything.
	if both := strings.ContainsAny(distinctive["ru_RU"], distinctive["uk_UA"]); both {
		t.Error("the Russian and Ukrainian marker sets share a letter")
	}
}
