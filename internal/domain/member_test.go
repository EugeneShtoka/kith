package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestMemberName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		member domain.Member
		want   string
	}{
		"display name wins":    {domain.Member{UserID: "matrix:@alice:x", DisplayName: "Alice"}, "Alice"},
		"falls back to local":  {domain.Member{UserID: "matrix:@alice:example.org"}, "alice"},
		"bare id has no local": {domain.Member{UserID: "nonsense"}, "nonsense"},
		"empty":                {domain.Member{}, ""},
	}
	for name, tc := range tests {
		if got := tc.member.Name(); got != tc.want {
			t.Errorf("%s: Name() = %q, want %q", name, got, tc.want)
		}
	}
}

func TestLocalpart(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"matrix:@alice:example.org": "alice",
		"alice:example.org":         "alice",
		"matrix:@whatsapp_4470:x":   "whatsapp_4470",
		"@no-server":                "no-server",
		"":                          "",
		"@:server":                  "",
	}
	for in, want := range tests {
		if got := domain.Localpart(in); got != want {
			t.Errorf("Localpart(%q) = %q, want %q", in, got, want)
		}
	}
}

// Match ranks: a name-token prefix beats a localpart prefix beats a substring.
func TestMemberMatches(t *testing.T) {
	t.Parallel()

	michael := domain.Member{UserID: "matrix:@mike:x", DisplayName: "Michael Livingston"}
	bridged := domain.Member{UserID: "matrix:@whatsapp_4470:x", DisplayName: "Dana Levi"}
	dotted := domain.Member{UserID: "matrix:@d:x", DisplayName: "michael.livingston"}
	parens := domain.Member{UserID: "matrix:@p:x", DisplayName: "Dana Levi (WA)"}

	tests := []struct {
		name   string
		member domain.Member
		query  string
		want   int
	}{
		// Rank 1: a token of the display name starts with the query.
		{"first-name prefix", michael, "mic", 1},
		{"surname prefix", michael, "liv", 1},
		{"full first name", michael, "michael", 1},
		{"case-insensitive", michael, "MICH", 1},
		{"dot-separated token", dotted, "liv", 1},
		{"parenthesised token", parens, "wa", 1},
		// Rank 2: the MXID localpart — how a bridged account is found.
		{"bridge prefix", bridged, "whats", 2},
		// Rank 3: anywhere in the display name.
		{"mid-token substring", michael, "chael", 3},
		{"mid-surname substring", michael, "ingst", 3},
		// Rank 4: anywhere in the localpart.
		{"mid-localpart substring", bridged, "4470", 4},
		// No match.
		{"absent", michael, "zzz", 0},
		{"not a subsequence match", michael, "mlv", 0},
		// Everything matches an empty query, so `@` alone lists the room.
		{"empty query", michael, "", 1},
	}
	for _, tc := range tests {
		if got := tc.member.Matches(tc.query); got != tc.want {
			t.Errorf("%s: %q.Matches(%q) = %d, want %d", tc.name, tc.member.Name(), tc.query, got, tc.want)
		}
	}
}

func TestSortMembers(t *testing.T) {
	t.Parallel()

	members := []domain.Member{
		{UserID: "matrix:@c:x", DisplayName: "carol"},
		{UserID: "matrix:@b2:x", DisplayName: "Bob"},
		{UserID: "matrix:@a:x", DisplayName: "Alice"},
		{UserID: "matrix:@b1:x", DisplayName: "Bob"},
		{UserID: "matrix:@z:x"}, // no display name: sorts by localpart
	}
	domain.SortMembers(members)
	got := make([]string, len(members))
	for i, member := range members {
		got[i] = member.UserID
	}
	// Case-insensitive by name, MXID breaking ties so two Bobs keep a fixed order.
	want := []string{"matrix:@a:x", "matrix:@b1:x", "matrix:@b2:x", "matrix:@c:x", "matrix:@z:x"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted = %v, want %v", got, want)
		}
	}
}

// A mention is recorded when inserted, but the body stays editable.
func TestDraftLiveMentions(t *testing.T) {
	t.Parallel()

	alice := domain.Mention{UserID: "matrix:@alice:x", Name: "Alice"}
	bob := domain.Mention{UserID: "matrix:@bob:x", Name: "Bob"}

	tests := []struct {
		name  string
		draft domain.Draft
		want  []string
	}{
		{
			name:  "still named",
			draft: domain.Draft{Body: "hi Alice", Mentions: []domain.Mention{alice}},
			want:  []string{"matrix:@alice:x"},
		},
		{
			name:  "typed over",
			draft: domain.Draft{Body: "never mind", Mentions: []domain.Mention{alice}},
			want:  nil,
		},
		{
			name:  "one of two deleted",
			draft: domain.Draft{Body: "hi Bob", Mentions: []domain.Mention{alice, bob}},
			want:  []string{"matrix:@bob:x"},
		},
		{
			name:  "named twice, notified once",
			draft: domain.Draft{Body: "Alice, Alice!", Mentions: []domain.Mention{alice, alice}},
			want:  []string{"matrix:@alice:x"},
		},
		{
			name:  "no mentions at all",
			draft: domain.Draft{Body: "plain"},
			want:  nil,
		},
		{
			name:  "a blank mention is not a mention",
			draft: domain.Draft{Body: "hi", Mentions: []domain.Mention{{UserID: "matrix:@x:y"}, {Name: "n"}}},
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			live := tc.draft.LiveMentions()
			if len(live) != len(tc.want) {
				t.Fatalf("LiveMentions() = %+v, want %v", live, tc.want)
			}
			for i := range tc.want {
				if live[i].UserID != tc.want[i] {
					t.Errorf("LiveMentions()[%d] = %q, want %q", i, live[i].UserID, tc.want[i])
				}
			}
		})
	}
}
