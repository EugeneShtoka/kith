package daemon

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/human"
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

	// AliveAt is the latest evidence the network answered (domain.Heard); State.Networks
	// gives an account whose keep-alive answers as alive at the time asked.
	AliveAt time.Time
	// CaughtUp is what was queued for it drained since it last went online.
	CaughtUp bool
	// NotOurs is it stuck for a reason every bridge shares (domain.HeardNotOurs).
	NotOurs bool
	// KeysReady is, on Matrix, this device cross-signed and holding the key backup's key
	// or room keys, or no cross-signing to verify against. UseKeys says it.
	KeysReady bool
}

// Held says whether the bridge holds n as of now (design-failover.md, call 2): online,
// caught up since it connected, alive within its network's window and, on Matrix, its
// keys ready; or stuck for a reason every bridge shares. why says what keeps it from
// being held, or why it counts as held while stuck.
func (n NetworkStatus) Held(now time.Time) (held bool, why string) {
	switch {
	case n.NotOurs:
		return true, "stuck the same on every bridge: " + n.Detail
	case n.Phase != PhaseOnline:
		return false, phaseWord(n.Phase) + detailText(n.Detail)
	case !n.CaughtUp:
		return false, "catching up since it connected " + n.At.Format(time.TimeOnly)
	case n.Network == string(domain.ProtocolMatrix) && !n.KeysReady:
		return false, "its keys are not ready: verify this device and restore the key backup"
	case n.AliveAt.IsZero():
		return false, "nothing heard since it connected"
	case now.Sub(n.AliveAt) > domain.AliveWindow(domain.Protocol(n.Network)):
		return false, "nothing heard within its " + human.Duration(domain.AliveWindow(domain.Protocol(n.Network))) + " window"
	}
	return true, ""
}

func phaseWord(p Phase) string {
	switch p {
	case PhaseLoggedOut:
		return "logged out"
	case PhaseConnecting:
		return "connecting"
	case PhaseFailed:
		return "failed"
	case PhaseOnline:
		return "online"
	}
	return "not started"
}

func detailText(d string) string {
	if d == "" {
		return ""
	}
	return ": " + d
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
	// heard is what each account's evidence left beyond its NetworkStatus, by key.
	heard map[string]*evidence
	log   *slog.Logger
	// keysReady answers NetworkStatus.KeysReady for a Matrix account (UseKeys).
	keysReady func(ctx context.Context, account string) bool
}

// evidence is State's own record of one account's Heard.
type evidence struct {
	// answering is a keep-alive answering (domain.HeardAnswering), with no silence and
	// the account online since. offlineAt is when it last left online: a silence whose
	// last answer is before it is an earlier connection's, heard late.
	answering bool
	offlineAt time.Time
	// from is the latest alive evidence while online, zero while answering or offline:
	// where the next gap starts. longest is this hour's longest gap; hour, when it began.
	from    time.Time
	longest time.Duration
	hour    time.Time
}

// NewState returns a State that is not ready.
func NewState() *State {
	return &State{expected: map[string]bool{}, heard: map[string]*evidence{}, log: slog.New(slog.DiscardHandler)}
}

// UseKeys sets who says whether a Matrix account's keys are ready; unset, none is.
func (s *State) UseKeys(ready func(ctx context.Context, account string) bool) { s.keysReady = ready }

// UseLogger sets where each account's hourly longest gap between alive evidence goes,
// which sets the windows not measured yet (domain.AliveWindow).
func (s *State) UseLogger(log *slog.Logger) {
	if log != nil {
		s.log = log
	}
}

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

// Report records an account's phase at t and returns the phase it had: connecting for
// an account Expect named and nothing reported yet, zero for one never seen. Going
// online counts as a sync; any phase but connecting settles what Expect waits for.
func (s *State) Report(n NetworkStatus, t time.Time) (was Phase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.networks, func(have NetworkStatus) bool { return have.key() == n.key() })
	switch {
	case i >= 0:
		have := s.networks[i]
		n.At, was = have.At, have.Phase
		n.AliveAt, n.CaughtUp, n.NotOurs = have.AliveAt, have.CaughtUp, have.NotOurs
	case s.expected[n.key()]:
		was = PhaseConnecting
	}
	if n.Phase == PhaseOnline {
		n.At = t
		s.synced(t)
	} else {
		s.offline(&n, t)
	}
	if n.Phase != PhaseConnecting {
		delete(s.expected, n.key())
	}
	if i >= 0 {
		s.networks[i] = n
	} else {
		s.networks = append(s.networks, n)
	}
	return was
}

