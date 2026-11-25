package main

import (
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
)

// kith status names kithd's build and says of each account whether it is held, and
// what keeps it stuck: catching up, a quiet keep-alive, its phase; a phone that is
// not answering is held, being the same on every bridge.
func TestShowStatusSaysHeldOrStuckAndWhy(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	rows := []daemon.NetworkStatus{
		{Network: "WhatsApp", Account: "home", Phase: daemon.PhaseOnline, CaughtUp: true, AliveAt: now},
		{Network: "LinkedIn", Account: "work", Phase: daemon.PhaseOnline, CaughtUp: true, AliveAt: now.Add(-4 * time.Minute)},
		{Network: "Signal", Account: "home", Phase: daemon.PhaseOnline, At: now.Add(-time.Minute), AliveAt: now},
		{Network: "Google Messages", Account: "home", Phase: daemon.PhaseOnline, NotOurs: true, Detail: "the phone is not answering"},
		{Network: "Telegram", Account: "home", Phase: daemon.PhaseConnecting, Detail: "Telegram cannot be reached"},
	}
	var out strings.Builder
	showStatus(&out, "dev 0123456789ab", rows, now)
	for _, want := range []string{
		"kithd dev 0123456789ab\n",
		"WhatsApp home: held (heard 0s ago)\n",
		"LinkedIn work: stuck (heard 4m ago): nothing heard within its 3m window\n",
		"Signal home: stuck (heard 0s ago): catching up since it connected",
		"Google Messages home: held (never heard): stuck the same on every bridge: the phone is not answering\n",
		"Telegram home: stuck (never heard): connecting: Telegram cannot be reached\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("kith status lacks %q:\n%s", want, out.String())
		}
	}
}
