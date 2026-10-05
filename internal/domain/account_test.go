package domain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A clean shutdown must not look like a failure.
func TestSyncFaultSeparatesShutdownFromFailure(t *testing.T) {
	t.Parallel()

	live := context.Background()
	stopped, cancel := context.WithCancel(context.Background())
	cancel()

	real := errors.New("sync: invalid access token")
	tests := map[string]struct {
		ctx  context.Context
		err  error
		want error
	}{
		"real failure while running":   {live, real, real},
		"no error":                     {live, nil, nil},
		"our shutdown, canceled error": {stopped, context.Canceled, nil},
		// Stop() can make the loop return something other than context.Canceled; a
		// done context means it is still our shutdown.
		"our shutdown, other error": {stopped, real, nil},
		// The context has not been canceled but the loop saw a cancellation anyway —
		// still ours: nothing else cancels it.
		"canceled error, live context": {live, context.Canceled, nil},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := domain.SyncFault(tc.ctx, tc.err); !errors.Is(got, tc.want) {
				t.Errorf("SyncFault() = %v, want %v", got, tc.want)
			}
		})
	}
}
