package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// seats are the windows the daemon serves: in each [[profile]] the one window that sits
// (api.Seat), held while its Seat stream is open, and every window listening for links.
// A forced take asks the holder to step aside and hands over once it closes its stream
// or times out. The daemon keeps a window only while it sits or listens.
type seats struct {
	mu sync.Mutex
	// turn counts windows given a seat and windows that began listening, which orders
	// windows never used.
	turn uint64
	// held is each profile's sitting, by the profile's name in lower case.
	held map[string]*sitter
	// windows is every window sitting or listening, by its client id.
	windows map[string]*viewer
}

// sitter is the window sitting in one profile; aside closes to ask it to step aside,
// left once it left or the seat was handed over.
type sitter struct {
	turn   uint64
	client string
	where  domain.SeatHolder
	aside  chan struct{}
	left   chan struct{}
}

// viewer is one TUI window: the device it runs on, the profile it runs in, the link waiting
// for it and the wake that says one is, and what Present said of it.
type viewer struct {
	// device is the paired device it reaches kithd from; "" over the socket, kithd's own.
	device  string
	profile string // lower case
	// listening counts its open link streams; wake is the one now reading.
	listening int
	wake      chan struct{}
	link      string
	open      domain.RoomID
	used      time.Time
	seen      uint64
}

func newSeats() *seats {
	return &seats{held: map[string]*sitter{}, windows: map[string]*viewer{}}
}

// stepAsideWait bounds how long a window asked to step aside has to save and close
// before the seat is handed over anyway; longer than the client's own quit wait.
const stepAsideWait = 3 * time.Second

// sitting is one window's time in a profile's seat: aside closes to ask it to step
// aside, over once it left or the seat was handed over.
type sitting struct {
	profile string
	turn    uint64
	aside   <-chan struct{}
	over    <-chan struct{}
}

// seatKey is how a profile's name keys the seats: names are unique in any case.
func seatKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// take gives client, on device, the profile's seat when free, or (force only) once the
// holder has left or ignored a step-aside request. Without force a taken seat is refused
// with ErrSeatTaken naming the holder's whereabouts.
func (s *seats) take(ctx context.Context, profile, client, device string, force bool, where domain.SeatHolder) (sitting, error) {
	profile = seatKey(profile)
	timeout := time.NewTimer(stepAsideWait)
	defer timeout.Stop()
	for {
		s.mu.Lock()
		holder := s.held[profile]
		if holder == nil {
			sat := s.sitLocked(profile, client, device, where)
			s.mu.Unlock()
			return sat, nil
		}
		if !force {
			other := holder.where
			s.mu.Unlock()
			return sitting{}, fmt.Errorf("%w: %s", api.ErrSeatTaken, other)
		}
		askAside(holder)
		left := holder.left
		s.mu.Unlock()
		select {
		case <-left:
		case <-timeout.C:
			s.mu.Lock()
			sat := s.sitLocked(profile, client, device, where)
			s.mu.Unlock()
			return sat, nil
		case <-ctx.Done():
			return sitting{}, fmt.Errorf("daemon: wait for the seat: %w", ctx.Err())
		}
	}
}

func (s *seats) sitLocked(profile, client, device string, where domain.SeatHolder) sitting {
	before := s.held[profile]
	s.turn++
	sat := &sitter{turn: s.turn, client: client, where: where, aside: make(chan struct{}), left: make(chan struct{})}
	s.held[profile] = sat
	if before != nil {
		// Handed over while the holder still sat: tell it, and wake waiters.
		askAside(before)
		close(before.left)
		s.forgetLocked(before.client)
	}
	w := s.windowLocked(client)
	w.device, w.profile = device, profile
	return sitting{profile: profile, turn: sat.turn, aside: sat.aside, over: sat.left}
}

// askAside tells the window in a seat to step aside, once.
func askAside(holder *sitter) {
	select {
	case <-holder.aside:
	default:
		close(holder.aside)
	}
}

// leave ends a sitting: the seat is free unless already handed over.
func (s *seats) leave(sat sitting) {
	s.mu.Lock()
	defer s.mu.Unlock()
	holder := s.held[sat.profile]
	if holder == nil || holder.turn != sat.turn {
		return
	}
	delete(s.held, sat.profile)
	close(holder.left)
	s.forgetLocked(holder.client)
}

// holderOf is who sits in a profile, for a refusal; the zero value when nobody does.
func (s *seats) holderOf(profile string) domain.SeatHolder {
	s.mu.Lock()
	defer s.mu.Unlock()
	if holder := s.held[seatKey(profile)]; holder != nil {
		return holder.where
	}
	return domain.SeatHolder{}
}

// write runs a draft write from client unless another window sits in the profile it
// writes in, under the lock so the seat cannot change hands in between. Client ""
// (kith-mcp) is never refused.
func (s *seats) write(profile, client string, write func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if holder := s.held[seatKey(profile)]; client != "" && holder != nil && holder.client != client {
		return api.ErrSeatTaken
	}
	return write()
}

// windowLocked is client's window, made on first sight.
func (s *seats) windowLocked(client string) *viewer {
	w := s.windows[client]
	if w == nil {
		s.turn++
		w = &viewer{seen: s.turn}
		s.windows[client] = w
	}
	return w
}

// forgetLocked drops a window that neither sits nor listens any more.
func (s *seats) forgetLocked(client string) {
	w := s.windows[client]
	if w == nil || w.listening > 0 {
		return
	}
	for _, holder := range s.held {
		if holder.client == client {
			return
		}
	}
	delete(s.windows, client)
}

// listen opens client's link stream, on device, in profile: wake says a link waits, take
// hands it over once. A link already waiting wakes it at once.
func (s *seats) listen(client, device, profile string) (wake <-chan struct{}, take func() string, release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.windowLocked(client)
	w.device, w.profile, w.listening = device, seatKey(profile), w.listening+1
	w.wake = make(chan struct{}, 1)
	if w.link != "" {
		w.wake <- struct{}{}
	}
	mine := w.wake
	take = func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		uri := w.link
		w.link = ""
		return uri
	}
	release = func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.listening--
		if w.wake == mine {
			w.wake = nil
		}
		s.forgetLocked(client)
	}
	return mine, take, release
}

// present records what a window said it is doing.
func (s *seats) present(client string, open domain.RoomID, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.windows[client]; w != nil {
		w.open, w.used = open, at
	}
}

// follow hands uri to one window on device, the one the link was clicked on: the one with
// one of rooms (the link's chat, as each account sees it) open, else the one used last
// whose profile holds one, else the one used last; a window not listening yet gets it when
// it does. False when no window there sits or listens, so the caller starts one.
func (s *seats) follow(uri, device string, rooms []domain.RoomID, holds func(profile string, room domain.RoomID) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hasOpen, inProfile, any *viewer
	later := func(a, b *viewer) bool {
		return b == nil || a.used.After(b.used) || a.used.Equal(b.used) && a.seen > b.seen
	}
	for _, w := range s.windows {
		if w.device != device {
			continue
		}
		if later(w, any) {
			any = w
		}
		for _, room := range rooms {
			if w.open == room && later(w, hasOpen) {
				hasOpen = w
			}
			if holds(w.profile, room) && later(w, inProfile) {
				inProfile = w
			}
		}
	}
	to := any
	switch {
	case hasOpen != nil:
		to = hasOpen
	case inProfile != nil:
		to = inProfile
	case to == nil:
		return false
	}
	to.link = uri
	if to.wake != nil {
		select {
		case to.wake <- struct{}{}:
		default: // already woken: it takes the latest link
		}
	}
	return true
}
