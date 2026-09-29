package tui

// List arithmetic shared by every scrollable selection.

// moveCursor steps cur by delta within n items. wrap cycles at the ends (short
// lists like the completion popup); otherwise it clamps, so long lists keep
// their position.
func moveCursor(cur, n, delta int, wrap bool) int {
	if n <= 0 {
		return 0
	}
	next := cur + delta
	if wrap {
		return ((next % n) + n) % n
	}
	return clampIndex(next, n)
}

// windowStart is the first visible index of a list of total items showing visible
// at a time, scrolled the least it can be to keep cursor on screen.
func windowStart(cursor, visible, total int) int {
	if visible <= 0 || total <= visible {
		return 0
	}
	start := max(cursor-visible+1, 0)
	return min(start, total-visible)
}

// clampIndex keeps an index inside a list that may have shrunk under it.
func clampIndex(i, n int) int {
	switch {
	case n <= 0 || i < 0:
		return 0
	case i >= n:
		return n - 1
	default:
		return i
	}
}

// countLimit bounds a vim-style count typed before a motion (`12j`).
const countLimit = 9999

// countDigit reads a press as another digit of a pending count, and reports whether it
// was one. A leading `0` is not a count (it may be bound); after that it extends one.
func countDigit(press string, pending int) (int, bool) {
	if len(press) != 1 || press[0] < '0' || press[0] > '9' {
		return 0, false
	}
	digit := int(press[0] - '0')
	if digit == 0 && pending == 0 {
		return 0, false
	}
	return min(pending*10+digit, countLimit), true
}

// take returns the pending count (or 1) and clears it, so one count drives one motion.
func (m *Model) take() int {
	n := m.motion.repeat
	m.motion.repeat = 0
	return max(n, 1)
}
