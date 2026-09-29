package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
)

// A stream with nothing to say (verifications, follows: quiet for hours) stays attached
// past the client's answer timeout, which bounds the wait for a call's first header:
// a stream's headers go out when it opens, not with its first event.
func TestAQuietStreamOutlivesTheAnswerTimeout(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "d.sock")
	ln, err := Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &apitest.Nop{}
	streams := NewStreams()
	go streams.Run(ctx, b)
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, ln, &Daemon{Backend: b, Streams: streams, State: NewState()}) }()
	t.Cleanup(func() { cancel(); <-served })

	const answer = 300 * time.Millisecond
	r := newRemote(dialWithAnswerTimeout(path, answer))
	go func() { _ = r.Start(ctx) }()
	t.Cleanup(r.Stop)

	deadline := time.Now().Add(5 * time.Second)
	for streams.Attached() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if streams.Attached() < 1 {
		t.Fatal("never attached")
	}
	select {
	case attached := <-r.Attached():
		t.Fatalf("attachment changed to %v with the daemon up: a quiet stream hit the answer timeout", attached)
	case <-time.After(6 * answer):
	}
}
