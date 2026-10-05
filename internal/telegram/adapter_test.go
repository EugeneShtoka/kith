package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// memSecrets is a secret store in memory; fail makes every read fail.
type memSecrets struct {
	mu     sync.Mutex
	values map[string]string
	fail   error
}

func (m *memSecrets) Secret(ref string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return "", false, m.fail
	}
	v, ok := m.values[ref]
	return v, ok, nil
}

func (m *memSecrets) StoreSecret(ref, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[ref] = value
	return nil
}

func (m *memSecrets) DeleteSecret(ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, ref)
	return nil
}

func loggedIn(t *testing.T, secrets *memSecrets, digits string) {
	t.Helper()
	blob, err := json.Marshal(Credentials{App: App{ID: 1, Hash: "h"}, Session: []byte("session"), User: 42})
	if err != nil {
		t.Fatal(err)
	}
	secrets.values[credentialsRef(digits)] = string(blob)
}

// sessions records each account's reported state.
type sessions struct {
	mu   sync.Mutex
	seen map[string]domain.AccountPhase
	said map[string]string
}

func (s *sessions) hear(status domain.AccountStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[status.Account], s.said[status.Account] = status.Phase, status.Detail
}

func (s *sessions) of(name string) (domain.AccountPhase, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[name], s.said[name]
}

// started runs an adapter over secrets for accounts, recording what each says, and
// stops it with the test.
func started(t *testing.T, secrets Secrets, accounts ...Account) (*Adapter, *sessions) {
	t.Helper()
	heard := &sessions{seen: map[string]domain.AccountPhase{}, said: map[string]string{}}
	a := New(nil, secrets, accounts, nil)
	a.OnStatus(heard.hear)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		a.Stop()
	})
	return a, heard
}

// eventually waits for cond, failing after a second.
func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal(what)
}

var (
	home = Account{Name: "home", Digits: "447700900000"}
	work = Account{Name: "work", Digits: "12025550100"}
)

// Starting says of each account whether it is logged in: one with credentials is
// connecting, one without is told how to log in, by its name.
func TestStartSaysWhoIsLoggedIn(t *testing.T) {
	t.Parallel()
	secrets := &memSecrets{values: map[string]string{}}
	loggedIn(t, secrets, home.Digits)
	_, heard := started(t, secrets, home, work)
	eventually(t, func() bool { s, _ := heard.of("work"); return s != 0 }, "work said nothing")
	eventually(t, func() bool { s, _ := heard.of("home"); return s != 0 }, "home said nothing")
	if s, _ := heard.of("home"); s != Connecting {
		t.Errorf("home = %v, want connecting", s)
	}
	if s, said := heard.of("work"); s != LoggedOut || !strings.Contains(said, ":login telegram") || !strings.Contains(said, "kith login telegram work") {
		t.Errorf("work = %v %q, want logged out with how to log in", s, said)
	}
}

// Credentials that cannot be read, or are kept but incomplete, leave the account
// logged out, saying why.
func TestUnreadableCredentialsSaySo(t *testing.T) {
	t.Parallel()
	_, heard := started(t, &memSecrets{fail: errors.New("keyring locked")}, home)
	eventually(t, func() bool { s, _ := heard.of("home"); return s != 0 }, "home said nothing")
	if s, said := heard.of("home"); s != LoggedOut || !strings.Contains(said, "keyring locked") {
		t.Errorf("home = %v %q, want logged out saying why", s, said)
	}

	partial := &memSecrets{values: map[string]string{credentialsRef(work.Digits): `{"app":{"api_id":1,"api_hash":"h"},"user":42}`}}
	if _, ok, err := loadCredentials(partial, work.Digits); ok || !errors.Is(err, errUnusable) {
		t.Errorf("credentials without a session = (%v, %v), want unusable", ok, err)
	}
}

// An account added when the config is re-read says whether it is logged in.
func TestAnAddedAccountIsAnnounced(t *testing.T) {
	t.Parallel()
	a, heard := started(t, &memSecrets{values: map[string]string{}}, home)
	eventually(t, func() bool { s, _ := heard.of("home"); return s != 0 }, "home said nothing")
	a.useAccounts([]Account{home, work})
	eventually(t, func() bool { s, _ := heard.of("work"); return s == LoggedOut }, "work, added, said nothing")
}

// Telegram rooms are not end-to-end encrypted.
func TestTelegramRoomsAreNotEncrypted(t *testing.T) {
	t.Parallel()
	a := New(nil, &memSecrets{values: map[string]string{}}, []Account{home}, nil)
	room := domain.RoomID("telegram:42/-1001")
	if enc, err := a.RoomEncryption(t.Context(), []domain.RoomID{room}); err != nil || enc[room] {
		t.Errorf("RoomEncryption = (%v, %v), want not encrypted", enc, err)
	}
}

// UseConfig reads [[telegram.account]], each number as its digits.
func TestUseConfigReadsTheTelegramSection(t *testing.T) {
	t.Parallel()
	a := New(nil, &memSecrets{values: map[string]string{}}, nil, nil)
	a.UseConfig(t.Context(), config.Config{Telegram: config.Telegram{Accounts: []config.TelegramAccount{{Name: "home", Phone: "+44 7700 900000"}}}})
	if got := a.accountsNow(); len(got) != 1 || got[0] != home {
		t.Errorf("accounts = %+v, want home", got)
	}
}
