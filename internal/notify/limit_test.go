package notify

import (
	"testing"
	"time"
)

// The limiter is the answer to a question no per-message rule can ask: whether the
// twentieth popup in five minutes is still worth raising.

var epoch = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func TestLimitOffDeliversEverything(t *testing.T) {
	t.Parallel()

	var l Limiter
	for i := range 50 {
		at := epoch.Add(time.Duration(i) * time.Second)
		if d, _ := l.Allow("!room", Limit{}, at); d != Deliver {
			t.Fatalf("message %d = %v with no limit set, want Deliver", i, d)
		}
	}
}

// The burst goes through, then the room starts counting — and says so once, naming how
// many it has held.
func TestABurstIsDeliveredThenSummarized(t *testing.T) {
	t.Parallel()

	var l Limiter
	limit := Limit{Max: 3, Window: 2 * time.Minute}
	for i := range 3 {
		if d, _ := l.Allow("!room", limit, epoch.Add(time.Duration(i)*time.Second)); d != Deliver {
			t.Fatalf("message %d = %v, want Deliver — it is inside the burst", i, d)
		}
	}
	// The fourth is the first over: one popup, naming itself.
	d, held := l.Allow("!room", limit, epoch.Add(4*time.Second))
	if d != Summarize || held != 1 {
		t.Fatalf("the first overflow = %v, %d; want Summarize of 1", d, held)
	}
	// The rest of the window is counted and says nothing.
	for i := range 10 {
		held, _ := l.Allow("!room", limit, epoch.Add(time.Duration(5+i)*time.Second))
		if held != Hold {
			t.Fatalf("message %d after the summary = %v, want Hold", i, held)
		}
	}
	// A window later, the count goes out — all ten of them, not one.
	d, held = l.Allow("!room", limit, epoch.Add(2*time.Minute+5*time.Second))
	if d != Summarize || held != 11 {
		t.Errorf("the next summary = %v, %d; want Summarize naming all 11 held", d, held)
	}
}

// Rooms are counted apart. A busy room must not silence a quiet one, which is the
// whole reason the state is keyed by room rather than global.
func TestRoomsAreLimitedSeparately(t *testing.T) {
	t.Parallel()

	var l Limiter
	limit := Limit{Max: 1, Window: time.Minute}
	if d, _ := l.Allow("!loud", limit, epoch); d != Deliver {
		t.Fatal("the loud room's first message did not get through")
	}
	if d, _ := l.Allow("!loud", limit, epoch.Add(time.Second)); d != Summarize {
		t.Fatal("the loud room's second message was not summarized")
	}
	if d, _ := l.Allow("!quiet", limit, epoch.Add(2*time.Second)); d != Deliver {
		t.Error("a quiet room was limited by a loud one's traffic")
	}
}

// A room that goes quiet starts fresh: the next thing it says arrives as itself rather
// than as a statistic.
func TestAQuietWindowResetsTheRoom(t *testing.T) {
	t.Parallel()

	var l Limiter
	limit := Limit{Max: 2, Window: time.Minute}
	l.Allow("!room", limit, epoch)
	l.Allow("!room", limit, epoch.Add(time.Second))

	// Nothing else arrives for a window, so the burst is over and forgotten.
	if d, held := l.Allow("!room", limit, epoch.Add(2*time.Minute)); d != Deliver || held != 0 {
		t.Errorf("after a quiet window = %v, %d; want an ordinary delivery", d, held)
	}
}

// Nothing is lost to the clock: messages held when a window ran out are named by the
// first message of the next one, rather than vanishing with the window.
func TestHeldMessagesSurviveTheWindowEnding(t *testing.T) {
	t.Parallel()

	var l Limiter
	limit := Limit{Max: 1, Window: time.Minute}
	l.Allow("!room", limit, epoch)                    // delivered
	l.Allow("!room", limit, epoch.Add(time.Second))   // summarized: 1
	l.Allow("!room", limit, epoch.Add(2*time.Second)) // held
	l.Allow("!room", limit, epoch.Add(3*time.Second)) // held

	d, held := l.Allow("!room", limit, epoch.Add(90*time.Second))
	if d != Summarize || held != 3 {
		t.Errorf("the first message of the next window = %v, %d; want the two held plus itself", d, held)
	}
}
