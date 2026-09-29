package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// candidates is a detector answer: mostly English, a third Hebrew, a sixth Russian.
func candidates() []domain.LanguageCandidate {
	return []domain.LanguageCandidate{
		{Tag: "en_US", Script: "Latin", Words: 102677, Share: 0.472, Bytes: 555 << 10},
		{Tag: "he_IL", Script: "Hebrew", Words: 78074, Share: 0.359, Bytes: 7875 << 10},
		{Tag: "ru_RU", Script: "Cyrillic", Words: 36592, Share: 0.168, Bytes: 3544 << 10},
	}
}

// suggest is what the backend would have returned for this account.
func suggest(cs ...domain.LanguageCandidate) dictionaryOfferMsg {
	return dictionaryOfferMsg{suggestion: domain.SpellSuggestion{Candidates: cs}}
}

func offered(t *testing.T, m Model) []string {
	t.Helper()
	out := make([]string, 0, len(m.picker.items))
	for _, item := range m.picker.items {
		out = append(out, item.value)
	}
	return out
}

// The offer lists what the corpus found, every row ticked.
func TestDictionaryOfferTicksWhatTheCorpusFound(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleDictionaryOffer(suggest(candidates()...))
	m = mdl

	if !m.picker.active() {
		t.Fatal("no chooser was raised for three detected languages")
	}
	got := offered(t, m)
	if len(got) != 3 {
		t.Fatalf("offered %v, want all three", got)
	}
	for _, tag := range got {
		if !m.picker.ticked(tag) {
			t.Errorf("%s is not ticked; the detector already decided it is worth having", tag)
		}
	}
}

// Each row carries its evidence.
func TestDictionaryOfferShowsItsReasoning(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleDictionaryOffer(suggest(candidates()...))
	m = mdl

	for _, item := range m.picker.items {
		if item.value != "he_IL" {
			continue
		}
		for _, want := range []string{"Hebrew", "36%", "MB"} {
			if !strings.Contains(item.detail, want) {
				t.Errorf("detail = %q, want it to mention %q", item.detail, want)
			}
		}
		return
	}
	t.Error("he_IL was not offered")
}

// An empty answer raises nothing.
func TestDictionaryOfferStaysQuietWithNothingToSay(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleDictionaryOffer(dictionaryOfferMsg{})
	m = mdl
	if m.picker.active() {
		t.Error("a chooser was raised for no candidates")
	}
}

// Asked once per session, however many times the room list arrives.
func TestDictionaryOfferIsMadeOnce(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	first, _ := m.maybeOfferDictionaries()
	if !first.offers.dictionaries.asked {
		t.Fatal("the first room list should have prompted the question")
	}
	if _, cmd := first.maybeOfferDictionaries(); cmd != nil {
		t.Error("the question was asked twice in one session")
	}
}

// A cold start with no corpus must not consume the one question.
func TestDictionaryOfferWaitsForACorpus(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel()) // no rooms yet
	next, cmd := m.maybeOfferDictionaries()
	if cmd != nil {
		t.Error("asked before the cache had anything in it")
	}
	if next.offers.dictionaries.asked {
		t.Error("a cold start consumed the one question of the session")
	}
}

// Unticking everything installs nothing and says nothing.
func TestInstallingNothingSaysNothing(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = m.clearStatus()
	mdl, cmd := m.installDictionaries(nil)
	next := mdl

	if cmd != nil {
		t.Error("an empty selection started a download")
	}
	if next.status() != "" && strings.Contains(next.status(), "install") {
		t.Errorf("status = %q, want silence for a deliberate no", next.status())
	}
}

// A checksum failure is reported as a refusal.
func TestFailedInstallIsReportedAsARefusal(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleDictionariesInstalled(dictionariesInstalledMsg{failed: []string{"he_IL"}})
	next := mdl

	got := next.status()
	if !strings.Contains(got, "verified") || !strings.Contains(got, "he_IL") {
		t.Errorf("status = %q, want it to name what was refused and why", got)
	}
}

// Unticking a row and accepting records the language as declined.
func TestDecliningALanguageIsRemembered(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleDictionaryOffer(suggest(candidates()...))
	m = mdl
	if len(m.offers.dictionaries.shown) != 3 {
		t.Fatalf("offered %v, want all three", m.offers.dictionaries.shown)
	}

	mdl, _ = m.acceptDictionaryOffer([]string{"en_US", "he_IL"})
	next := mdl

	if !next.conf.base.Spell.SpellDeclined("ru_RU") {
		t.Error("ru_RU was unticked and accepted, and was not recorded as declined")
	}
	for _, taken := range []string{"en_US", "he_IL"} {
		if next.conf.base.Spell.SpellDeclined(taken) {
			t.Errorf("%s was ticked and should not be declined", taken)
		}
	}
}

