package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// modelSuggestion is the daemon's answer for a machine with llama.cpp and no weights.
func modelSuggestion() modelOfferMsg {
	return modelOfferMsg{suggestion: domain.ModelSuggestion{
		Offer: true,
		Candidate: domain.ModelCandidate{
			Tag: "smollm2-360m", Name: "SmolLM2 360M Instruct (Q8_0)",
			Bytes: 386404992, Recall: 0.339,
		},
	}}
}

// The row starts ticked and carries its own evidence.
func TestModelOfferShowsWhatItIsWorth(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleModelOffer(modelSuggestion())
	m = mdl

	if !m.picker.active() {
		t.Fatal("no prompt was raised for a machine with nothing to complete with")
	}
	if got := offered(t, m); len(got) != 1 || got[0] != "smollm2-360m" {
		t.Fatalf("offered %v, want the one model", got)
	}
	if !m.picker.checked["smollm2-360m"] {
		t.Error("the row starts unticked, so the measurement has to be re-done by hand")
	}
	detail := m.picker.items[0].detail
	for _, want := range []string{"34%", "369 MB", "runs here"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the row does not say %q:\n%s", want, detail)
		}
	}
}

func TestModelOfferStaysQuietWithNothingToOffer(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleModelOffer(modelOfferMsg{})
	m = mdl
	if m.picker.active() {
		t.Error("a prompt was raised for a machine that has nothing to install")
	}
}

// Without llama.cpp there is nothing to offer, only the install command to name.
func TestModelOfferNamesTheMissingProgramInsteadOfOfferingWeights(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleModelOffer(modelOfferMsg{suggestion: domain.ModelSuggestion{
		Why: "llama-server is not installed, and the local completion model needs it — Arch: pacman -S llama.cpp",
	}})
	m = mdl

	if m.picker.active() {
		t.Error("offered a download for a program that cannot run it")
	}
	if !strings.Contains(m.status(), "pacman") {
		t.Errorf("the status does not name what to install:\n%s", m.status())
	}
}

// Unticking the row and accepting declines for good.
func TestUntickingTheRowDeclinesItForGood(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, _ := m.handleModelOffer(modelSuggestion())
	m = mdl

	mdl, _ = m.acceptModelOffer(nil) // everything unticked
	m = mdl
	if !m.conf.base.Complete.Model.Declined {
		t.Error("unticking the row did not record the no, so it will be asked again")
	}
	if m.picker.active() {
		t.Error("the prompt is still up after being answered")
	}
}

// A recorded no, or the layer switched off in config, means nothing is asked.
func TestNothingIsOfferedWhenDeclinedOrOff(t *testing.T) {
	t.Parallel()

	off := false
	for name, set := range map[string]func(*Model){
		"declined":       func(m *Model) { m.conf.base.Complete.Model.Declined = true },
		"model off":      func(m *Model) { m.conf.base.Complete.Model.Enabled = &off },
		"completion off": func(m *Model) { m.conf.base.Complete.Enabled = &off },
	} {
		m := sized(t, withRooms(t, newModel()))
		set(&m)
		if _, cmd := m.maybeOfferModel(); cmd != nil {
			t.Errorf("%s: a model was offered", name)
		}
	}
}

// Asked once per session, however many times the room list arrives.
func TestModelOfferIsMadeOnce(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	first, cmd := m.maybeOfferModel()
	if cmd == nil || !first.offers.model.asked {
		t.Fatal("the first opportunity did not ask")
	}
	if _, again := first.maybeOfferModel(); again != nil {
		t.Error("the question was asked twice in one session")
	}
}

// The dictionary → model prompt chain must reach the model even when spelling has
// nothing to ask.
func TestTheChainReachesTheModelWhenSpellingHasNothingToAsk(t *testing.T) {
	t.Parallel()

	off := false
	m := sized(t, withRooms(t, newModel()))
	m.conf.base.Spell.Enabled = &off
	m.offers.dictionaries.asked = false

	next, cmd := m.maybeOfferDictionaries()
	if cmd == nil {
		t.Fatal("spelling off ended the chain, so the model was never asked about")
	}
	if !next.offers.model.asked {
		t.Error("the chain did not reach the model's question")
	}
}

func TestTheChainReachesTheModelWhenThereIsNoSpellEngine(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	mdl, cmd := m.handleDictionaryOffer(dictionaryOfferMsg{
		suggestion: domain.SpellSuggestion{Why: "hunspell is not installed"},
	})
	next := mdl
	if cmd == nil {
		t.Fatal("a missing spell engine ended the chain")
	}
	if !next.offers.model.asked {
		t.Error("the chain did not reach the model's question")
	}
}

// Once the chain has started, a second room list must not restart it.
func TestASecondRoomListDoesNotOvertakeTheChain(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel())) // the first list started the chain
	if !m.offers.dictionaries.asked {
		t.Fatal("precondition: the chain should have started")
	}
	if _, cmd := m.maybeOfferDictionaries(); cmd != nil {
		t.Error("a second room list raised a question the chain already owns")
	}
}

// modelLayerOn asks "may the model run" from config alone; the assist endpoint is
// unrelated.
func TestTheGateIsTheCompletionModelAlone(t *testing.T) {
	t.Parallel()

	m := newModel()
	if !m.modelLayerOn() {
		t.Error("the completion model defaults to off, so nothing would ever ask")
	}
	m.conf.base.Complete.Model.Declined = true
	if m.modelLayerOn() {
		t.Error("a declined model still arms the asker")
	}
	m.conf.base.Assist.Endpoint = "https://example.org/v1/chat/completions"
	if m.modelLayerOn() {
		t.Error("configuring the assist endpoint armed the completion asker")
	}
}

// The model takes four of the five slots; the room's own vocabulary keeps one.
func TestTheModelLeadsAndTheLadderKeepsOneSlot(t *testing.T) {
	t.Parallel()

	got := merge([]string{"weather", "data", "status", "availability"}, []string{"logs"})
	if want := []string{"weather", "data", "status", "availability", "logs"}; !slices.Equal(got, want) {
		t.Errorf("strip = %v, want %v", got, want)
	}
}

// Duplicates, case-insensitively, are offered once.
func TestTheStripDoesNotOfferTheSameWordTwice(t *testing.T) {
	t.Parallel()

	got := merge([]string{"The", "soon"}, []string{"the", "later", "soon", "now", "then"})
	if want := []string{"The", "soon", "later", "now", "then"}; !slices.Equal(got, want) {
		t.Errorf("merge = %v, want %v", got, want)
	}
	if len(got) > domain.MaxCompletionOptions {
		t.Errorf("merge returned %d options, more than the strip can show", len(got))
	}
}

// A held space key is the same question, so no debounce is needed; the ask resolves
// to the tick message directly.
func TestThereIsNoWaitBeforeAsking(t *testing.T) {
	t.Parallel()

	if !sameAsk("hello ", "hello   ") {
		t.Error("a held space key reads as a new question, so each repeat is a request")
	}
	if sameAsk("hello ", "hello there ") {
		t.Error("a different draft reads as the same question, so the strip would go stale")
	}
	if cmd := modelAskNow(7); cmd == nil {
		t.Fatal("no ask was scheduled")
	} else if msg, ok := cmd().(modelTickMsg); !ok || msg.gen != 7 {
		t.Errorf("modelAskNow produced %#v, want the tick for generation 7", msg)
	}
}
