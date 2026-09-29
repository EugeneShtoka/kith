package vocab

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestFoldIsTheIndexsFold(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"Planning", "planning"},
		{"Café", "cafe"},
		{"naïve", "naive"},
		{"Ångström", "angstrom"},
		// Only Latin letters lose their marks, as unicode61 remove_diacritics does.
		{"Привет", "привет"},
		{"ёлка", "ёлка"},
		{"שלום", "שלום"},
	}
	for _, c := range cases {
		if got := Fold(c.in); got != c.want {
			t.Errorf("Fold(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func words(cands []domain.WordCandidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Word
	}
	return out
}

// The score is the weighted sum over the windows in scope; ties go alphabetically, and
// a word saving too little typing (the prefix itself, "deploys") is never offered.
func TestRankWeighsTheWindows(t *testing.T) {
	t.Parallel()
	room, space, mine, global := NewWindow(10), NewWindow(10), NewWindow(10), NewWindow(10)
	room.Add("deploy deployment")             // deploy 10, deployment 10
	space.Add("deployment deployment")        // deployment +6
	mine.Add("deployed")                      // deployed 5
	global.Add("deploy deployed deployments") // +1 each
	global.Add("Déployées deploys")           // "deployees" +1; "deploys" saves too little

	got := Rank("deploy", Scope{Room: room, Space: space, Mine: mine, Global: global}, 10)
	want := []domain.WordCandidate{
		{Word: "deployment", Score: 16},
		{Word: "deployed", Score: 6},
		{Word: "deployees", Score: 1},
		{Word: "deployments", Score: 1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Rank = %+v, want %+v", got, want)
	}
	if got := words(Rank("deploy", Scope{Global: global}, 2)); !slices.Equal(got, []string{"deployed", "deployees"}) {
		t.Errorf("global only, limit 2 = %v", got)
	}
}

// A window added to after its prefix list was built still finds the new word.
func TestAWindowFindsWordsAddedAfterALookup(t *testing.T) {
	t.Parallel()
	w := NewWindow(4)
	w.Add("release")
	_ = Rank("rel", Scope{Global: w}, 5)
	w.Add("relevant")
	if got := words(Rank("rel", Scope{Global: w}, 5)); !slices.Equal(got, []string{"release", "relevant"}) {
		t.Errorf("Rank = %v, want both", got)
	}
	for range 4 {
		w.Add("x")
	}
	if !w.Stale() {
		t.Error("a window 50% over its limit is not stale")
	}
}

// What is never offered: the prefix itself, a word saving under two characters, and
// anything for an empty prefix or no room to show it.
func TestRankRefusals(t *testing.T) {
	t.Parallel()
	w := NewWindow(4)
	w.Add("cormorant corm short")
	for _, c := range []struct {
		name   string
		prefix string
		limit  int
		want   int
	}{
		{"the prefix itself", "cormorant", 5, 0},
		{"just long enough", "cormor", 5, 1},
		{"nothing left to save", "cormoran", 5, 0},
		{"an empty prefix", "", 5, 0},
		{"no limit", "cor", 0, 0},
	} {
		if got := Rank(c.prefix, Scope{Global: w}, c.limit); len(got) != c.want {
			t.Errorf("%s: %v, want %d candidate(s)", c.name, words(got), c.want)
		}
	}
}

// A Hebrew prefix finds the Hebrew words starting with it.
func TestRankHebrewPrefix(t *testing.T) {
	t.Parallel()
	w := NewWindow(4)
	w.Add("שלומית שלום שמש")
	if got := words(Rank("שלו", Scope{Global: w}, 5)); !slices.Equal(got, []string{"שלומית"}) {
		t.Errorf("Rank = %v, want the word that starts with the prefix and saves typing", got)
	}
}