// A declined language is never offered again.
func TestADeclinedLanguageIsNotOfferedAgain(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.conf.base.Spell.Declined = []string{"ru_RU"}

	mdl, _ := m.handleDictionaryOffer(suggest(candidates()...))
	next := mdl

	got := offered(t, next)
	for _, tag := range got {
		if tag == "ru_RU" {
			t.Errorf("offered %v — ru_RU was declined and must not come back", got)
		}
	}
	if len(got) != 2 {
		t.Errorf("offered %v, want the two that were not declined", got)
	}
}

// With every language declined no prompt is raised.
func TestAllDeclinedRaisesNothing(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.conf.base.Spell.Declined = []string{"en_US", "he_IL", "ru_RU"}

	mdl, _ := m.handleDictionaryOffer(suggest(candidates()...))
	next := mdl
	if next.picker.active() {
		t.Error("a chooser was raised with every row already declined")
	}
}

// freqCandidates: Hebrew measured as needing word counts, English as not.
func freqCandidates() []domain.FrequencyCandidate {
	return []domain.FrequencyCandidate{
		{
			Tag: "he_IL", Script: "Hebrew", Share: 0.359, Accepts: 0.23, Recommended: true,
			Bytes: 18 << 20, Disk: 7 << 20, Words: 455962,
		},
		{
			Tag: "en_US", Script: "Latin", Share: 0.472, Accepts: 0.08,
			Bytes: 19 << 20, Disk: 6 << 20, Words: 538513,
		},
	}
}

// The word-counts prompt is raised when no dictionaries are missing, ticking only
// the recommended rows.
func TestFrequencyOfferIsRaisedWhenNoDictionariesAreMissing(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	msg := dictionaryOfferMsg{suggestion: domain.SpellSuggestion{Frequencies: freqCandidates()}}
	mdl, _ := m.handleDictionaryOffer(msg)
	m = mdl

	if !m.picker.active() || m.picker.kind != pickerFrequencies {
		t.Fatalf("no word-counts prompt was raised: kind %v, active %v", m.picker.kind, m.picker.active())
	}
	if !m.picker.ticked("he_IL") {
		t.Error("he_IL is not ticked; it is the measured case the feature exists for")
	}
	if m.picker.ticked("en_US") {
		t.Error("en_US is ticked; 19 MB for a dictionary that rarely needs it is not a recommendation")
	}
	if got := offered(t, m); len(got) != 2 {
		t.Errorf("offered %v, want both — a row nobody can see is not an offer", got)
	}
}

// Every word-counts row shows its costs and evidence.
func TestFrequencyOfferShowsBothCostsAndTheEvidence(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleDictionaryOffer(
		dictionaryOfferMsg{suggestion: domain.SpellSuggestion{Frequencies: freqCandidates()}})
	m = mdl

	var detail string
	for _, item := range m.picker.items {
		if item.value == "he_IL" {
			detail = item.detail
		}
	}
	for _, want := range []string{"Hebrew", "36%", "accepts 23%", "18.0 MB", "7.0 MB kept"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the row %q does not say %q", detail, want)
		}
	}
}

// Declining word counts is recorded separately from dictionaries, and not re-asked.
func TestDecliningCountsIsRememberedSeparately(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleDictionaryOffer(
		dictionaryOfferMsg{suggestion: domain.SpellSuggestion{Frequencies: freqCandidates()}})
	m = mdl

	next, _ := m.acceptFrequencyOffer([]string{"he_IL"})
	m = next
	if !m.conf.base.Spell.FrequenciesDeclinedFor("en_US") {
		t.Error("the unticked language was not recorded as declined")
	}
	if m.conf.base.Spell.SpellDeclined("en_US") {
		t.Error("declining the counts also declined the dictionary; they are separate answers")
	}

	again, _ := m.handleDictionaryOffer(
		dictionaryOfferMsg{suggestion: domain.SpellSuggestion{Frequencies: freqCandidates()}})
	m = again
	if got := offered(t, m); len(got) != 1 || got[0] != "he_IL" {
		t.Errorf("offered %v after a refusal; no means no", got)
	}
}

// With no frequency lists to offer, accepting the dictionaries goes on to the model
// offer, and its lookup runs: it is marked asked, so a dropped lookup never comes back.
func TestAcceptingDictionariesGoesOnToTheModelOffer(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleDictionaryOffer(suggest(candidates()...))
	next, cmd := mdl.acceptDictionaryOffer([]string{"en_US"})
	if !next.offers.model.asked {
		t.Fatal("the model offer was not reached")
	}
	for _, msg := range msgsOf(t, cmd) {
		if _, ok := msg.(modelOfferMsg); ok {
			return
		}
	}
	t.Fatal("the model offer was marked asked but its lookup never ran")
}
