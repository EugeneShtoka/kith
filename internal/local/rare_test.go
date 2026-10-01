package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/spell"
)

// Rare-word hints (engine accepts, frequency list disagrees), tested through the
// backend to cover the wiring rather than spell.Rarity itself.

// rareList: `the` is common, `hte` a real transposition, `rarer` rare with no
// common neighbor.
const rareList = "at 300000\nhte 30\nrarer 4\nthe 500000\n"

// rareBackend wires a stub engine accepting every word, a Latin dictionary, and
// (if withList) a frequency list.
func rareBackend(t *testing.T, set SpellSettings, withList bool, extraTags ...string) *Service {
	t.Helper()
	dir := stubEngine(t, nil)
	// The TRY alphabet says which script a dictionary is for.
	write(t, filepath.Join(dir, "en_US.aff"), "SET UTF-8\nTRY etaoinshrdlu\n")
	for _, tag := range extraTags {
		write(t, filepath.Join(dir, tag+".aff"), "SET UTF-8\nTRY etaoinshrdlu\n")
		write(t, filepath.Join(dir, tag+".dic"), "0\n")
	}

	freqs := t.TempDir()
	if withList {
		write(t, filepath.Join(freqs, "en_US.freq"), "# a test list\n"+rareList)
	}

	set.Enabled = true
	set.Command = filepath.Join(dir, "stubhunspell")
	b := New(nil, nil)
	b.UseSpell(set)
	b.spell.dirs = []string{dir}
	b.spell.freqs = freqs
	t.Cleanup(b.spell.stop)
	return b
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestCheckSpellingFlagsAWordTheEngineAccepted(t *testing.T) {
	t.Parallel()

	b := rareBackend(t, SpellSettings{FlagRare: true}, true)
	found, err := b.CheckSpelling(t.Context(), "at hte end")
	if err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("found %+v, want one hint", found)
	}
	want := domain.Misspelling{
		Word: "hte", Start: 3, End: 6, Suggestions: []string{"the"}, Rare: true,
	}
	if found[0].Word != want.Word || found[0].Start != want.Start || found[0].End != want.End {
		t.Errorf("hint = %+v, want %+v", found[0], want)
	}
	if !found[0].Rare {
		t.Error("the hint is not marked Rare, so the composer would draw it as a misspelling")
	}
	if len(found[0].Suggestions) == 0 || found[0].Suggestions[0] != "the" {
		t.Errorf("suggestions = %v, want the", found[0].Suggestions)
	}
}

// Off by default, even with a list installed.
func TestCheckSpellingAsksNothingRareUnlessItIsTurnedOn(t *testing.T) {
	t.Parallel()

	b := rareBackend(t, SpellSettings{}, true)
	found, err := b.CheckSpelling(t.Context(), "at hte end")
	if err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("found %+v with the setting off, want nothing", found)
	}
}

func TestCheckSpellingIsSilentWithoutAList(t *testing.T) {
	t.Parallel()

	b := rareBackend(t, SpellSettings{FlagRare: true}, false)
	found, err := b.CheckSpelling(t.Context(), "at hte end")
	if err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("found %+v with no list installed, want nothing", found)
	}
}

// Two dictionaries of one script: the language is ambiguous, so no hint.
func TestCheckSpellingRefusesToGuessBetweenTwoDictionariesOfOneScript(t *testing.T) {
	t.Parallel()

	b := rareBackend(t, SpellSettings{FlagRare: true}, true, "de_DE")
	found, err := b.CheckSpelling(t.Context(), "at hte end")
	if err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("found %+v with two Latin dictionaries loaded, want nothing", found)
	}
}

func TestCheckSpellingHonoursTheConfiguredRatio(t *testing.T) {
	t.Parallel()

	// `the` is 16,666× `hte`; a higher ratio is quieter.
	quiet := rareBackend(t, SpellSettings{FlagRare: true, RareRatio: 100000}, true)
	found, err := quiet.CheckSpelling(t.Context(), "at hte end")
	if err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("found %+v at ratio 100000, want silence", found)
	}

	// Zero means "not configured": the default applies.
	unset := rareBackend(t, SpellSettings{FlagRare: true}, true)
	if found, err = unset.CheckSpelling(t.Context(), "at hte end"); err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(found) != 1 {
		t.Errorf("found %+v with no ratio configured, want the default's one hint", found)
	}
}

// A rare word with no common neighbor one slip away (names, jargon) is left alone.
func TestCheckSpellingLeavesARareWordWithNoNeighbourAlone(t *testing.T) {
	t.Parallel()

	b := rareBackend(t, SpellSettings{FlagRare: true}, true)
	found, err := b.CheckSpelling(t.Context(), "rarer")
	if err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("found %+v, want nothing — nothing common is one slip from it", found)
	}
}

// A word the engine rejected is a misspelling, not marked Rare.
func TestCheckSpellingStillReportsRealMisspellingsUnmarked(t *testing.T) {
	t.Parallel()

	dir := stubEngine(t, map[string]string{typoReceive: wrongReply})
	write(t, filepath.Join(dir, "en_US.aff"), "SET UTF-8\nTRY etaoinshrdlu\n")
	freqs := t.TempDir()
	write(t, filepath.Join(freqs, "en_US.freq"), rareList)

	b := New(nil, nil)
	b.UseSpell(SpellSettings{
		Enabled: true, Command: filepath.Join(dir, "stubhunspell"), FlagRare: true,
	})
	b.spell.dirs = []string{dir}
	b.spell.freqs = freqs
	t.Cleanup(b.spell.stop)

	found, err := b.CheckSpelling(t.Context(), typoReceive)
	if err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(found) != 1 || found[0].Rare {
		t.Fatalf("found %+v, want one plain misspelling", found)
	}
	if len(found[0].Suggestions) == 0 || found[0].Suggestions[0] != "receive" {
		t.Errorf("suggestions = %v, want the engine's own", found[0].Suggestions)
	}
}

