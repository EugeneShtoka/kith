package tui

import (
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// every room exists, for the tests that are not about a room going away.
func anyRoom(domain.RoomID) bool { return true }

// The list records places you opened, and nothing else.
func TestJumplistRecordsArrivals(t *testing.T) {
	t.Parallel()

	var j jumplist
	j = j.arrived("!a:x") // the first place: nowhere to come back to yet
	j = j.arrived("!a:x") // opening the room you are already in
	if len(j.back) != 0 {
		t.Fatalf("back = %v, want empty — neither of those left anywhere", j.back)
	}

	j = j.arrived("!b:x")
	next, to, ok := j.goingBack(anyRoom)
	if !ok || to != "!a:x" {
		t.Fatalf("back gave (%q, %v), want !a:x", to, ok)
	}
	j = next
	if j.anchor != "!a:x" {
		t.Errorf("anchor = %q after going back, want !a:x", j.anchor)
	}
	fwd, to, ok := j.goingForward(anyRoom)
	if !ok || to != "!b:x" {
		t.Fatalf("forward gave (%q, %v), want !b:x", to, ok)
	}
	j = fwd
	if len(j.back) != 1 || len(j.forward) != 0 {
		t.Errorf("after a round trip back=%v forward=%v, want one entry back and none forward", j.back, j.forward)
	}

	// The landing itself is announced again by the code that opens a room, and must not
	// double-count: the anchor is already there.
	j = j.arrived("!b:x")
	if len(j.back) != 1 {
		t.Errorf("back = %v after re-announcing the place we are at, want it unchanged", j.back)
	}

	// Opening somewhere new is a branch: what you could have gone forward to is not on
	// the way anywhere any more.
	j = j.arrived("!c:x")
	if len(j.forward) != 0 {
		t.Errorf("forward = %v after opening somewhere new, want it dropped", j.forward)
	}
}

// Nothing to go back to is a state, not an error — the model says so rather than moving
// somewhere arbitrary.
func TestJumplistEmptyEnds(t *testing.T) {
	t.Parallel()

	var j jumplist
	if _, _, ok := j.goingBack(anyRoom); ok {
		t.Error("an empty list went back")
	}
	if _, _, ok := j.goingForward(anyRoom); ok {
		t.Error("an empty list went forward")
	}
}

// The list is bounded, and it does not repeat the entry already on top: alternating
// between two rooms all afternoon must not grow it.
func TestJumplistIsBounded(t *testing.T) {
	t.Parallel()

	var j jumplist
	for i := range jumpDepth * 2 {
		j = j.arrived(domain.RoomID("!" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ":x"))
	}
	if len(j.back) > jumpDepth {
		t.Errorf("back holds %d entries, want at most %d", len(j.back), jumpDepth)
	}

	// Back and forward between two rooms, twice round: one entry each side.
	pair := jumplist{}.arrived("!a:x").arrived("!b:x")
	for range 2 {
		next, _, ok := pair.goingBack(anyRoom)
		if !ok {
			t.Fatal("the round trip ran out of places to go back to")
		}
		pair = next
		next, _, ok = pair.goingForward(anyRoom)
		if !ok {
			t.Fatal("the round trip ran out of places to go forward to")
		}
		pair = next
	}
	if len(pair.back) != 1 || len(pair.forward) != 0 {
		t.Errorf("back=%v forward=%v after two round trips, want one and none", pair.back, pair.forward)
	}
}

// A room that has gone away is stepped over and forgotten, so the key that skipped it
// does not trip over it again.
func TestJumplistStepsOverAGoneRoom(t *testing.T) {
	t.Parallel()

	j := jumplist{anchor: "!here:x", back: []domain.RoomID{"!a:x", "!gone:x"}}
	next, to, ok := j.goingBack(func(id domain.RoomID) bool { return id != "!gone:x" })
	if !ok || to != "!a:x" {
		t.Fatalf("back gave (%q, %v), want it to step over the gone room to !a:x", to, ok)
	}
	if len(next.back) != 0 {
		t.Errorf("back = %v, want both entries consumed", next.back)
	}
	if len(next.forward) != 1 || next.forward[0] != "!here:x" {
		t.Errorf("forward = %v, want the place we left", next.forward)
	}

	// Nothing but dead entries is the same as nothing at all.
	dead := jumplist{anchor: "!here:x", back: []domain.RoomID{"!gone:x"}}
	if _, _, ok := dead.goingBack(func(domain.RoomID) bool { return false }); ok {
		t.Error("a list of gone rooms claimed somewhere to go")
	}
}
