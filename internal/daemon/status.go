package daemon

import (
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Phase is a network account's state, as a client shows it.
type Phase = domain.AccountPhase

// The phases, as domain names them.
const (
	PhaseLoggedOut  = domain.AccountLoggedOut
	PhaseConnecting = domain.AccountConnecting
	PhaseOnline     = domain.AccountOnline
	PhaseFailed     = domain.AccountFailed
)

// NetworkStatus is one network account as the daemon sees it.
type NetworkStatus struct {
	Network string // domain.Protocol: "Matrix", "WhatsApp"
	Account string // the MXID, or the [[whatsapp.account]] name
	Phase   Phase
	Detail  string    // why it is logged out or failed, or what it waits for
	At      time.Time // when it last went online (zero before)
}

// StatusOf is an adapter's report as the daemon keeps it.
func StatusOf(s domain.AccountStatus) NetworkStatus {
	return NetworkStatus{Network: string(s.Network), Account: s.Account, Phase: s.Phase, Detail: s.Detail}
}

func (n NetworkStatus) key() string { return n.Network + "\x00" + n.Account }

// State is the daemon's view of itself: readiness (a listening socket over a cold
// cache is not ready), last sync, last error, and each network account. Safe for
// concurrent use.
type State struct {
	mu       sync.Mutex
	syncedAt time.Time
	lastErr  string
	failed   bool

	// expectSet makes readiness "every expected account is past connecting" rather than
	// "something synced", so a daemon with nothing logged in is ready and `kith login`
	// can reach it.
	expectSet bool
	expected  map[string]bool
	networks  []NetworkStatus
}

// NewState returns a State that is not ready.
func NewState() *State { return &State{expected: map[string]bool{}} }

// Expect names the accounts the daemon starts with a session; it is ready once each
// has gone online, logged out or failed. Call once, before serving.
func (s *State) Expect(accounts ...NetworkStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expectSet = true
	for _, a := range accounts {
		s.expected[a.key()] = true
	}
}

// Report records an account's phase at t. Going online counts as a sync; any phase
// but connecting settles what Expect waits for.
func (s *State) Report(n NetworkStatus, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.networks, func(have NetworkStatus) bool { return have.key() == n.key() })
	if i >= 0 {
		n.At = s.networks[i].At
	}
	if n.Phase == PhaseOnline {
		n.At = t
		s.synced(t)
	}
	if n.Phase != PhaseConnecting {
		delete(s.expected, n.key())
	}
	if i >= 0 {
		s.networks[i] = n
	} else {
		s.networks = append(s.networks, n)
	}
}

// Networks is every reported account, in first-reported order.
func (s *State) Networks() []NetworkStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.networks)
}

// Synced records a sync response at t, making the daemon ready and clearing the error.
func (s *State) Synced(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.synced(t)
}

func (s *State) synced(t time.Time) {
	s.syncedAt = t
	s.lastErr = ""
	s.failed = false
}

// Failed records why the networks stopped and clears readiness, so a dead sync loop
// over a warm cache does not look healthy.
func (s *State) Failed(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncedAt = time.Time{}
	s.lastErr = err.Error()
	s.failed = true
}

// Snapshot reports readiness, the last sync time (zero before the first) and the last
// error ("" if none).
func (s *State) Snapshot() (ready bool, syncedAt time.Time, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ready = !s.syncedAt.IsZero()
	if s.expectSet {
		ready = !s.failed && len(s.expected) == 0
	}
	return ready, s.syncedAt, s.lastErr
}
