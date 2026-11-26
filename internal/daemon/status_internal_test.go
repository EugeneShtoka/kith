package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A daemon waiting for nothing is ready at once, so `kith login` can reach it.
func TestStateExpectingNothingIsReady(t *testing.T) {
	t.Parallel()
	s := NewState()
	s.Expect()
	if ready, _, _ := s.Snapshot(); !ready {
		t.Error("ready = false for a daemon that waits for nothing")
	}
	s.Failed(errors.New("networks: stopped"))
	if ready, _, lastErr := s.Snapshot(); ready || lastErr == "" {
		t.Errorf("after Failed: ready = %t, lastErr = %q; want not ready, with the reason", ready, lastErr)
	}
}

// For any sequence of reports, failures and syncs, readiness holds exactly when nothing
// failed since the last sync and every expected account is past connecting; rows keep
// each account's latest phase and last online time.
func TestStateReadinessFollowsTheExpectedAccounts(t *testing.T) {
	t.Parallel()
	accounts := []NetworkStatus{
		{Network: "matrix", Account: "matrix:@me:x"},
		{Network: "whatsapp", Account: "home"},
		{Network: "whatsapp", Account: "work"},
	}
	phases := []Phase{PhaseLoggedOut, PhaseConnecting, PhaseOnline, PhaseFailed}
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 1)) // #nosec G404 -- reproducible
		s := NewState()
		waiting := map[string]bool{}
		var expect []NetworkStatus
		for _, a := range accounts {
			if rng.IntN(2) == 0 {
				expect = append(expect, a)
				waiting[a.key()] = true
			}
		}
		s.Expect(expect...)
		failed := false
		latest := map[string]Phase{}
		online := map[string]time.Time{}
		at := time.Unix(1_700_000_000, 0)
		for step := range rng.IntN(12) {
			at = at.Add(time.Second)
			switch rng.IntN(6) {
			case 0:
				s.Failed(errors.New("stopped"))
				failed = true
			case 1:
				s.Synced(at)
				failed = false
			default:
				a := accounts[rng.IntN(len(accounts))]
				a.Phase = phases[rng.IntN(len(phases))]
				a.Detail = fmt.Sprint(step)
				s.Report(a, at)
				latest[a.key()] = a.Phase
				if a.Phase != PhaseConnecting {
					delete(waiting, a.key())
				}
				if a.Phase == PhaseOnline {
					failed = false
					online[a.key()] = at
				}
			}
		}
		want := !failed && len(waiting) == 0
		if ready, _, _ := s.Snapshot(); ready != want {
			t.Errorf("seed %d: ready = %t, want %t (failed %t, waiting for %d)", seed, ready, want, failed, len(waiting))
		}
		rows := s.Networks(t.Context(), at)
		if len(rows) != len(latest) {
			t.Errorf("seed %d: %d rows, want one per account reported (%d)", seed, len(rows), len(latest))
		}
		for _, row := range rows {
			if row.Phase != latest[row.key()] || !row.At.Equal(online[row.key()]) {
				t.Errorf("seed %d: row %s/%s = %v at %v, want %v at %v", seed, row.Network, row.Account,
					row.Phase, row.At, latest[row.key()], online[row.key()])
			}
		}
	}
}

// Each phase crosses the wire as its own value.
func TestPhaseToProto(t *testing.T) {
	t.Parallel()
	seen := map[v1.NetworkPhase]bool{}
	for _, p := range []Phase{PhaseLoggedOut, PhaseConnecting, PhaseOnline, PhaseFailed} {
		got := phaseToProto(p)
		if got == v1.NetworkPhase_NETWORK_PHASE_UNSPECIFIED || seen[got] {
			t.Errorf("phaseToProto(%d) = %v, want a value of its own", p, got)
		}
		seen[got] = true
		if back := protoToPhase(got); back != p {
			t.Errorf("protoToPhase(%v) = %d, want %d back", got, back, p)
		}
	}
}

