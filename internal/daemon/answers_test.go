package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

var errHomeserver = errors.New("matrix: the homeserver did not take the event")

// answering holds each verification answer that reaches it until the test lets it
// go, and fails the ones it is told to: so the test decides what every other client
// sends while one answer is on its way.
type answering struct {
	apitest.Nop
	mu      sync.Mutex
	reached []string
	arrived chan struct{}
	release chan error
}

func (a *answering) answer(what string) error {
	a.mu.Lock()
	a.reached = append(a.reached, what)
	a.mu.Unlock()
	a.arrived <- struct{}{}
	return <-a.release
}

func (a *answering) AcceptVerification(_ context.Context, txn string) error {
	return a.answer("accept " + txn)
}

func (a *answering) ConfirmSAS(_ context.Context, txn string) error {
	return a.answer("confirm " + txn)
}

// A request and its emoji are shown to every client (a TUI and a desktop window, two
// TUIs in two profiles), so any of them may answer, at once or one after another:
// kithd sends one answer to each step, refuses every other while one is on its way or
// once it was given, takes one again after a failed one, and tells every client of
// each answer given, so none offers it again. A second confirm would send a second
// MAC, which the other device cancels the verification on.
func TestEachVerificationStepTakesOneAnswer(t *testing.T) {
	t.Parallel()

	b := &answering{
		Nop:     apitest.Nop{Msgs: make(chan domain.Message), Verifs: make(chan domain.Verification)},
		arrived: make(chan struct{}),
		release: make(chan error),
	}
	h := serve(t, b)
	clients := []*daemon.Remote{h.client(), h.client(), h.client()}
	for _, c := range clients {
		go func() { _ = c.Start(context.Background()) }()
	}
	h.waitAttached(len(clients))

	type step struct {
		txn  string
		kind domain.VerificationKind
	}
	given := map[step]bool{}
	reached := 0
	rng := rand.New(rand.NewPCG(7, 431))
	for round := range 60 {
		s := step{txn: fmt.Sprintf("t%d", rng.IntN(3)), kind: domain.VerificationAccepted}
		if rng.IntN(2) == 0 {
			s.kind = domain.VerificationConfirmed
		}
		fails := rng.IntN(3) == 0
		// 1 to 3 clients answer at once.
		answerers := clients[:1+rng.IntN(len(clients))]
		errs := make(chan error, len(answerers))
		for _, c := range answerers {
			go func() {
				if s.kind == domain.VerificationAccepted {
					errs <- c.AcceptVerification(context.Background(), s.txn)
				} else {
					errs <- c.ConfirmSAS(context.Background(), s.txn)
				}
			}()
		}
		var sent bool
		var refused, failed, ok int
		count := func(err error) {
			switch {
			case errors.Is(err, api.ErrAnswered):
				refused++
			case err != nil:
				failed++
			default:
				ok++
			}
		}
		if !given[s] {
			// One answer reaches the backend; the others are refused while it is held.
			take(t, b.arrived, fmt.Sprintf("round %d: an answer reaching the backend", round))
			sent = true
			for range len(answerers) - 1 {
				count(take(t, errs, fmt.Sprintf("round %d: a second answer", round)))
			}
			if fails {
				b.release <- errHomeserver
			} else {
				b.release <- nil
			}
		}
		for refused+failed+ok < len(answerers) {
			count(take(t, errs, fmt.Sprintf("round %d: an answer", round)))
		}

		wantRefused := len(answerers)
		if sent {
			reached++
			wantRefused--
			if !fails {
				given[s] = true
			}
		}
		if wantOK := boolInt(sent && !fails); ok != wantOK || failed != boolInt(sent && fails) || refused != wantRefused {
			t.Fatalf("round %d (%v, %d clients, fails %t, given before %t): %d given, %d failed, %d refused; want %d, %d, %d",
				round, s, len(answerers), fails, !sent, ok, failed, refused, wantOK, boolInt(sent && fails), wantRefused)
		}
		if sent && !fails {
			// Every client is told, the ones that did not answer too.
			for i, c := range clients {
				got := take(t, c.Verifications(), fmt.Sprintf("round %d: client %d told", round, i))
				if got.Kind != s.kind || got.TxnID != s.txn {
					t.Fatalf("round %d: client %d was told %+v, want %v of %s", round, i, got, s.kind, s.txn)
				}
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.reached) != reached {
		t.Errorf("the backend was sent %d answers, want %d: %v", len(b.reached), reached, b.reached)
	}
	for _, c := range clients {
		select {
		case v := <-c.Verifications():
			t.Errorf("a client was told of %+v, which no answer gave", v)
		default:
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