// offline ends what holds only while n is online: its catch-up, its keep-alive's
// answering (alive until t) and the gap being measured. A session gone ends NotOurs.
func (s *State) offline(n *NetworkStatus, t time.Time) {
	n.CaughtUp = false
	if n.Phase == PhaseLoggedOut || n.Phase == PhaseFailed {
		n.NotOurs = false
	}
	e := s.heard[n.key()]
	if e == nil {
		e = &evidence{}
		s.heard[n.key()] = e
	}
	e.offlineAt = t
	if e.answering {
		e.answering = false
		n.AliveAt = later(n.AliveAt, t)
	}
	e.from = time.Time{}
}

// Heard records what an account's network said (domain.Heard) and reports whether it
// changed what Held reads beyond AliveAt, which the beat carries every 30 s anyway.
// An account never reported is not recorded.
func (s *State) Heard(h domain.Heard) (changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := NetworkStatus{Network: string(h.Network), Account: h.Account}.key()
	i := slices.IndexFunc(s.networks, func(have NetworkStatus) bool { return have.key() == key })
	if i < 0 {
		return false
	}
	n := &s.networks[i]
	e := s.heard[key]
	if e == nil {
		e = &evidence{}
		s.heard[key] = e
	}
	online := n.Phase == PhaseOnline
	switch h.Kind {
	case domain.HeardAlive:
		n.AliveAt = later(n.AliveAt, h.At)
		if online && !e.answering {
			s.measure(n, e, h.At)
			e.from = h.At
		}
	case domain.HeardAnswering:
		if !online {
			return false
		}
		changed = !e.answering
		s.measure(n, e, h.At)
		e.answering, e.from = true, time.Time{}
		n.AliveAt = later(n.AliveAt, h.At)
	case domain.HeardSilent:
		if !e.answering || h.At.Before(e.offlineAt) {
			return false
		}
		e.answering, e.from = false, h.At
		n.AliveAt = later(n.AliveAt, h.At)
		return true
	case domain.HeardCaughtUp:
		changed = online && !n.CaughtUp
		n.CaughtUp = n.CaughtUp || online
	case domain.HeardNotOurs, domain.HeardOurs:
		notOurs := h.Kind == domain.HeardNotOurs
		changed, n.NotOurs = n.NotOurs != notOurs, notOurs
	}
	return changed
}

// measure counts the gap from e.from to t into n's hour, first logging the hour before
// once t is past it.
func (s *State) measure(n *NetworkStatus, e *evidence, t time.Time) {
	switch {
	case e.hour.IsZero():
		e.hour = t
	case t.Sub(e.hour) >= time.Hour:
		if e.longest > 0 {
			s.log.Info("alive gaps", "network", n.Network, "account", n.Account,
				"longest", e.longest.Truncate(time.Second), "window", domain.AliveWindow(domain.Protocol(n.Network)))
		}
		e.hour, e.longest = t, 0
	}
	if !e.from.IsZero() {
		e.longest = max(e.longest, t.Sub(e.from))
	}
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Networks is every reported account, in first-reported order, as of now: an account
// whose keep-alive answers is alive at now, and a Matrix account's keys as UseKeys
// says.
func (s *State) Networks(ctx context.Context, now time.Time) []NetworkStatus {
	s.mu.Lock()
	out := slices.Clone(s.networks)
	for i := range out {
		if e := s.heard[out[i].key()]; e != nil && e.answering {
			out[i].AliveAt = now
		}
	}
	s.mu.Unlock()
	for i := range out {
		if out[i].Network == string(domain.ProtocolMatrix) && s.keysReady != nil {
			out[i].KeysReady = s.keysReady(ctx, out[i].Account)
		}
	}
	return out
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
