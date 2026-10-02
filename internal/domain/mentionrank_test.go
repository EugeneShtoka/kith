package domain_test

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestRankMembers(t *testing.T) {
	t.Parallel()
	all := []domain.Member{{UserID: "@alice:x"}, {UserID: "@bob:x"}, {UserID: "@carol:x"}, {UserID: "@dave:x"}}
	got := domain.RankMembers(all,
		[]string{"@carol:x", "@ghost:x"}, // a past speaker who has left the room
		[]string{"@bob:x", "@carol:x"},   // already placed on the rung above
		0)
	want := []string{"@carol:x", "@bob:x", "@alice:x", "@dave:x"}
	if len(got) != len(want) {
		t.Fatalf("got %d members, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].UserID != want[i] {
			t.Errorf("position %d = %s, want %s", i, got[i].UserID, want[i])
		}
	}
	if none := domain.RankMembers(all[:2], nil, nil, 0); len(none) != 2 || none[0].UserID != "@alice:x" {
		t.Errorf("with no history = %+v, want everyone in the order given", none)
	}
	for _, tc := range []struct{ limit, want int }{{0, 4}, {-1, 4}, {2, 2}, {4, 4}, {10, 4}} {
		if got := domain.RankMembers(all, nil, nil, tc.limit); len(got) != tc.want {
			t.Errorf("limit %d kept %d, want %d", tc.limit, len(got), tc.want)
		}
	}
}
