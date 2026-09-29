package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// seat is the one window the daemon serves (api.Seat). A window has it while its Seat
// stream is open; asking for it by force tells the window that has it to step aside,
// and gives it over once that window has closed its stream or not done so in time.
type seat struct {
	mu sync.Mutex
	// turn counts the windows given the seat; holder is the current one's client id
	// ("" free), where is its whereabouts, aside is closed to tell it to step aside,
	// and left when its stream ends.
	turn   uint64
	holder string
	where  domain.SeatHolder
	aside  chan struct{}
	left   chan struct{}
	// link is a matrix link handed over (`kith --open`) while a window sat but did
	// not listen for links yet (starting up, reconnecting): its link stream gets it.
	link string
}

// stepAsideWait bounds how long a window told to step aside has to save its drafts and
// close before the seat is given over anyway: it has stopped answering. Longer than
// the client's own wait for its saves at quit.
const stepAsideWait = 3 * time.Second

// sitting is one window's time in the seat: aside is closed to tell it to step aside,
// and over once it has left or the seat was given over without it.
type sitting struct {
	turn  uint64
	aside <-chan struct{}
	over  <-chan struct{}
}

// take gives client the seat: at once when it is free, or when the window that has it
// has left or not answered a request to step aside (force only). Without force a
// taken seat is refused with ErrSeatTaken, naming where the other window is.
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
		// Given over while that window still sat (it did not answer): it is told, and
		// whoever waited for it to leave looks again.
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

// leave is a sitting's stream ending: the seat is free, unless it was given over
// already.
func (s *seat) leave(sat sitting) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != sat.turn || s.holder == "" {
		return
	}
	s.holder = ""
	close(s.left)
}

// keepLink holds uri for the window that sits, and reports whether one does.
func (s *seat) keepLink(uri string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holder == "" {
		return false
	}
	s.link = uri
	return true
}

// takeLink is the link held for the window, once.
func (s *seat) takeLink() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	uri := s.link
	s.link = ""
	return uri
}

// write runs a draft write from client unless another window has the seat. It runs
// under the lock, so the seat does not change hands between the check and the write.
// A client of "" is not a window (kith-mcp) and is never refused.
func (s *seat) write(client string, write func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if client != "" && s.holder != "" && s.holder != client {
		return api.ErrSeatTaken
	}
	return write()
}
