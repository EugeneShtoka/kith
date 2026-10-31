package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
)

// A call abandoned for its context crosses as that error, not "internal", so errors.Is
// works as for a local call.
func TestContextErrorsCrossTheWireAsThemselves(t *testing.T) {
	t.Parallel()

	cases := []struct {
		err  error
		code connect.Code
	}{
		{fmt.Errorf("matrix: send: %w", context.Canceled), connect.CodeCanceled},
		{fmt.Errorf("matrix: send: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded},
		{errors.New("M_FORBIDDEN"), connect.CodeInternal},
		{fmt.Errorf("local: %w", api.ErrSpellUnavailable), connect.CodeFailedPrecondition},
	}
	for _, c := range cases {
		sent := rpcErr(c.err)
		if got := connect.CodeOf(sent); got != c.code {
			t.Errorf("rpcErr(%v) code = %v, want %v", c.err, got, c.code)
		}
		back := callErr("send", sent)
		for _, ctxErr := range []error{context.Canceled, context.DeadlineExceeded} {
			if want := errors.Is(c.err, ctxErr); errors.Is(back, ctxErr) != want {
				t.Errorf("callErr(%v): errors.Is(%v) = %v, want %v", c.err, ctxErr, !want, want)
			}
		}
	}
}

// An error wrapping two sentinels is tagged the same way every time.
func TestAnErrorWithTwoSentinelsIsTaggedDeterministically(t *testing.T) {
	t.Parallel()

	both := errors.Join(api.ErrUnreachable, api.ErrNoEncryption)
	first, ok := sentinelName(both)
	if !ok {
		t.Fatal("no sentinel found")
	}
	for range 100 {
		if again, _ := sentinelName(both); again != first {
			t.Fatalf("tagged %q, then %q", first, again)
		}
	}
}