// heardStep is one thing that happens to an account in TestStateHeldFollowsItsConnection.
type heardStep int

const (
	stepOnline      heardStep = iota // reported online: a new connection if it was not
	stepConnecting                   // reported connecting: the connection is gone
	stepLoggedOut                    // reported logged out: the session is gone
	stepAlive                        // one answer, now
	stepAnswering                    // the keep-alive answers from now on
	stepSilent                       // this connection's keep-alive stopped; last answer 5 s ago
	stepSilentFirst                  // this connection's first keep-alive failed: last answer its start
	stepSilentOld                    // an earlier connection's silence, heard late
	stepCaughtUp                     // the queue drained
	stepNotOurs                      // stuck the same on every bridge
	stepOurs                         // no longer
	stepWait                         // most of a window passes: two pass it
)

// heardSteps is how many kinds of step there are.
const heardSteps = int(stepWait) + 1

// heldModel is what an account is, tracked by connection rather than by the times State
// has to go by.
type heldModel struct {
	reported, online, notOurs bool
	conn                      int // the current connection; a new one at each going online
	caughtUp, answering       int // the connection caught up or answering; -1 none
	lastAlive, lastOffline    time.Time
	offlineSeen               bool
}

// TestStateHeldFollowsItsConnection is the failure review of the heartbeat (CONTRIBUTING.md:
// it caches a derived result, held, from events). Every sequence of five steps from each
// named start is checked after every step: held exactly when the account is online,
// caught up on this connection, and alive within the window (answering on this
// connection, or an answer within it), or stuck for a reason every bridge shares. The
// blind spots it adds: a reconnect, which must take caught up away until the next
// drain; an earlier connection's silence heard after a reconnect; a keep-alive whose
// last answer is from before the connection said it answers; a quiet account; going
// online while already online.
func TestStateHeldFollowsItsConnection(t *testing.T) {
	t.Parallel()
	const network = "Signal"
	window := 90 * time.Second
	starts := map[string][]heardStep{
		"fresh":             nil,
		"held by answering": {stepOnline, stepAnswering, stepCaughtUp},
		"held by an answer": {stepOnline, stepAlive, stepCaughtUp},
		"reconnected":       {stepOnline, stepAnswering, stepCaughtUp, stepConnecting, stepOnline},
	}
	const depth = 5
	for name, start := range starts {
		seq := make([]heardStep, depth)
		var walk func(i int)
		walk = func(i int) {
			if i == depth {
				checkHeld(t, name, append(slices.Clone(start), seq...), network, window)
				return
			}
			for s := range heardSteps {
				seq[i] = heardStep(s)
				walk(i + 1)
			}
		}
		walk(0)
	}
}

