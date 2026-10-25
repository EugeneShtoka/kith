package main

import (
	"context"
	"strings"
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
	for _, c := range []struct {
		args []string
		want string // substring the error must contain
	}{
		{[]string{}, "usage"},
		{[]string{"notanmxid"}, "not a Matrix user ID"},
		{[]string{"@noserver"}, "not a Matrix user ID"},
		{[]string{"missing-at:server"}, "not a Matrix user ID"},
		{[]string{"matrix:@a:b", "extra"}, "usage"},
	} {
		err := runIgnoreOrUnignore(c.args, "ignore", func(ctx context.Context, b ignoreBackend, mxid string) error {
			return do(ctx, backend, mxid)
		})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want contains %q", c.args, err, c.want)
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
