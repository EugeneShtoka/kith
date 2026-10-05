package daemon

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Phase is where one network account is: what a client shows beside it.
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
	Network string // the network's name (domain.Protocol): "Matrix", "WhatsApp"
	Account string // the MXID, or the [[whatsapp.account]] name
	Phase   Phase
	Detail  string    // why it is logged out or failed, or what it waits for
	At      time.Time // when it last went online (zero before)
}

// StatusOf is an adapter's report as the daemon keeps it.
func StatusOf(s domain.AccountStatus) NetworkStatus {
	return NetworkStatus{Network: string(s.Network), Account: s.Account, Phase: s.Phase, Detail: s.Detail}
}

// key is what a network account is known by.
func (n NetworkStatus) key() string { return n.Network + "\x00" + n.Account }

// State is the daemon's view of itself: whether it is ready (what it waits for has
// synced — a listening socket over a cold cache is not ready), when it last synced,
// the last error, and each network account. Safe for concurrent use.
type State struct {
	mu       sync.Mutex
	syncedAt time.Time
	lastErr  string
	failed   bool

	// expectSet is set by Expect: readiness is then "every expected account is past
	// connecting", not "something synced" — so a daemon with nothing logged in is
	// ready at once, and `kith login` can reach it.
	expectSet bool
	expected  map[string]bool
	networks  []NetworkStatus
}

// NewState returns a State that is not ready: nothing has synced yet.
func NewState() *State { return &State{expected: map[string]bool{}} }

// Expect names the accounts the daemon starts with a session: it is ready once each
// has gone online, logged out or failed. Called once, before serving; none is ready.
func (s *State) Expect(accounts ...NetworkStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expectSet = true
	for _, a := range accounts {
		s.expected[a.key()] = true
	}
}

// Report records a network account's phase at t. Going online is a sync (see Synced);
// any phase but connecting settles what Expect waits for.
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

// Networks is every network account reported, in the order first reported.
func (s *State) Networks() []NetworkStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.networks)
}

// Synced records a sync response arriving at t, making the daemon ready and
// clearing the last error.
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

// Failed records why the networks stopped and clears readiness: a dead sync loop
// over a warm cache would otherwise look healthy while serving a frozen account.
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

// Snapshot reports readiness, the last sync time (zero before the first) and the
// last error (empty when there has been none).
func (s *State) Snapshot() (ready bool, syncedAt time.Time, lastErr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ready = !s.syncedAt.IsZero()
	if s.expectSet {
		ready = !s.failed && len(s.expected) == 0
	}
	return ready, s.syncedAt, s.lastErr
}

// SyncFault returns nil when err (from api.Backend.Start) is just our own
// shutdown, so a clean stop does not exit non-zero or clear readiness.
func SyncFault(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return nil //nolint:nilerr // discarding the error is the point: our own shutdown is not a fault
	}
	return err
}
