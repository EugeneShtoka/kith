package tui

import "testing"

func TestMoveCursorWrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cur, n, delta int
		wrap          bool
		want          int
	}{
		// Wrapping: a short list you cycle.
		{"wrap forward", 0, 3, 1, true, 1},
		{"wrap off the end", 2, 3, 1, true, 0},
		{"wrap off the start", 0, 3, -1, true, 2},
		{"wrap by more than the length", 0, 3, 7, true, 1},
		{"wrap backward by more than the length", 0, 3, -7, true, 2},
		// Clamping: a long list has a position you can lose.
		{"clamp forward", 0, 3, 1, false, 1},
		{"clamp at the end", 2, 3, 1, false, 2},
		{"clamp at the start", 0, 3, -1, false, 0},
		{"clamp a big jump", 0, 3, 99, false, 2},
		{"clamp a big backward jump", 2, 3, -99, false, 0},
		// Degenerate: an empty list has no cursor to move.
		{"empty wraps to zero", 0, 0, 1, true, 0},
		{"empty clamps to zero", 5, 0, -1, false, 0},
		{"single item wraps to itself", 0, 1, 1, true, 0},
	}
	for _, tc := range tests {
		if got := moveCursor(tc.cur, tc.n, tc.delta, tc.wrap); got != tc.want {
			t.Errorf("%s: moveCursor(%d, %d, %d, wrap=%v) = %d, want %d",
				tc.name, tc.cur, tc.n, tc.delta, tc.wrap, got, tc.want)
		}
	}
}

func TestWindowStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		cursor, visible, total int
		want                   int
	}{
		// Everything fits: no scrolling, whatever the cursor does.
		{"all visible", 0, 10, 5, 0},
		{"all visible, cursor at the end", 4, 10, 5, 0},
		// The window holds still while the cursor moves inside it.
		{"cursor in view", 2, 5, 20, 0},
		{"cursor on the last visible row", 4, 5, 20, 0},
		// And moves the least it can once the cursor leaves.
		{"cursor one past the window", 5, 5, 20, 1},
		{"cursor well past", 10, 5, 20, 6},
		// It never scrolls past the end, so the last screen is full.
		{"cursor at the very end", 19, 5, 20, 15},
		{"cursor beyond the end", 99, 5, 20, 15},
		// Degenerate.
		{"no room", 3, 0, 20, 0},
		{"empty list", 0, 5, 0, 0},
	}
	for _, tc := range tests {
		if got := windowStart(tc.cursor, tc.visible, tc.total); got != tc.want {
			t.Errorf("%s: windowStart(%d, %d, %d) = %d, want %d",
				tc.name, tc.cursor, tc.visible, tc.total, got, tc.want)
		}
	}
}

func TestClampIndex(t *testing.T) {
	t.Parallel()

	tests := []struct{ i, n, want int }{
		{0, 5, 0}, {3, 5, 3}, {4, 5, 4},
		{5, 5, 4},  // shrunk under the cursor
		{99, 5, 4}, // shrunk a lot
		{-1, 5, 0},
		{3, 0, 0}, // emptied entirely
	}
	for _, tc := range tests {
		if got := clampIndex(tc.i, tc.n); got != tc.want {
			t.Errorf("clampIndex(%d, %d) = %d, want %d", tc.i, tc.n, got, tc.want)
		}
	}
}
