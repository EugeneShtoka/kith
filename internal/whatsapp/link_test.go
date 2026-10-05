package whatsapp

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"go.mau.fi/whatsmeow/types/events"
)

// links records what OnLink heard.
type links struct {
	mu  sync.Mutex
	got []string // "name link detail"
}

func (l *links) add(s domain.AccountStatus) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, s.Account+" "+[]string{"?", "unlinked", "connecting", "connected"}[s.Phase]+" "+s.Detail)
}

func (l *links) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.got...)
}

// SavedSessions is the configured accounts with a device, the ones the daemon waits for.
func TestLinkedIsTheAccountsWithADevice(t *testing.T) {
	t.Parallel()
	home, work := Account{Name: "home", Digits: ownDigits}, Account{Name: "work", Digits: "1500000001"}
	a, _, store := offline(t, home, work)
	linkedClient(t, store, ownDigits)
	got, err := a.SavedSessions(context.Background())
	if err != nil || !slices.Equal(got, []string{home.Name}) {
		t.Errorf("SavedSessions = (%v, %v), want home alone", got, err)
	}
}

// An account says how it is linked as that changes: not linked at Start, connected,
// reconnecting, unlinked by the phone, dropped from the config; one added to the
// config says it is not linked yet. A client already replaced says nothing.
func TestAnAccountReportsItsLink(t *testing.T) {
	t.Parallel()
	account := Account{Name: "home", Digits: ownDigits}
	a, _, store := offline(t, account)
	l := &links{}
	a.OnStatus(l.add)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(5 * time.Second); len(l.all()) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("Start reported nothing")
		}
	}
	if got := l.all()[0]; !strings.HasPrefix(got, "home unlinked ") || !strings.Contains(got, "kith login whatsapp home") {
		t.Errorf("at Start = %q, want unlinked with how to link it", got)
	}

	client := linkedClient(t, store, ownDigits)
	a.mu.Lock()
	a.clients[ownDigits] = client
	a.mu.Unlock()
	a.handle(account, client, &events.Connected{})
	a.handle(account, client, &events.Disconnected{})
	stale := linkedClient(t, store, ownDigits)
	a.handle(account, stale, &events.Disconnected{})
	a.handle(account, client, &events.LoggedOut{})
	a.useAccounts(ctx, []Account{{Name: "work", Digits: "1500000001"}})

	got := l.all()[1:]
	want := []string{
		"home connected ", "home connecting disconnected; reconnecting", "home unlinked the phone unlinked kith",
		"home unlinked no longer in the config", "work unlinked not linked yet",
	}
	if len(got) != len(want) {
		t.Fatalf("reports = %q, want %q", got, want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("report %d = %q, want %q", i, got[i], want[i])
		}
	}
}
