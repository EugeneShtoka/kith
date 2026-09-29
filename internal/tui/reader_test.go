package tui

import "testing"

// Scrolling stays inside the content, and opening another overlay starts at the top.
func TestReaderScrollStaysInsideItsContent(t *testing.T) {
	t.Parallel()

	r := readerState{}.opening(readerHelp)
	for _, tc := range []struct{ want, limit, expect int }{
		{want: 3, limit: 10, expect: 3},
		{want: 99, limit: 10, expect: 10},
		{want: -4, limit: 10, expect: 0},
		// A list shorter than the frame has a negative limit.
		{want: 2, limit: -6, expect: 0},
	} {
		if got := r.scrolledTo(tc.want, tc.limit).scroll; got != tc.expect {
			t.Errorf("scrolledTo(%d, limit %d) = %d, want %d", tc.want, tc.limit, got, tc.expect)
		}
	}
	r = r.scrolledTo(5, 20).opening(readerWhy)
	if !r.showing(readerWhy) || r.scroll != 0 {
		t.Errorf("opening another overlay = %+v, want it up at the top", r)
	}
}
