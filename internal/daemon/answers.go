package daemon

import (
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// answers is which steps of which verifications a client has answered (server.answer).
// A claim is held while its answer is sent, and kept once the answer was given: kithd
// answers a handful of verifications in its life, a short key for each step.
type answers struct {
	mu    sync.Mutex
	given map[answered]bool
}

// answered is one step of one verification: its transaction, and the answer
// (VerificationAccepted or VerificationConfirmed).
type answered struct {
	txn  string
	step domain.VerificationKind
}

// claim takes step of txn for one answer, false when another holds it or gave it.
func (a *answers) claim(txn string, step domain.VerificationKind) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := answered{txn: txn, step: step}
	if a.given[k] {
		return false
	}
	if a.given == nil {
		a.given = map[answered]bool{}
	}
	a.given[k] = true
	return true
}

// release gives back a claim whose answer was not sent.
func (a *answers) release(txn string, step domain.VerificationKind) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.given, answered{txn: txn, step: step})
}
