package daemon

import (
	"strings"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// windows are the windows the daemon serves: every window listening for links, in any
// [[profile]], on any device, as many as run. The daemon keeps a window only while it
// listens.
type windows struct {
	mu sync.Mutex
	// turn counts windows that began listening, which orders windows never used.
	turn uint64
	// windows is every window listening, by its client id.
	windows map[string]*viewer
}

// viewer is one window: the device it runs on, the profile it runs in, the link waiting
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

func newWindows() *windows {
	return &windows{windows: map[string]*viewer{}}
}

// profileID is how a profile's name keys the windows and the notification topics:
// names are unique in any case.
func profileID(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// windowLocked is client's window, made on first sight.
func (s *windows) windowLocked(client string) *viewer {
	w := s.windows[client]
	if w == nil {
		s.turn++
		w = &viewer{seen: s.turn}
		s.windows[client] = w
	}
	return w
}

// forgetLocked drops a window that listens no more.
func (s *windows) forgetLocked(client string) {
	if w := s.windows[client]; w != nil && w.listening == 0 {
		delete(s.windows, client)
	}
}

// listen opens client's link stream, on device, in profile: wake says a link waits, take
// hands it over once. A link already waiting wakes it at once.
func (s *windows) listen(client, device, profile string) (wake <-chan struct{}, take func() string, release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.windowLocked(client)
	w.device, w.profile, w.listening = device, profileID(profile), w.listening+1
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
func (s *windows) present(client string, open domain.RoomID, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.windows[client]; w != nil {
		w.open, w.used = open, at
	}
}

// follow hands uri to one window on device, the one the link was clicked on: the one with
// one of rooms (the link's chat, as each account sees it) open, else the one used last
// whose profile holds one, else the one used last; a window between link streams gets it
// when the next opens. False when no window there listens, so the caller starts one.
func (s *windows) follow(uri, device string, rooms []domain.RoomID, holds func(profile string, room domain.RoomID) bool) bool {
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
