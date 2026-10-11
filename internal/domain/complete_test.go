package domain

import "testing"

func TestDominantNeedsAClearLeader(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		cands []WordCandidate
		ratio int
		want  string
	}{
		{"nothing at all", nil, 3, ""},
		{"one candidate leads by definition", []WordCandidate{{"coriander", 4}}, 3, "coriander"},
		{"three times clear", []WordCandidate{{"coriander", 30}, {"cormorant", 10}}, 3, "coriander"},
		{"a whisker short", []WordCandidate{{"coriander", 29}, {"cormorant", 10}}, 3, ""},
		{"too close to call", []WordCandidate{{"coriander", 11}, {"cormorant", 10}}, 3, ""},
		{"a ratio of one takes the lead as it is", []WordCandidate{{"coriander", 11}, {"cormorant", 10}}, 1, "coriander"},
		// Only the runner-up matters, not the tail.
		{"a tail changes nothing", []WordCandidate{{"coriander", 30}, {"cormorant", 9}, {"corbel", 8}}, 3, "coriander"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Dominant(tc.cands, tc.ratio)
			if (tc.want == "") == ok {
				t.Fatalf("Dominant() = %+v, %v; want %q", got, ok, tc.want)
			}
			if ok && got.Word != tc.want {
				t.Fatalf("Dominant() = %q, want %q", got.Word, tc.want)
			}
		})
	}
}

func TestMergeCandidatesKeepsTheCorpusAhead(t *testing.T) {
	t.Parallel()

	cache := []WordCandidate{{"flapjack", 25}, {"flagstone", 4}}
	// Words this account used outrank commoner frequency-list words it never used.
	freq := []WordCandidate{{"flannel", 900_000}, {"flagstone", 500_000}, {"flatten", 100}}

	got := MergeCandidates(cache, freq, 10)
	want := []string{"flapjack", "flagstone", "flannel", "flatten"}
	if len(got) != len(want) {
		t.Fatalf("MergeCandidates() = %+v, want %v", got, want)
	}
	for i, w := range want {
		if got[i].Word != w {
			t.Fatalf("MergeCandidates() = %+v, want %v in that order", got, want)
		}
	}
	// Appended scores land below the weakest cache candidate, so they never pass the
	// dominance gate.
	for _, c := range got[2:] {
		if c.Score >= cache[len(cache)-1].Score {
			t.Fatalf("appended %q scored %d, want below the cache's own floor %d", c.Word, c.Score, cache[len(cache)-1].Score)
		}
	}

	if got := MergeCandidates(cache, freq, 3); len(got) != 3 {
		t.Fatalf("MergeCandidates(limit 3) = %+v, want three", got)
	}
	if got := MergeCandidates(nil, freq, 2); len(got) != 2 || got[0].Word != "flannel" {
		t.Fatalf("MergeCandidates(no cache) = %+v, want the list's own order", got)
	}
}

func TestRecaseLikeFollowsTheTypist(t *testing.T) {
	t.Parallel()

	cases := []struct{ typed, word, want string }{
		{"comp", "completion", "completion"},
		{"Comp", "completion", "Completion"},
		{"COMP", "completion", "COMPLETION"},
		// Hebrew has no case, so every branch has to leave it exactly as it is.
		{"מש", "משתמשים", "משתמשים"},
		{"", "completion", "completion"},
		{"comp", "", ""},
		// A single typed capital is a capitalised word, not a shout.
		{"C", "completion", "Completion"},
	}
	for _, tc := range cases {
		if got := RecaseLike(tc.typed, tc.word); got != tc.want {
			t.Errorf("RecaseLike(%q, %q) = %q, want %q", tc.typed, tc.word, got, tc.want)
		}
	}
}
