package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// seat is the one window the daemon serves (api.Seat), held while its Seat stream is
// open. A forced take asks the holder to step aside and hands over once it closes its
// stream or times out.
type seat struct {
	mu sync.Mutex
	// turn counts windows given the seat; holder is the current client id ("" free),
	// where its whereabouts.
	turn   uint64
	holder string
	where  domain.SeatHolder
	aside  chan struct{}
	left   chan struct{}
	// link is a `kith --open` link handed over while the seated window was not yet
	// listening for links (starting, reconnecting); its link stream gets it.
	link string
}

// stepAsideWait bounds how long a window asked to step aside has to save and close
// before the seat is handed over anyway; longer than the client's own quit wait.
const stepAsideWait = 3 * time.Second

// sitting is one window's time in the seat: aside closes to ask it to step aside,
// over once it left or the seat was handed over.
type sitting struct {
	turn  uint64
	aside <-chan struct{}
	over  <-chan struct{}
}

// take gives client the seat when free, or (force only) once the holder has left or
// ignored a step-aside request. Without force a taken seat is refused with
// ErrSeatTaken naming the holder's whereabouts.
func (s *seat) take(ctx context.Context, client string, force bool, where domain.SeatHolder) (sitting, error) {
	timeout := time.NewTimer(stepAsideWait)
	defer timeout.Stop()
	for {
		s.mu.Lock()
		if s.holder == "" {
			sat := s.sitLocked(client, where)
			s.mu.Unlock()
			return sat, nil
		}
		if !force {
			other := s.where
			s.mu.Unlock()
			return sitting{}, fmt.Errorf("%w: %s", api.ErrSeatTaken, other)
		}
		s.askAsideLocked()
		left := s.left
		s.mu.Unlock()
		select {
		case <-left:
		case <-timeout.C:
			s.mu.Lock()
			sat := s.sitLocked(client, where)
			s.mu.Unlock()
			return sat, nil
		case <-ctx.Done():
			return sitting{}, fmt.Errorf("daemon: wait for the seat: %w", ctx.Err())
		}
	}
}

func (s *seat) sitLocked(client string, where domain.SeatHolder) sitting {
	if s.holder != "" {
		// Handed over while the holder still sat: tell it, and wake waiters.
		s.askAsideLocked()
		close(s.left)
	}
	s.turn++
	s.holder, s.where, s.link = client, where, ""
	s.aside, s.left = make(chan struct{}), make(chan struct{})
	return sitting{turn: s.turn, aside: s.aside, over: s.left}
}

// askAsideLocked tells the window in the seat to step aside, once.
func (s *seat) askAsideLocked() {
	select {
	case <-s.aside:
	default:
		close(s.aside)
	}
}

// leave ends a sitting: the seat is free unless already handed over.
func (s *seat) leave(sat sitting) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != sat.turn || s.holder == "" {
		return
	}
	s.holder = ""
	close(s.left)
}

// keepLink holds uri for the seated window and reports whether there is one.
func (s *seat) keepLink(uri string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holder == "" {
		return false
	}
	s.link = uri
	return true
}

// takeLink returns the held link, once.
func (s *seat) takeLink() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	uri := s.link
	s.link = ""
	return uri
}

// write runs a draft write from client unless another window has the seat, under the
// lock so the seat cannot change hands in between. Client "" (kith-mcp) is never refused.
func (s *seat) write(client string, write func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if client != "" && s.holder != "" && s.holder != client {
		return api.ErrSeatTaken
	}
	return write()
}
