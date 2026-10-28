package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// A tool called while kithd cannot start yet answers at once that kithd is waiting,
// and why, rather than a bare "unavailable".
func TestAToolSaysKithdIsWaitingAndWhy(t *testing.T) {
	t.Parallel()
	const why = "the cache is locked: the keyring is not answering; unlock it"
	socket := filepath.Join(t.TempDir(), "kithd.sock")
	ln, err := daemon.Listen(t.Context(), socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- daemon.ServeWaiting(ctx, ln, func() string { return why }) }()
	t.Cleanup(func() { cancel(); <-served })

	s := &server{backend: daemon.NewRemote(socket), scope: shareAll, log: logging.Discard()}
	start := time.Now()
	said := callErr(t, s, "list_rooms", nil)
	// Not a readiness timeout (a minute) first; a third of it leaves a loaded machine room.
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("list_rooms took %s to answer", took)
	}
	if !strings.Contains(said, why) {
		t.Errorf("list_rooms said %q, want it to say kithd is waiting: %s", said, why)
	}
}
