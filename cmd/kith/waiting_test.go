package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// syncBuffer is a writer the test reads while waitOut writes it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Opening kith while kithd waits says why at once and that kith opens by itself;
// Ctrl-C ends the wait.
func TestOpeningKithWhileKithdWaitsSaysWhy(t *testing.T) {
	t.Parallel()
	const why = "the cache is locked: the keyring is not answering; unlock it"
	storage := domain.Storage{RuntimeDir: t.TempDir(), Instance: "t"}
	ln, err := daemon.Listen(t.Context(), storage.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, stopServing := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- daemon.ServeWaiting(serveCtx, ln, func() string { return why }) }()
	t.Cleanup(func() { stopServing(); <-served })

	out := &syncBuffer{}
	ctx, quit := context.WithCancel(context.Background())
	returned := make(chan bool, 1)
	go func() { returned <- waitOut(ctx, storage, func(line string) { _, _ = out.Write([]byte(line + "\n")) }) }()

	// Not the readiness timeout (a minute) first; a third of it leaves a loaded machine room.
	const within = 20 * time.Second
	deadline := time.Now().Add(within)
	for !strings.Contains(out.String(), why) {
		if time.Now().After(deadline) {
			t.Fatalf("after %s kith said %q, want why kithd waits", within, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "kith opens by itself") {
		t.Errorf("kith said %q, not that it opens by itself", out.String())
	}
	quit()
	select {
	case started := <-returned:
		if started {
			t.Error("waitOut reported kithd started when quit")
		}
	case <-time.After(within):
		t.Fatal("waitOut did not end on Ctrl-C")
	}
}
