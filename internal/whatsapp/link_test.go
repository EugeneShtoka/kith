package whatsapp

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// links records what OnLink heard.
type links struct {
	mu  sync.Mutex
	got []string // "name link detail"
}

func (l *links) add(account Account, link Link, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, account.Name+" "+[]string{"?", "unlinked", "connecting", "connected"}[link]+" "+detail)
}

func (l *links) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.got...)
}

// Linked is the configured accounts with a device, the ones the daemon waits for.
func TestLinkedIsTheAccountsWithADevice(t *testing.T) {
	t.Parallel()
	bg, il := Account{Name: "bg", Digits: ownDigits}, Account{Name: "il", Digits: "972500000001"}
	a, _, store := offline(t, bg, il)
	linkedClient(t, store, ownDigits)
	got, err := a.Linked(context.Background())
	if err != nil || len(got) != 1 || got[0] != bg {
		t.Errorf("Linked = (%v, %v), want bg alone", got, err)
	}
}

// An account says how it is linked as that changes: not linked at Start, connected,
// reconnecting, unlinked by the phone, dropped from the config; one added to the
// config says it is not linked yet. A client already replaced says nothing.
func TestAnAccountReportsItsLink(t *testing.T) {
	t.Parallel()
	account := Account{Name: "bg", Digits: ownDigits}
	a, _, store := offline(t, account)
	l := &links{}
	a.OnLink(l.add)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Start(ctx) }()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(5 * time.Second); len(l.all()) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("Start reported nothing")
		}
	}
	if got := l.all()[0]; !strings.HasPrefix(got, "bg unlinked ") || !strings.Contains(got, "kith login whatsapp bg") {
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
	a.UseAccounts(ctx, []Account{{Name: "il", Digits: "972500000001"}})

	got := l.all()[1:]
	want := []string{
		"bg connected ", "bg connecting disconnected; reconnecting", "bg unlinked the phone unlinked kith",
		"bg unlinked no longer in the config", "il unlinked not linked yet",
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
