package tui

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The zero searchState is a word search, so a closed pane summarizes without panicking.
func TestSearchKindZeroValueIsAWordSearch(t *testing.T) {
	var s searchState
	if got := s.showing(); got != (wordsList{}) {
		t.Fatalf("zero searchState shows %T, want wordsList", got)
	}
	m := Model{}
	if got := m.searchSummary(); got == "" {
		t.Error("a closed results pane must still summarize, not panic or say nothing")
	}
}

// Each kind's clause makes a list a non-empty query with no terms.
func TestSearchKindNarrow(t *testing.T) {
	for _, tc := range []struct {
		kind      searchKind
		name      string
		mentioned bool
		hasFile   bool
		fileName  bool
	}{
		{wordsList{}, "Search", false, false, false},
		{mentionsList{}, "Mentions", true, false, false},
		{filesList{}, "Files", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f domain.SearchFilter
			tc.kind.narrow(&f)
			if f.Mentioned != tc.mentioned || f.HasFile != tc.hasFile {
				t.Errorf("narrow gave mentioned=%v hasFile=%v, want %v/%v",
					f.Mentioned, f.HasFile, tc.mentioned, tc.hasFile)
			}
			if tc.kind.name() != tc.name {
				t.Errorf("name = %q, want %q", tc.kind.name(), tc.name)
			}
			if tc.kind.showsFileName() != tc.fileName {
				t.Errorf("showsFileName = %v, want %v", tc.kind.showsFileName(), tc.fileName)
			}
			// A list's clause alone is a query; a word search with no terms is not.
			if f.Empty() == (tc.mentioned || tc.hasFile) {
				t.Errorf("filter.Empty() = %v for %s, which inverts what the pane relies on",
					f.Empty(), tc.name)
			}
		})
	}
}

// Each kind says its own summary sentence.
func TestSearchKindSummaries(t *testing.T) {
	m := Model{}
	for _, tc := range []struct {
		kind searchKind
		want string
	}{
		{wordsList{}, "type to search in this room"},
		{mentionsList{}, "nobody has named you in this room"},
		{filesList{}, "no files in this room"},
	} {
		if got := tc.kind.summary(m, "in this room"); got != tc.want {
			t.Errorf("%T summary = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