// checkHeld runs steps on a State and on the model, comparing held after each.
func checkHeld(t *testing.T, start string, steps []heardStep, network string, window time.Duration) {
	t.Helper()
	s := NewState()
	m := heldModel{caughtUp: -1, answering: -1}
	now := time.Unix(1_700_000_000, 0)
	account := NetworkStatus{Network: network, Account: "home"}
	heard := func(kind domain.HeardKind, at time.Time) {
		s.Heard(domain.Heard{Network: domain.Protocol(network), Account: "home", Kind: kind, At: at})
	}
	report := func(p Phase) {
		a := account
		a.Phase = p
		s.Report(a, now)
		m.reported = true
		switch {
		case p == PhaseOnline && !m.online:
			m.online = true
			m.conn++
		case p != PhaseOnline:
			if m.answering == m.conn && m.online {
				m.lastAlive = later(m.lastAlive, now)
			}
			m.online, m.answering, m.lastOffline, m.offlineSeen = false, -1, now, true
			if p == PhaseLoggedOut {
				m.notOurs = false
			}
		}
	}
	for i, step := range steps {
		now = now.Add(10 * time.Second)
		switch step {
		case stepOnline:
			report(PhaseOnline)
		case stepConnecting:
			report(PhaseConnecting)
		case stepLoggedOut:
			report(PhaseLoggedOut)
		case stepAlive:
			heard(domain.HeardAlive, now)
			if m.reported {
				m.lastAlive = later(m.lastAlive, now)
			}
		case stepAnswering:
			heard(domain.HeardAnswering, now)
			if m.online {
				m.answering = m.conn
				m.lastAlive = later(m.lastAlive, now)
			}
		case stepSilent:
			// A keep-alive loop starts with its connection, after the last one went.
			last := now.Add(-5 * time.Second)
			if m.offlineSeen {
				last = later(last, m.lastOffline.Add(time.Second))
			}
			heard(domain.HeardSilent, last)
			if m.answering == m.conn && m.online {
				m.answering = -1
				m.lastAlive = later(m.lastAlive, last)
			}
		case stepSilentFirst:
			// whatsmeow's loop starts before Connected: its last answer is before the
			// connection said it answers.
			last := time.Unix(1_700_000_000, 0)
			if m.offlineSeen {
				last = m.lastOffline.Add(time.Second)
			}
			heard(domain.HeardSilent, last)
			if m.answering == m.conn && m.online {
				m.answering = -1
				m.lastAlive = later(m.lastAlive, last)
			}
		case stepSilentOld:
			if m.offlineSeen {
				heard(domain.HeardSilent, m.lastOffline.Add(-time.Second))
			}
		case stepCaughtUp:
			heard(domain.HeardCaughtUp, now)
			if m.online {
				m.caughtUp = m.conn
			}
		case stepNotOurs, stepOurs:
			heard(map[bool]domain.HeardKind{true: domain.HeardNotOurs, false: domain.HeardOurs}[step == stepNotOurs], now)
			if m.reported {
				m.notOurs = step == stepNotOurs
			}
		case stepWait:
			now = now.Add(window - 20*time.Second)
		}
		alive := m.answering == m.conn && m.online || !m.lastAlive.IsZero() && now.Sub(m.lastAlive) <= window
		want := m.reported && (m.notOurs || m.online && m.caughtUp == m.conn && alive)
		got := false
		var why string
		if rows := s.Networks(t.Context(), now); len(rows) == 1 {
			got, why = rows[0].Held(now)
		}
		if got != want {
			t.Fatalf("from %q, steps %v: after step %d held = %t (%s), want %t", start, steps, i, got, why, want)
		}
	}
}

// Each hour's longest gap between alive evidence is logged once the hour is over, the
// measure that sets the windows not decided yet; a gap across going offline is not one.
func TestStateLogsTheLongestAliveGapEachHour(t *testing.T) {
	t.Parallel()
	var logged bytes.Buffer
	s := NewState()
	s.UseLogger(slog.New(slog.NewTextHandler(&logged, nil)))
	at := time.Unix(1_700_000_000, 0)
	account := NetworkStatus{Network: "LinkedIn", Account: "work"}
	report := func(p Phase, after time.Duration) {
		at = at.Add(after)
		a := account
		a.Phase = p
		s.Report(a, at)
	}
	alive := func(after time.Duration) {
		at = at.Add(after)
		s.Heard(domain.Heard{Network: domain.ProtocolLinkedIn, Account: "work", Kind: domain.HeardAlive, At: at})
	}
	report(PhaseOnline, 0)
	alive(0)
	alive(40 * time.Second)
	alive(70 * time.Second)
	report(PhaseConnecting, 0)
	report(PhaseOnline, 10*time.Minute) // offline for ten minutes: not a gap
	alive(0)
	alive(50 * time.Second)
	if logged.Len() != 0 {
		t.Fatalf("logged within the hour: %s", logged.String())
	}
	alive(time.Hour)
	if got := logged.String(); !strings.Contains(got, "alive gaps") || !strings.Contains(got, "longest=1m10s") {
		t.Errorf("the hour's line = %q, want the longest gap 1m10s", got)
	}
}
