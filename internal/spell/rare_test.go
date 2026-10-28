package spell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testFreq builds a Frequencies from `word count` lines.
func testFreq(t *testing.T, tag, body string) *Frequencies {
	t.Helper()
	path := filepath.Join(t.TempDir(), tag+freqExt)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := LoadFreq(path)
	if err != nil {
		t.Fatalf("LoadFreq: %v", err)
	}
	return f
}

// Latin stand-ins for the Hebrew cases: `hte` and `hosue` transpose `the` and `house`,
// `tqe` needs a substitution, `stll` a letter put back.
const latinList = "" +
	"the 500000\n" +
	"house 90000\n" +
	"still 40000\n" +
	"tie 20000\n" +
	"hte 30\n" +
	"tqe 5\n" +
	"rarely 400\n" +
	"rarer 4\n"

func latinRarity(t *testing.T) *Rarity {
	t.Helper()
	aff := parseAffix("SET UTF-8\nTRY etaoinshrdlu\nMAP 1\nMAP hq\n")
	return NewRarity(testFreq(t, "en_US", latinList), aff)
}

func TestRareCatchesATranspositionOfACommonWord(t *testing.T) {
	t.Parallel()

	hint, rare := latinRarity(t).Rare("hte", 100)
	if !rare || len(hint.Alternatives) == 0 || hint.Alternatives[0] != "the" {
		t.Fatalf("Rare(hte) = %v, %v; want the", hint.Alternatives, rare)
	}
}

// MAP substitutions, which autocorrect excludes: `tqe` reaches `the` only through the
// dictionary's MAP set.
func TestRareUsesTheDictionarysConfusableSets(t *testing.T) {
	t.Parallel()

	hint, rare := latinRarity(t).Rare("tqe", 100)
	if !rare || len(hint.Alternatives) == 0 || hint.Alternatives[0] != "the" {
		t.Fatalf("Rare(tqe) = %v, %v; want the", hint.Alternatives, rare)
	}
	// Without the MAP set, no transposition or insertion turns tqe into the.
	plain := NewRarity(testFreq(t, "en_US", latinList), parseAffix("TRY etaoinshrdlu\n"))
	if _, rare := plain.Rare("tqe", 100); rare {
		t.Error("Rare(tqe) fired without a MAP set, so something else generated it")
	}
}

// A letter that did not register, put back from TRY.
func TestRareCatchesAMissingLetter(t *testing.T) {
	t.Parallel()

	hint, rare := latinRarity(t).Rare("stll", 100)
	if !rare || len(hint.Alternatives) == 0 || hint.Alternatives[0] != "still" {
		t.Fatalf("Rare(stll) = %v, %v; want still", hint.Alternatives, rare)
	}
}

// A rare word with nothing much commoner one slip away is left alone (names, jargon).
func TestRareLeavesARareWordWithNoCommonNeighbourAlone(t *testing.T) {
	t.Parallel()

	if hint, rare := latinRarity(t).Rare("rarer", 100); rare {
		t.Errorf("Rare(rarer) = %v, true; want no flag", hint.Alternatives)
	}
}

func TestRareLeavesCommonWordsAlone(t *testing.T) {
	t.Parallel()

	r := latinRarity(t)
	for _, w := range []string{"the", "house", "still"} {
		if hint, rare := r.Rare(w, 100); rare {
			t.Errorf("Rare(%q) = %v, true; want no flag", w, hint.Alternatives)
		}
	}
}

// A higher ratio is quieter.
func TestRareRatioDecidesHowLoudItIs(t *testing.T) {
	t.Parallel()

	r := latinRarity(t)
	// `tie` at 20,000 is 500× `hte` at 30 — flagged at 100, silent at 1000.
	if _, rare := r.Rare("hte", 100); !rare {
		t.Error("Rare(hte) at ratio 100 = false; want a flag")
	}
	if hint, rare := r.Rare("hte", 100000); rare {
		t.Errorf("Rare(hte) at ratio 100000 = %v, true; want silence", hint.Alternatives)
	}
	// A ratio of zero or less turns hints off.
	if _, rare := r.Rare("hte", 0); rare {
		t.Error("Rare(hte) at ratio 0 = true; want off")
	}
}

