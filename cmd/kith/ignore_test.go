package main

import (
	"context"
	"sync"
	"testing"
)

// TestIgnoreUserValidation checks that runIgnoreOrUnignore refuses a syntactically
// invalid MXID before it reaches the daemon. A real homeserver would error too,
// but this way kith does not spin up a daemon connection for garbage input.
func TestIgnoreUserValidation(t *testing.T) {
	t.Parallel()
	backend := &recordingIgnoreBackend{}
	do := func(ctx context.Context, b ignoreBackend, mxid string) error { return b.Ignore(ctx, mxid) }
	for _, args := range [][]string{
		{},
		{"notanmxid"},
		{"@noserver"},
		{"missing-at:server"},
		{"matrix:@a:b", "extra"},
	} {
		err := runIgnoreOrUnignore(args, "ignore", func(ctx context.Context, b ignoreBackend, mxid string) error {
			return do(ctx, backend, mxid)
		})
		if err == nil {
			t.Errorf("%v: err = nil, want refused", args)
		}
	}
	if backend.calls != 0 {
		t.Errorf("backend reached %d time(s); validation was supposed to short-circuit", backend.calls)
	}
}

// recordingIgnoreBackend fulfills the ignoreBackend interface for the validator test
// so the subcommand does not need a running daemon.
type recordingIgnoreBackend struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (r *recordingIgnoreBackend) Ignore(_ context.Context, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.err
}

func (r *recordingIgnoreBackend) Unignore(_ context.Context, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.err
}
