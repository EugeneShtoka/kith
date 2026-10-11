package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// `setup` validates needs against this vocabulary, so a typo is refused at startup.
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
		// A count on a need that takes none is refused.
		{entry: "url:3"},
		// A need that takes a count requires one.
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

// A need round-trips through String.
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

// Empty is the default, not an error.
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
