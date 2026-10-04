package slack

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// Over random interleavings of a connection begun on the kept session finishing late,
// fresh sign-ins, and the account leaving and rejoining the config: the account's
// connection is never from an older sign-in than one already adopted, and an account
// not configured has none.
func TestAnOlderSessionNeverReplacesANewerOne(t *testing.T) {
	t.Parallel()
	account := Account{Name: "work", Workspace: "acme"}
	for seed := range uint64(500) {
		rng := rand.New(rand.NewPCG(seed, 41)) // #nosec G404 -- reproducible
		a := New(nil, &memSecrets{values: map[string]string{}}, []Account{account}, nil)
		var pending []int // connections begun, each on the sign-in current then
		newest := 0       // the newest sign-in adopted so far
		configured := true
		for step := range 12 {
			switch rng.IntN(5) {
			case 0: // a connection begins, on whatever session is kept now
				pending = append(pending, a.currentSignIn(account.Name))
			case 1: // one finishes, in any order
				if len(pending) == 0 {
					continue
				}
				i := rng.IntN(len(pending))
				gen := pending[i]
				pending = append(pending[:i], pending[i+1:]...)
				if a.adopt(newWorkspace(account, Credentials{}, "", nil, gen)) {
					newest = max(newest, gen)
				}
			case 2: // signed in afresh
				w := newWorkspace(account, Credentials{}, "", nil, a.newSignIn(account.Name))
				if a.adopt(w) {
					newest = w.signIn
				}
			case 3: // removed from the config
				a.UseAccounts(nil)
				configured = false
			default: // back in it
				a.UseAccounts([]Account{account})
				configured = true
			}
			where := fmt.Sprintf("seed %d step %d", seed, step)
			a.mu.Lock()
			w := a.workspaces[account.Name]
			a.mu.Unlock()
			switch {
			case !configured && w != nil:
				t.Fatalf("%s: a removed account is connected (sign-in %d)", where, w.signIn)
			case w != nil && w.signIn < newest:
				t.Fatalf("%s: connected on sign-in %d after %d was adopted", where, w.signIn, newest)
			}
		}
	}
}
