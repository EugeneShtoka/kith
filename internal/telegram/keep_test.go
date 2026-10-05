package telegram

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

// Credentials have several writers: each login when it finishes, and each connection
// as gotd renews its session. A login begun later makes every earlier login and
// connection stale, and an account left the config takes none; whatever the order,
// what is kept is the latest login's, as its own connection last wrote it.

// bump begins a login of account, as beginLogin does: earlier ones are stale.
func bump(a *Adapter, account Account) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.signIns[account.Name]++
	return a.signIns[account.Name]
}

func keptSessionOf(t *testing.T, secrets *memSecrets) string {
	t.Helper()
	creds, ok, err := loadCredentials(secrets, home.Digits)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return ""
	}
	return string(creds.Session)
}

func TestOnlyTheLatestLoginKeepsCredentials(t *testing.T) {
	t.Parallel()
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 7))
		secrets := &memSecrets{values: map[string]string{}}
		a := New(nil, secrets, []Account{home}, nil)
		latest, configured, want := 0, true, ""
		var trace []string
		for step := range 40 {
			gen := rng.IntN(latest + 1) // any login so far, stale or not
			session := fmt.Sprintf("g%d-%d", gen, step)
			creds := Credentials{App: App{ID: 1, Hash: "h"}, Session: []byte(session), User: 42}
			accepted := gen == latest && configured
			switch rng.IntN(5) {
			case 0:
				latest = bump(a, home)
				trace = append(trace, fmt.Sprintf("begin %d", latest))
				continue
			case 1:
				trace = append(trace, "finish "+session)
				_ = a.keepLogin(home, gen, creds)
			case 2:
				trace = append(trace, "renew "+session)
				s := &keptSession{a: a, account: home, gen: gen, creds: creds}
				if err := s.StoreSession(context.Background(), []byte(session)); err != nil {
					t.Fatal(err)
				}
			case 3:
				configured = !configured
				trace = append(trace, fmt.Sprintf("configured %v", configured))
				if configured {
					a.UseAccounts([]Account{home})
				} else {
					a.UseAccounts(nil)
				}
				continue
			case 4:
				// The same number under another name: not the account that logged in.
				trace = append(trace, "renamed")
				a.UseAccounts([]Account{{Name: "other", Digits: home.Digits}})
				configured = false
				continue
			}
			if accepted {
				want = session
			}
			if got := keptSessionOf(t, secrets); got != want {
				t.Fatalf("seed %d: kept %q, want %q after\n%v", seed, got, want, trace)
			}
		}
	}
}

// slowSecrets takes a while to write, as a keyring over D-Bus does: long enough for
// another writer to come and go between a write's check and its landing.
type slowSecrets struct{ *memSecrets }

func (s slowSecrets) StoreSecret(ref, value string) error {
	time.Sleep(time.Duration(rand.IntN(300)) * time.Microsecond)
	return s.memSecrets.StoreSecret(ref, value)
}

// A connection of an earlier login renewing its session while a later login finishes
// never leaves its session over the later one's.
func TestAStaleRenewalNeverOutlastsANewerLogin(t *testing.T) {
	t.Parallel()
	for round := range 200 {
		secrets := &memSecrets{values: map[string]string{}}
		a := New(nil, slowSecrets{secrets}, []Account{home}, nil)
		old := bump(a, home)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for w := range 4 {
			wg.Go(func() {
				<-start
				for i := range 20 {
					s := &keptSession{a: a, account: home, gen: old, creds: Credentials{App: App{ID: 1, Hash: "h"}, User: 42}}
					_ = s.StoreSession(context.Background(), fmt.Appendf(nil, "old-%d-%d", w, i))
				}
			})
		}
		close(start)
		newer := bump(a, home)
		if err := a.keepLogin(home, newer, Credentials{App: App{ID: 1, Hash: "h"}, Session: []byte("new"), User: 42}); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		if got := keptSessionOf(t, secrets); got != "new" {
			t.Fatalf("round %d: kept %q, want the newer login's", round, got)
		}
	}
}
