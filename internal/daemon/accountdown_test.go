package daemon_test

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// accountNotifier is the daemon's notifier with a recorder for its popups.
func accountNotifier(t *testing.T) (*daemon.Notifications, *recorder) {
	t.Helper()
	rec := &recorder{}
	n, err := daemon.NewNotifications(asStarted(notifsOn("all")), world(), func(config.Notifications) notify.Notifier { return rec })
	if err != nil {
		t.Fatal(err)
	}
	return n, rec
}

// Of every change from one phase (or none) to another, exactly an account with a
// session going logged out or failed raises a popup, naming it and saying why.
func TestOnlyAnAccountWithASessionFallingRaisesAPopup(t *testing.T) {
	t.Parallel()
	phases := []domain.AccountPhase{0, domain.AccountLoggedOut, domain.AccountConnecting, domain.AccountOnline, domain.AccountFailed}
	for _, was := range phases {
		for _, now := range phases[1:] {
			n, rec := accountNotifier(t)
			n.AccountChanged(domain.AccountStatus{Network: domain.ProtocolGMessages, Account: "SMS", Phase: now, Detail: "run `kith login gmessages SMS`"}, was)
			hadSession := was == domain.AccountConnecting || was == domain.AccountOnline
			fell := now == domain.AccountLoggedOut || now == domain.AccountFailed
			sent := rec.all()
			if want := hadSession && fell; want != (len(sent) == 1) || len(sent) > 1 {
				t.Errorf("%v → %v raised %+v, want a popup: %v", was, now, sent, want)
				continue
			}
			if len(sent) == 1 && (sent[0].Room != "Google Messages SMS" || sent[0].Body != "run `kith login gmessages SMS`") {
				t.Errorf("%v → %v raised %+v, want the account and why", was, now, sent[0])
			}
		}
	}
}

// Over State's own reports: an account started with a session that fails to connect,
// one that falls while online, retries, a repeated report and an account never
// logged in each raise what they should, once.
func TestAnAccountFallingIsToldOncePerFall(t *testing.T) {
	t.Parallel()
	n, rec := accountNotifier(t)
	state := daemon.NewState()
	sms := daemon.NetworkStatus{Network: string(domain.ProtocolGMessages), Account: "SMS"}
	state.Expect(sms)
	report := func(network domain.Protocol, account string, phase domain.AccountPhase) {
		s := domain.AccountStatus{Network: network, Account: account, Phase: phase}
		n.AccountChanged(s, state.Report(daemon.StatusOf(s), time.Now()))
	}
	steps := []struct {
		network domain.Protocol
		account string
		phase   domain.AccountPhase
		popups  int // the total so far
	}{
		{domain.ProtocolGMessages, "SMS", domain.AccountLoggedOut, 1}, // refused at start
		{domain.ProtocolGMessages, "SMS", domain.AccountLoggedOut, 1}, // said again
		{domain.ProtocolGMessages, "SMS", domain.AccountConnecting, 1},
		{domain.ProtocolGMessages, "SMS", domain.AccountOnline, 1},
		{domain.ProtocolGMessages, "SMS", domain.AccountConnecting, 1}, // a retry
		{domain.ProtocolGMessages, "SMS", domain.AccountOnline, 1},
		{domain.ProtocolGMessages, "SMS", domain.AccountFailed, 2},   // falls while online
		{domain.ProtocolInstagram, "me", domain.AccountLoggedOut, 2}, // never logged in
		{domain.ProtocolInstagram, "me", domain.AccountConnecting, 2},
		{domain.ProtocolInstagram, "me", domain.AccountLoggedOut, 3}, // a login that failed
	}
	for i, step := range steps {
		report(step.network, step.account, step.phase)
		if got := len(rec.all()); got != step.popups {
			t.Fatalf("step %d (%s %s %v): %d popups, want %d", i, step.network, step.account, step.phase, got, step.popups)
		}
	}
}
