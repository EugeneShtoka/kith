package daemon

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// genWindow is one generated window: how it is known to the daemon, in which profile,
// and what it said of itself.
type genWindow struct {
	client  string
	device  string // "" is kithd's own machine
	profile string
	sits    bool
	listens bool
	open    domain.RoomID // "" when it said nothing of a room
	used    int           // its turn in the order windows said they were used; 0 never
	gone    bool          // it left its seat and closed its link stream
}

// A link goes to exactly one window on the device it was clicked on: the one with its room
// open, else the one used last whose profile holds the room, else the one used last (a
// window never used counts as used when it came); none when no window there sits or
// listens. A window not listening yet gets it once it does. Generated: windows on kithd's
// machine and on paired devices, sitting, listening or both, in profiles holding some
// rooms, with presence in random orders, ties, and links to a room, to several accounts'
// views of one chat, or to nothing known, clicked on any of the devices.
func TestALinkGoesToTheWindowPlacedForIt(t *testing.T) {
	t.Parallel()
	rooms := []domain.RoomID{"r0", "r1", "r2", "r3"}
	for seed := range uint64(500) {
		rng := rand.New(rand.NewPCG(seed, 11))
		holdsRooms := map[string][]domain.RoomID{} // profile → the rooms it holds
		for _, p := range []string{"work", "home", "phone"} {
			for _, r := range rooms {
				if rng.IntN(2) == 0 {
					holdsRooms[p] = append(holdsRooms[p], r)
				}
			}
		}
		holds := func(profile string, room domain.RoomID) bool { return slices.Contains(holdsRooms[profile], room) }

		s := newSeats()
		var windows []*genWindow
		var departures []func()
		takes := map[string]func() string{}
		profiles := []string{"work", "home", "phone"}
		devices := []string{"", "laptop", "tablet"}
		rng.Shuffle(len(profiles), func(i, j int) { profiles[i], profiles[j] = profiles[j], profiles[i] })
		for i := range rng.IntN(4) {
			w := &genWindow{client: fmt.Sprintf("w%d", i), device: devices[rng.IntN(len(devices))], profile: profiles[i%3], sits: rng.IntN(3) > 0, listens: rng.IntN(3) > 0}
			if !w.sits && !w.listens {
				w.listens = true
			}
			var sat sitting
			if w.sits {
				var err error
				if sat, err = s.take(t.Context(), w.profile, w.client, w.device, false, domain.SeatHolder{}); err != nil {
					t.Fatalf("seed %d: %s could not sit: %v", seed, w.client, err)
				}
			}
			var release func()
			if w.listens {
				var take func() string
				_, take, release = s.listen(w.client, w.device, w.profile)
				takes[w.client] = take
			}
			windows = append(windows, w)
			departures = append(departures, func() {
				// A window leaving: its seat, its link stream, or both; gone once neither holds it.
				leaveSeat, closeStream := w.sits && rng.IntN(4) == 0, w.listens && rng.IntN(4) == 0
				if leaveSeat {
					s.leave(sat)
				}
				if closeStream {
					release()
					delete(takes, w.client)
				}
				w.gone = (!w.sits || leaveSeat) && (!w.listens || closeStream)
			})
		}
		for _, depart := range departures {
			depart()
		}
		// Presence, in a random order; some say nothing.
		order := rng.Perm(len(windows))
		at := time.Unix(1000, 0)
		turn := 0
		for _, i := range order {
			if rng.IntN(4) == 0 {
				continue
			}
			w := windows[i]
			if w.gone {
				continue
			}
			if rng.IntN(2) == 0 {
				w.open = rooms[rng.IntN(len(rooms))]
			}
			turn++
			w.used = turn
			s.present(w.client, w.open, at.Add(time.Duration(turn)*time.Second))
		}
		var linked []domain.RoomID
		switch rng.IntN(3) {
		case 0: // one room
			linked = []domain.RoomID{rooms[rng.IntN(len(rooms))]}
		case 1: // one chat, as two accounts see it
			linked = []domain.RoomID{rooms[rng.IntN(2)], rooms[2+rng.IntN(2)]}
		}

		clicked := devices[rng.IntN(len(devices))]
		want := pickWindow(windows, clicked, linked, holds)
		delivered := s.follow("uri", clicked, linked, holds)
		what := fmt.Sprintf("seed %d, link to %v clicked on %q, windows %+v", seed, linked, clicked, windows)
		if delivered != (want != nil) {
			t.Fatalf("%s: delivered %v, want %v", what, delivered, want != nil)
		}
		for _, w := range windows {
			got := ""
			if take := takes[w.client]; take != nil {
				got = take()
			} else {
				// Not listening yet: it gets the link once it listens.
				_, take, _ := s.listen(w.client, w.device, w.profile)
				got = take()
			}
			if (got == "uri") != (want == w) {
				t.Errorf("%s: %s was handed %q, want the link only at %v", what, w.client, got, want)
			}
		}
	}
}

// pickWindow is the rule, written plainly: the device's windows only; open first, then the
// profile, then use.
func pickWindow(windows []*genWindow, device string, linked []domain.RoomID, holds func(string, domain.RoomID) bool) *genWindow {
	// Never used counts as used when it came: before any presence, in arrival order.
	rank := func(w *genWindow) int {
		if w.used > 0 {
			return 1000 + w.used
		}
		return slices.Index(windows, w)
	}
	last := func(keep func(*genWindow) bool) *genWindow {
		var best *genWindow
		for _, w := range windows {
			if !w.gone && w.device == device && keep(w) && (best == nil || rank(w) > rank(best)) {
				best = w
			}
		}
		return best
	}
	if w := last(func(w *genWindow) bool { return w.open != "" && slices.Contains(linked, w.open) }); w != nil {
		return w
	}
	if w := last(func(w *genWindow) bool {
		return slices.ContainsFunc(linked, func(r domain.RoomID) bool { return holds(w.profile, r) })
	}); w != nil {
		return w
	}
	return last(func(*genWindow) bool { return true })
}
