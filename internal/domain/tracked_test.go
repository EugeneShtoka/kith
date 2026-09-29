package domain_test

import (
	"reflect"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The glob is the feature: one list, two languages, and each entry saying how wide
// it wants to be without a global switch deciding for both.
func TestTrackedGlobWidth(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		words []string
		body  string
		want  []string // the matched text of each hit, in order
	}{
		{
			name:  "a bare word is the whole word only",
			words: []string{"deploy"},
			body:  "deploy the deployment, redeploy later",
			want:  []string{"deploy"},
		},
		{
			name:  "a trailing star takes what grows from it",
			words: []string{"deploy*"},
			body:  "deploy the deployment, redeploy later",
			want:  []string{"deploy", "deployment"},
		},
		{
			name:  "stars on both ends go anywhere",
			words: []string{"*deploy*"},
			body:  "deploy the deployment, redeploy later",
			want:  []string{"deploy", "deployment", "redeploy"},
		},
		{
			name:  "a leading star takes what ends with it",
			words: []string{"*deploy"},
			body:  "deploy the deployment, redeploy later",
			want:  []string{"deploy", "redeploy"},
		},
		{
			// The case the glob exists for.
			name:  "hebrew needs the wide form for its own prefixes",
			words: []string{"פריסה"},
			body:  "לפריסה של המערכת",
			want:  nil,
		},
		{
			// And the mark covers the word the sentence actually contains, prefix
			// included — see TestTrackedSpansTheWholeWord for why.
			name:  "and gets it from the stars",
			words: []string{"*פריסה*"},
			body:  "לפריסה של המערכת",
			want:  []string{"לפריסה"},
		},
		{
			name:  "case is folded, always",
			words: []string{"invoice"},
			body:  "Invoice attached. INVOICE #4",
			want:  []string{"Invoice", "INVOICE"},
		},
		{
			name:  "punctuation is a boundary and emoji are too",
			words: []string{"ship"},
			body:  "ship, ship. ship🚀 shipped",
			want:  []string{"ship", "ship", "ship"},
		},
		{
			name:  "two entries may both claim one run",
			words: []string{"invoice", "*voice*"},
			body:  "the invoice",
			want:  []string{"invoice", "invoice"},
		},
		{
			name:  "nothing tracked finds nothing",
			words: nil,
			body:  "deploy",
			want:  nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hits := domain.Tracked{Words: tc.words}.Find(tc.body)
			var got []string
			for _, h := range hits {
				got = append(got, tc.body[h.Start:h.End])
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("hits = %q, want %q", got, tc.want)
			}
		})
	}
}

// A hit carries the entry as configured, not the text that matched it, because the
// list groups by what the user wrote.
func TestTrackedHitNamesTheEntry(t *testing.T) {
	t.Parallel()

	hits := domain.Tracked{Words: []string{"*deploy*"}}.Find("redeployed twice, redeployed again")
	if len(hits) != 2 {
		t.Fatalf("hits = %+v, want two", hits)
	}
	for _, h := range hits {
		if h.Word != "*deploy*" {
			t.Errorf("hit word = %q, want the configured entry", h.Word)
		}
	}
}

// One word is one hit, however many times the needle fits inside it — because the
// hit *is* the word. Two separate words are two hits.
func TestTrackedCountsWordsNotNeedles(t *testing.T) {
	t.Parallel()

	if hits := (domain.Tracked{Words: []string{"*aa*"}}).Find("aaa"); len(hits) != 1 {
		t.Errorf("hits in \"aaa\" = %+v, want one — the span is the word", hits)
	}
	if hits := (domain.Tracked{Words: []string{"*aa*"}}).Find("aaa baa"); len(hits) != 2 {
		t.Errorf("hits in \"aaa baa\" = %+v, want one per word", hits)
	}
}

// The span is the word whatever the glob's width, so the same word underlines
// identically however it was caught.
func TestTrackedSpansTheWholeWord(t *testing.T) {
	t.Parallel()

	const body = "לפריסה של המערכת"
	hits := domain.Tracked{Words: []string{"*פריסה*"}}.Find(body)
	if len(hits) != 1 {
		t.Fatalf("hits = %+v, want one", hits)
	}
	if got := body[hits[0].Start:hits[0].End]; got != "לפריסה" {
		t.Errorf("span = %q, want the whole word including its prefix", got)
	}
}

