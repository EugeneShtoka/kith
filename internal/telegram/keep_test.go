package telegram

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

// Credentials are written by finishing logins and by connections renewing sessions.
// For any order, the stored value is the latest login's as its own connection last
// wrote it; a removed account stores nothing.

// bump starts a new login generation, as beginLogin does.
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
		a := New(nil, secrets, nil, []Account{home}, nil)
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
					a.useAccounts([]Account{home})
				} else {
					a.useAccounts(nil)
				}
				continue
			case 4:
				// The same number under another name: not the account that logged in.
				trace = append(trace, "renamed")
				a.useAccounts([]Account{{Name: "other", Digits: home.Digits}})
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

// slowSecrets writes slowly, like a D-Bus keyring, widening the check-to-write window.
type slowSecrets struct{ *memSecrets }

func (s slowSecrets) StoreSecret(ref, value string) error {
	time.Sleep(time.Duration(rand.IntN(300)) * time.Microsecond)
	return s.memSecrets.StoreSecret(ref, value)
}

// An old connection renewing its session never overwrites a newer login's.
func TestAStaleRenewalNeverOutlastsANewerLogin(t *testing.T) {
	t.Parallel()
	for round := range 200 {
		secrets := &memSecrets{values: map[string]string{}}
		a := New(nil, slowSecrets{secrets}, nil, []Account{home}, nil)
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