// A dismissed word never comes back, including after a restart.
func TestCheckSpellingRespectsTheAllowList(t *testing.T) {
	t.Parallel()

	b := rareBackend(t, SpellSettings{FlagRare: true}, true)
	if found, err := b.CheckSpelling(t.Context(), "at hte end"); err != nil || len(found) != 1 {
		t.Fatalf("CheckSpelling = %+v, %v; want the hint before it is dismissed", found, err)
	}
	if err := b.AllowRareWord(t.Context(), "hte"); err != nil {
		t.Fatalf("AllowRareWord: %v", err)
	}
	found, err := b.CheckSpelling(t.Context(), "at hte end")
	if err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("found %+v after the word was dismissed", found)
	}
	body, rerr := os.ReadFile(filepath.Join(b.spell.lookIn()[0], "rare-ok.txt"))
	if rerr != nil || !strings.Contains(string(body), "hte") {
		t.Errorf("rare-ok.txt = %q, %v; want the dismissed word", body, rerr)
	}
}

// Certain is set only when the typed word is absent from the corpus.
func TestCheckSpellingMarksTheCertainCase(t *testing.T) {
	t.Parallel()

	// `hte` is in the list (30), so its candidate is a guess.
	b := rareBackend(t, SpellSettings{FlagRare: true}, true)
	found, err := b.CheckSpelling(t.Context(), "at hte end")
	if err != nil || len(found) != 1 {
		t.Fatalf("CheckSpelling = %+v, %v", found, err)
	}
	if found[0].Certain {
		t.Error("a word the corpus has seen came back Certain")
	}

	found, err = b.CheckSpelling(t.Context(), "at th end")
	if err != nil || len(found) != 1 {
		t.Fatalf("CheckSpelling = %+v, %v", found, err)
	}
	if !found[0].Certain || len(found[0].Suggestions) != 1 {
		t.Errorf("hint = %+v, want one certain answer", found[0])
	}
}

// Counts are offered only when the check is on, for installed dictionaries, where
// counts exist and are not yet installed.
func TestDetectLanguagesOffersCountsForWhatIsInstalled(t *testing.T) {
	t.Parallel()

	b := rareBackend(t, SpellSettings{FlagRare: true}, false)
	if _, err := b.CheckSpelling(t.Context(), "anything"); err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	avail := scanned(&b.spell)
	got := b.frequencyCandidates(avail, map[string]float64{"en_US": 0.5})
	if len(got) != 1 || got[0].Tag != "en_US" {
		t.Fatalf("offered %+v, want the installed dictionary's counts", got)
	}
	c := got[0]
	if c.Bytes <= c.Disk || c.Words == 0 {
		t.Errorf("row = %+v, want both costs and the word count", c)
	}
	// English is measured and does not need counts: shown unticked.
	if c.Recommended || c.Accepts == 0 {
		t.Errorf("en_US = recommended %v at %v; want offered but not recommended", c.Recommended, c.Accepts)
	}
	if c.Share != 0.5 {
		t.Errorf("share = %v, want what the corpus said", c.Share)
	}
}

func TestDetectLanguagesOffersNoCountsWhenTheCheckIsOff(t *testing.T) {
	t.Parallel()

	b := rareBackend(t, SpellSettings{}, false)
	if _, err := b.CheckSpelling(t.Context(), "anything"); err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	avail := scanned(&b.spell)
	if got := b.frequencyCandidates(avail, nil); len(got) != 0 {
		t.Errorf("offered %+v with the check off; nobody asked for it", got)
	}
}

func TestDetectLanguagesSkipsAnInstalledList(t *testing.T) {
	t.Parallel()

	b := rareBackend(t, SpellSettings{FlagRare: true}, true)
	if _, err := b.CheckSpelling(t.Context(), "anything"); err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	avail := scanned(&b.spell)
	if got := b.frequencyCandidates(avail, nil); len(got) != 0 {
		t.Errorf("offered %+v for a language that already has its counts", got)
	}
}

// Installed counts take effect without a restart.
func TestInstallFrequenciesReloadsWithoutARestart(t *testing.T) {
	t.Parallel()

	b := rareBackend(t, SpellSettings{FlagRare: true}, false)
	if found, err := b.CheckSpelling(t.Context(), "at hte end"); err != nil || len(found) != 0 {
		t.Fatalf("CheckSpelling = %+v, %v; want silence with no list", found, err)
	}
	// Stand in for the download, then reload the way the installer does.
	write(t, filepath.Join(b.spell.freqDir(), "en_US.freq"), rareList)
	b.spell.reloadRarity()

	found, err := b.CheckSpelling(t.Context(), "at hte end")
	if err != nil {
		t.Fatalf("CheckSpelling: %v", err)
	}
	if len(found) != 1 || !found[0].Rare {
		t.Errorf("found %+v after the list landed, want the hint — no restart needed", found)
	}
}

// scanned is the dictionary scan the engine makes, as it makes it.
func scanned(s *spellcheck) spell.Availability {
	s.mu.Lock()
	defer s.mu.Unlock()
	avail, _ := spell.Look(s.settings.Command, s.settings.Dictionaries, s.lookIn())
	return avail
}