// An absolute floor under the neighbor, without which one rare word suggests another.
func TestRareIgnoresANeighbourNobodyUsesEither(t *testing.T) {
	t.Parallel()

	// `rarely` (400) is under the floor, however much commoner than a word seen twice.
	r := NewRarity(testFreq(t, "en_US", latinList+"rarel 2\n"), parseAffix("TRY etaoinshrdlu\n"))
	if hint, rare := r.Rare("rarel", 100); rare {
		t.Errorf("Rare(rarel) = %v, true; want silence — the neighbor is below the floor", hint.Alternatives)
	}
}

// A word and the same word with a clitic glued on are both legal, not a slip.
func TestRareExcludesCliticPrefixesForHebrew(t *testing.T) {
	t.Parallel()

	const list = "בבירור 50000\nבירור 90\n"
	r := NewRarity(testFreq(t, "he_IL", list), parseAffix("TRY אבגדהוזחטיכלמנסעפצקרשת\n"))
	if hint, rare := r.Rare("בירור", 100); rare {
		t.Errorf("Rare(בירור) = %v, true; want silence — בבירור is ב + the word", hint.Alternatives)
	}
	// The same shape in a language with no clitic set is an ordinary missing letter.
	en := NewRarity(testFreq(t, "en_US", "band 50000\nand 900000\n"), parseAffix("TRY bandetc\n"))
	if _, rare := en.Rare("nd", 100); !rare {
		t.Error("Rare(nd) in English = false; want a flag — nothing excludes a prefix there")
	}
}

func TestRareOffersAtMostThreeCommonestFirst(t *testing.T) {
	t.Parallel()

	// Four neighbors of `at` over the floor: the three commonest come back, in order.
	const list = "at 3\nact 90000\nbat 80000\ncat 70000\neat 60000\n"
	r := NewRarity(testFreq(t, "en_US", list), parseAffix("TRY abcet\n"))
	hint, rare := r.Rare("at", 100)
	if !rare {
		t.Fatal("Rare(at) = false; want a flag")
	}
	if strings.Join(hint.Alternatives, " ") != "act bat cat" {
		t.Errorf("alternatives = %v, want [act bat cat]", hint.Alternatives)
	}
}

func TestRareIsSilentWithoutData(t *testing.T) {
	t.Parallel()

	var nilRarity *Rarity
	if _, rare := nilRarity.Rare("anything", 100); rare {
		t.Error("a nil Rarity flagged a word")
	}
	if got := nilRarity.Tag(); got != "" {
		t.Errorf("Tag of a nil Rarity = %q", got)
	}
	if got := nilRarity.Script(); got != Other {
		t.Errorf("Script of a nil Rarity = %v", got)
	}
	if NewRarity(nil, Affix{}) != nil {
		t.Error("NewRarity with no list did not return nil")
	}
	if _, rare := latinRarity(t).Rare("", 100); rare {
		t.Error("the empty word was flagged")
	}
}

func TestParseAffixReadsTryAndMap(t *testing.T) {
	t.Parallel()

	const aff = "SET UTF-8\n" +
		"TRY abc\n" +
		"MAP 3\n" +
		"MAP כק\n" +
		"MAP אע # for English\n" +
		"MAP ß(ss)\n" +
		"SFX A Y 1\n"
	got := parseAffix(aff)
	if string(got.Try) != "abc" {
		t.Errorf("Try = %q, want abc", string(got.Try))
	}
	if len(got.Map) != 3 {
		t.Fatalf("Map = %v, want three sets — the count line is not one of them", got.Map)
	}
	// The comment is not part of the set, and a parenthesised run is one member.
	if strings.Join(got.Map[1], "|") != "א|ע" {
		t.Errorf("Map[1] = %v, want [א ע]", got.Map[1])
	}
	if strings.Join(got.Map[2], "|") != "ß|ss" {
		t.Errorf("Map[2] = %v, want [ß ss]", got.Map[2])
	}
}

