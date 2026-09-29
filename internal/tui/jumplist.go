package tui

import "github.com/EugeneShtoka/kith/internal/domain"

// Back and forward over places, like vim's jumplist: only places you opened on purpose
// (enter, the switcher, a chord, a search hit) are recorded, not rooms the cursor walks
// past. The anchor is the last place opened; going back pushes it onto the forward
// side, and jumping somewhere new drops the forward side.

// jumpDepth bounds each side.
const jumpDepth = 32

// jumplist is where you have been. back's last element is the most recent place left;
// forward's last is the most recent place returned from.
type jumplist struct {
	anchor  domain.RoomID
	back    []domain.RoomID
	forward []domain.RoomID
}

// arrived records landing somewhere on purpose: the anchor goes onto the back stack and
// the forward side is dropped. Arriving where you already are records nothing.
func (j jumplist) arrived(at domain.RoomID) jumplist {
	if at == "" || at == j.anchor {
		return j
	}
	if j.anchor != "" {
		j.back = push(j.back, j.anchor)
		j.forward = nil
	}
	j.anchor = at
	return j
}

// goingBack moves to the most recent place left whose room still exists, dropping dead
// entries on the way. ok is false when nothing is left.
func (j jumplist) goingBack(exists func(domain.RoomID) bool) (jumplist, domain.RoomID, bool) {
	back, forward, anchor, to, ok := stepPlaces(j.back, j.forward, j.anchor, exists)
	j.back, j.forward, j.anchor = back, forward, anchor
	return j, to, ok
}

// goingForward is goingBack's mirror.
func (j jumplist) goingForward(exists func(domain.RoomID) bool) (jumplist, domain.RoomID, bool) {
	forward, back, anchor, to, ok := stepPlaces(j.forward, j.back, j.anchor, exists)
	j.back, j.forward, j.anchor = back, forward, anchor
	return j, to, ok
}

// stepPlaces pops from one side onto the other, stepping over and forgetting rooms that
// have gone.
func stepPlaces(
	from, onto []domain.RoomID,
	anchor domain.RoomID,
	exists func(domain.RoomID) bool,
) (rest, grown []domain.RoomID, newAnchor, to domain.RoomID, ok bool) {
	for len(from) > 0 {
		to = from[len(from)-1]
		from = from[:len(from)-1]
		if !exists(to) {
			from, onto = without(from, to), without(onto, to)
			continue
		}
		if anchor != "" {
			onto = push(onto, anchor)
		}
		return from, onto, to, to, true
	}
	return from, onto, anchor, "", false
}

// push appends, keeping the newest jumpDepth entries and never repeating the top.
func push(stack []domain.RoomID, id domain.RoomID) []domain.RoomID {
	if n := len(stack); n > 0 && stack[n-1] == id {
		return stack
	}
	stack = append(stack, id)
	if len(stack) > jumpDepth {
		stack = stack[len(stack)-jumpDepth:]
	}
	return stack
}

// without returns a copy of items without x, in a fresh backing array: the caller's
// slice is shared state.
func without[T comparable](items []T, x T) []T {
	out := make([]T, 0, len(items))
	for _, item := range items {
		if item != x {
			out = append(out, item)
		}
	}
	return out
}
