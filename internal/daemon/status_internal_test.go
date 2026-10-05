package daemon

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
)

// A daemon that waits for nothing is ready at once: `kith login` must be able to
// reach a daemon with no network logged in.
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

// Over any sequence of reports, failures and syncs, a daemon that named what it
// waits for is ready exactly when nothing has failed since the last sync and every
// expected account has reported a phase other than connecting; rows keep the latest
// phase per account, and the time each last went online.
func TestStateReadinessFollowsTheExpectedAccounts(t *testing.T) {
	t.Parallel()
	accounts := []NetworkStatus{
		{Network: "matrix", Account: "@me:x"},
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
		rows := s.Networks()
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

// Status carries every network account's row, with when it went online.
func TestStatusCarriesTheNetworks(t *testing.T) {
	t.Parallel()
	state := NewState()
	online := time.Unix(1_700_000_000, 0)
	state.Report(NetworkStatus{Network: "matrix", Account: "@me:x", Phase: PhaseLoggedOut, Detail: "run `kith login`"}, online)
	state.Report(NetworkStatus{Network: "whatsapp", Account: "home", Phase: PhaseOnline}, online)
	resp, err := (&server{Daemon: &Daemon{State: state}}).Status(context.Background(), connect.NewRequest(&v1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	rows := resp.Msg.GetNetworks()
	if len(rows) != 2 {
		t.Fatalf("networks = %v, want two rows", rows)
	}
	if r := rows[0]; r.GetNetwork() != "matrix" || r.GetAccount() != "@me:x" ||
		r.GetPhase() != v1.NetworkPhase_NETWORK_PHASE_LOGGED_OUT || r.GetDetail() == "" || r.GetOnlineAt() != nil {
		t.Errorf("matrix row = %v", r)
	}
	if r := rows[1]; r.GetPhase() != v1.NetworkPhase_NETWORK_PHASE_ONLINE || !r.GetOnlineAt().AsTime().Equal(online) {
		t.Errorf("whatsapp row = %v, want online at %v", r, online)
	}
}
