package tui

import (
	"fmt"
	"slices"
)

// staleRow returns the first cached timeline row the next frame would serve that differs
// from a fresh render, or nil; a row input missing from the cache key shows up here. It
// never fills the cache.
func staleRow(m Model) error {
	d := m.derived
	if d == nil || !d.valid || d.key != m.keyFor() {
		return nil // the next frame recomputes everything
	}
	width := m.contentWidth()
	if len(d.rowCache) != len(d.shown) || d.rowWidth != width || d.rowNameW != d.nameW {
		return nil // the next frame resets the rows
	}
	w := m.walkOver(d) // the walk the frame builds, without readying the rows
	for i := range d.rowCache {
		e := &d.rowCache[i]
		if !e.valid || !e.current(m.entryInputs(w, i)) {
			continue // re-rendered when next drawn
		}
		// The whole entry: rows, rule above, and height.
		if fresh := m.renderEntry(w, i); !slices.Equal(e.rows, fresh.rows) ||
			e.divider != fresh.divider || e.unread != fresh.unread || e.height != fresh.height {
			return fmt.Errorf("row cache: message %d (%s) is cached as\n%q (divider %v, unread %v, height %d)\n"+
				"but renders as\n%q (divider %v, unread %v, height %d)",
				i, w.msgs[i].ID, e.rows, e.divider, e.unread, e.height, fresh.rows, fresh.divider, fresh.unread, fresh.height)
		}
	}
	return nil
}