// Rules combine by union, which is the whole reason they are a filter and not a
// ladder: a narrower rule adds its words, it does not replace the broader one's.
func TestTrackedRulesUnionByScope(t *testing.T) {
	t.Parallel()

	rules := []domain.TrackedRule{
		{Words: []string{"deploy"}}, // everywhere
		{Words: []string{"invoice"}, Where: domain.PlaceFilter{Include: []string{"space:Work"}}},
		{Words: []string{"dinner"}, Where: domain.PlaceFilter{Include: []string{"dm"}}},
	}
	work := domain.RoomFacts{ID: "!w:x", Name: "Standup", Spaces: []string{"Work"}}
	if got := domain.TrackedFor(rules, work, "@dana:x").Words; len(got) != 2 {
		t.Errorf("in Work = %v, want deploy and invoice — a ladder would give one", got)
	}
	dm := domain.RoomFacts{ID: "!d:x", Name: "Dana", Direct: true}
	if got := domain.TrackedFor(rules, dm, "@dana:x").Words; len(got) != 2 {
		t.Errorf("in a DM = %v, want deploy and dinner", got)
	}
	other := domain.RoomFacts{ID: "!o:x", Name: "Noise", Spaces: []string{"Social"}}
	if got := domain.TrackedFor(rules, other, "@dana:x").Words; len(got) != 1 {
		t.Errorf("elsewhere = %v, want only the global word", got)
	}
}

// An except cannot be outranked, and `from` narrows to one person's messages.
func TestTrackedRuleExceptAndFrom(t *testing.T) {
	t.Parallel()

	rules := []domain.TrackedRule{{
		Words: []string{"deploy"},
		Where: domain.PlaceFilter{Include: []string{"space:Work"}, Exclude: []string{"!noise:x"}},
		From:  []string{"@dana:x"},
	}}
	inWork := domain.RoomFacts{ID: "!w:x", Spaces: []string{"Work"}}
	if got := domain.TrackedFor(rules, inWork, "@dana:x").Words; len(got) != 1 {
		t.Errorf("from the named sender = %v, want the word", got)
	}
	if got := domain.TrackedFor(rules, inWork, "@bob:x").Words; len(got) != 0 {
		t.Errorf("from anybody else = %v, want nothing", got)
	}
	excluded := domain.RoomFacts{ID: "!noise:x", Spaces: []string{"Work"}}
	if got := domain.TrackedFor(rules, excluded, "@dana:x").Words; len(got) != 0 {
		t.Errorf("in the excluded room = %v, want nothing — exclude is final", got)
	}
}

// Notifying is per rule over a global default, and one rule saying yes is enough:
// a quieter rule tracking the same word is not an argument against the louder one.
func TestTrackedNotifiesPerRule(t *testing.T) {
	t.Parallel()

	yes, no := true, false
	room := domain.RoomFacts{ID: "!w:x", Spaces: []string{"Work"}}
	loud := []domain.TrackedRule{
		{Words: []string{"outage"}, Notify: &yes},
		{Words: []string{"outage"}, Notify: &no},
	}
	hits := domain.Tracked{Words: []string{"outage"}}.Find("an outage again")
	if !domain.TrackedNotifies(loud, hits, false, room, "@dana:x") {
		t.Error("a rule saying notify=true did not interrupt, though the global default is off")
	}
	quiet := []domain.TrackedRule{{Words: []string{"outage"}, Notify: &no}}
	if domain.TrackedNotifies(quiet, hits, true, room, "@dana:x") {
		t.Error("a rule saying notify=false interrupted, though it overrides the global default")
	}
	// And a rule that says nothing follows the global default.
	plain := []domain.TrackedRule{{Words: []string{"outage"}}}
	if !domain.TrackedNotifies(plain, hits, true, room, "@dana:x") {
		t.Error("a rule with no opinion did not follow the global default")
	}
}