func TestAffixScriptComesFromTheAlphabetItDeclares(t *testing.T) {
	t.Parallel()

	for aff, want := range map[string]Script{
		"TRY אבגדהוזחט'\"\n": Hebrew,
		"TRY esianrtolc'\n":  Latin,
		"TRY иаоентрвсйл\n":  Cyrillic,
		"SET UTF-8\n":        Other,
	} {
		if got := parseAffix(aff).Script(); got != want {
			t.Errorf("%q: script = %v, want %v", aff, got, want)
		}
	}
	// With no TRY line, the script comes from the list's words.
	r := NewRarity(testFreq(t, "he_IL", "שלום 500\nתודה 900\n"), parseAffix("SET UTF-8\n"))
	if got := r.Script(); got != Hebrew {
		t.Errorf("script from the list = %v, want Hebrew", got)
	}
}

// Certain requires both an unseen typed word and exactly one candidate.
func TestRareIsCertainOnlyWhenTheWordIsUnknownAndTheAnswerIsSingle(t *testing.T) {
	t.Parallel()

	// `hte` is in the list (30), so its one candidate is only a guess.
	if hint, rare := latinRarity(t).Rare("hte", 100); !rare || hint.Certain {
		t.Errorf("Rare(hte) certain=%v; a word the corpus has seen is never certain", hint.Certain)
	}
	// `hosue` is not in the list at all, and only `house` is one slip away.
	hint, rare := latinRarity(t).Rare("hosue", 100)
	if !rare || !hint.Certain || len(hint.Alternatives) != 1 || hint.Alternatives[0] != "house" {
		t.Errorf("Rare(hosue) = %+v, %v; want a certain single answer", hint, rare)
	}
	// Unknown, but with a choice to make: not certain.
	r := NewRarity(testFreq(t, "en_US", "act 90000\nbat 80000\n"), parseAffix("TRY abct\n"))
	if hint, rare := r.Rare("at", 100); !rare || hint.Certain {
		t.Errorf("Rare(at) = %+v, %v; two candidates cannot be certain", hint, rare)
	}
}

// A dismissed word is recorded and survives a restart.
func TestWordFileRemembersAcrossReload(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rare-ok.txt")
	a := LoadWordFile(path)
	if a.Has("שגיא") {
		t.Fatal("an empty list knows a word")
	}
	if err := a.Add("שגיא"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Adding twice writes one line.
	if err := a.Add("שגיא"); err != nil {
		t.Fatalf("Add again: %v", err)
	}
	again := LoadWordFile(path)
	if !again.Has("שגיא") || again.Len() != 1 {
		t.Errorf("reloaded list = %d words, has=%v; want the one word", again.Len(), again.Has("שגיא"))
	}
	body, err := os.ReadFile(path)
	if err != nil || strings.Count(string(body), "\n") != 1 {
		t.Errorf("file = %q, %v; want one line", body, err)
	}
}

// A missing file and a nil list are both empty.
func TestWordFileToleratesNothing(t *testing.T) {
	t.Parallel()

	empty := LoadWordFile(filepath.Join(t.TempDir(), "none.txt"))
	if empty.Has("x") || empty.Len() != 0 {
		t.Error("a missing file did not read as empty")
	}
	var nilList *WordFile
	if nilList.Has("x") || nilList.Len() != 0 {
		t.Error("a nil list answered yes")
	}
	if err := nilList.Add("x"); err != nil {
		t.Errorf("adding to a nil list = %v, want silence", err)
	}
	// Comments and blank lines are skipped, so the file can say what it is.
	path := filepath.Join(t.TempDir(), "rare-ok.txt")
	if err := os.WriteFile(path, []byte("# words you said were fine\n\nשגיא\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := LoadWordFile(path); got.Len() != 1 || !got.Has("שגיא") {
		t.Errorf("list = %d words, want the one that is not a comment", got.Len())
	}
}

// Remember records without writing: hunspell owns personal.dic and appends duplicates.
func TestWordFileRemembersWithoutWriting(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "personal.dic")
	if err := os.WriteFile(path, []byte("Shtoka\nJira\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := LoadWordFile(path)
	if !f.Has("Jira") || f.Has("md") {
		t.Fatal("the file was not read as it stands")
	}
	f.Remember("md")
	if !f.Has("md") {
		t.Error("a remembered word is not known")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "Shtoka\nJira\n" {
		t.Errorf("file = %q, %v; Remember must not write — hunspell owns this one", body, err)
	}
}
