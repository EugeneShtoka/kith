package main

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/daemon"
)

// The notice names each logged-out account with how to log it in, and nothing else.
func TestLoggedOutNotice(t *testing.T) {
	t.Parallel()
	rows := []daemon.NetworkStatus{
		{Network: "Matrix", Account: "@me:x", Phase: daemon.PhaseLoggedOut, Detail: "no saved session; run `kith login`"},
		{Network: "WhatsApp", Account: "home", Phase: daemon.PhaseOnline},
		{Network: "WhatsApp", Account: "work", Phase: daemon.PhaseLoggedOut, Detail: "not linked yet; run `kith login whatsapp work`"},
		{Network: "WhatsApp", Account: "us", Phase: daemon.PhaseConnecting},
	}
	got := loggedOutNotice(rows)
	for _, want := range []string{"Matrix @me:x is logged out: no saved session; run `kith login`", "WhatsApp work is logged out", "kith login whatsapp work"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "home") || strings.Contains(got, "us") {
		t.Errorf("notice = %q, names an account that is not logged out", got)
	}
	if got := loggedOutNotice(rows[1:2]); got != "" {
		t.Errorf("notice with everything online = %q, want none", got)
	}
}
