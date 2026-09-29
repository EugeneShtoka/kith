package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The vocabulary is what `setup` refuses against, so what it accepts and rejects is the
// difference between a mistyped need being one sentence at startup and a script that
// silently receives less than it asked for.
func TestTheNeedVocabulary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		entry string
		want  domain.Need
		ok    bool
	}{
		{entry: "message", want: domain.Need{Kind: domain.NeedMessage}, ok: true},
		{entry: "url", want: domain.Need{Kind: domain.NeedURL}, ok: true},
		{entry: " URL ", want: domain.Need{Kind: domain.NeedURL}, ok: true},
		{entry: "history:20", want: domain.Need{Kind: domain.NeedHistory, Count: 20}, ok: true},
		// A count on something that takes none is a misunderstanding worth reporting,
		// not something to accept and quietly ignore.
		{entry: "url:3"},
		// And a need that takes one is not written without it.
		{entry: "history"},
		{entry: "history:0"},
		{entry: "history:501"},
		{entry: "history:lots"},
		{entry: "ulr"},
		{entry: ""},
	} {
		got, ok := domain.ParseNeed(tc.entry)
		if ok != tc.ok {
			t.Errorf("ParseNeed(%q) ok = %v, want %v", tc.entry, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("ParseNeed(%q) = %+v, want %+v", tc.entry, got, tc.want)
		}
	}
}

// A need reads back the way it is written, which is what lets the refusal quote the
// vocabulary rather than describing it.
func TestANeedWritesItselfBack(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{"message", "url", "history:5"} {
		need, ok := domain.ParseNeed(entry)
		if !ok {
			t.Fatalf("ParseNeed(%q) refused it", entry)
		}
		if got := need.String(); got != entry {
			t.Errorf("String() = %q, want %q", got, entry)
		}
	}
}

// Empty is the default and not an error: every command behaved this way before the
// setting existed, and a config that never mentions it must keep behaving that way.
func TestAnUnsetOutputIsTheComposer(t *testing.T) {
	t.Parallel()

	got, ok := domain.ParseScriptOutput("")
	if !ok || got != domain.OutputCompose {
		t.Errorf("ParseScriptOutput(\"\") = %v, %v; want the composer", got, ok)
	}
	if _, ok := domain.ParseScriptOutput("compsoe"); ok {
		t.Error("a misspelled sink was accepted")
	}
	for _, name := range domain.OutputVocabulary() {
		if _, ok := domain.ParseScriptOutput(name); !ok {
			t.Errorf("%q is offered by the vocabulary and refused by the parser", name)
		}
	}
}
