package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An entry names a room by ID, by displayed name, or by a space it is in — one idea,
// one spelling, shared with notification rules.
func TestCodeScopeMatchesThreeWays(t *testing.T) {
	t.Parallel()

	sms := domain.RoomFacts{
		ID: "!sms:x", Name: "Google Messages",
		Spaces: []string{"Bridges"}, Protocol: domain.ProtocolWhatsApp,
	}
	other := domain.RoomFacts{ID: "!work:x", Name: "Standup", Spaces: []string{"Work"}}
	// Every way an entry can name the room: ID, `room:` name, space, and network.
	for _, entry := range []string{
		"!sms:x", "room:Google Messages", "space:Bridges", "protocol:WhatsApp",
	} {
		only := domain.CodeScope{Include: []string{entry}}
		if !only.Admits(sms) {
			t.Errorf("include %q should admit the room it names", entry)
		}
		if only.Admits(other) {
			t.Errorf("include %q should admit nothing else", entry)
		}
		blocked := domain.CodeScope{Exclude: []string{entry}}
		if blocked.Admits(sms) {
			t.Errorf("exclude %q should block the room it names", entry)
		}
	}
	// A bare word that is not a room ID declares no kind, so it names nothing — which
	// is what makes a mistyped entry a refusal at startup rather than a silent miss.
	if (domain.CodeScope{Include: []string{"Bridges"}}).Admits(sms) {
		t.Error(`a bare "Bridges" matched; it has to be written space:Bridges`)
	}
}

// No lists means everywhere: the feature works before anyone configures it.
func TestCodeScopeEmptyMeansEverywhere(t *testing.T) {
	t.Parallel()

	if !(domain.CodeScope{}).Admits(domain.RoomFacts{ID: "!a:x", Name: "Anything"}) {
		t.Error("an unconfigured scope should admit every room")
	}
	if !(domain.CodeScope{}).Everywhere() {
		t.Error("an empty include list is what Everywhere reports")
	}
	if (domain.CodeScope{Include: []string{"Bridges"}}).Everywhere() {
		t.Error("a named place is not everywhere")
	}
}

// Exclude wins. The reason to write one is to stop something happening, and a rule
// that a broader rule can override does not stop it.
func TestCodeScopeExcludeWins(t *testing.T) {
	t.Parallel()

	scope := domain.CodeScope{Include: []string{"space:Bridges"}, Exclude: []string{"!noisy:x"}}
	if !scope.Admits(domain.RoomFacts{ID: "!sms:x", Name: "SMS", Spaces: []string{"Bridges"}}) {
		t.Error("an included room should still be admitted")
	}
	if scope.Admits(domain.RoomFacts{ID: "!noisy:x", Name: "Noisy", Spaces: []string{"Bridges"}}) {
		t.Error("exclude should beat include for the same room")
	}
}
