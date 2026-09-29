package tui

import "testing"

// One table of motions for the plain scrolled lists (history, search results).
func TestNavDelta(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		act   action
		count int
		page  int
		all   int
		want  int
		ok    bool
	}{
		{"a count repeats a line", actDown, 3, 20, 99, 3, true},
		{"up is negative", actUp, 2, 20, 99, -2, true},
		{"a page is the pane", actPageDown, 5, 20, 99, 20, true},
		{"a half page", actHalfPageUp, 1, 20, 99, -10, true},
		{"a half page never stands still", actHalfPageDown, 1, 1, 99, 1, true},
		{"newest is all", actSelectNewest, 1, 20, 99, 99, true},
		{"oldest is the other way", actScrollOldest, 1, 20, 99, -99, true},
		{"a newest-first list flips the ends", actSelectNewest, 1, 20, -7, -7, true},
		{"not a motion", actJump, 1, 20, 99, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, ok := navDelta(c.act, c.count, c.page, c.all)
			if got != c.want || ok != c.ok {
				t.Fatalf("navDelta = (%d, %v), want (%d, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}
