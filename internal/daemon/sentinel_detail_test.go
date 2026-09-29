package daemon

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
)

// A sentinel's identity has to survive the socket, and so does the reason behind it when
// there is one.
func TestCallErrKeepsTheReasonBehindASentinel(t *testing.T) {
	t.Parallel()

	wire := connect.NewError(connect.CodeInternal,
		errors.New("matrix: the room refused a canonical parent: M_FORBIDDEN (You don't have permission to post that to the room)"))
	wire.Meta().Set(sentinelHeader, "no-space-parent")

	err := callErr("add to space", wire)
	if !errors.Is(err, api.ErrNoSpaceParent) {
		t.Fatalf("callErr() = %v, want it to still be the sentinel", err)
	}
	if !strings.Contains(err.Error(), "M_FORBIDDEN") {
		t.Errorf("callErr() = %q, want the daemon's reason kept", err)
	}
}

// A sentinel whose message is the whole story is not repeated: "encryption is not
// enabled: encryption is not enabled" reads like a bug in the client.
func TestCallErrDoesNotRepeatASelfExplanatorySentinel(t *testing.T) {
	t.Parallel()

	wire := connect.NewError(connect.CodeFailedPrecondition, api.ErrNoEncryption)
	wire.Meta().Set(sentinelHeader, "no-encryption")

	err := callErr("verify", wire)
	if !errors.Is(err, api.ErrNoEncryption) {
		t.Fatalf("callErr() = %v, want the sentinel", err)
	}
	if got := err.Error(); got != "daemon: verify: "+api.ErrNoEncryption.Error() {
		t.Errorf("callErr() = %q, want it unrepeated", got)
	}
}

// A plain backend failure keeps the daemon's reason word for word, without Connect's
// "internal:" prefix, and is still a *connect.Error underneath.
func TestCallErrKeepsAPlainFailuresReason(t *testing.T) {
	t.Parallel()

	wire := connect.NewError(connect.CodeInternal,
		errors.New("matrix: mark room read: M_BAD_JSON (HTTP 400): Invalid JSON"))
	err := callErr("mark rooms read", wire)
	if got, want := err.Error(), "daemon: mark rooms read: matrix: mark room read: M_BAD_JSON (HTTP 400): Invalid JSON"; got != want {
		t.Errorf("callErr() = %q, want %q", got, want)
	}
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInternal {
		t.Errorf("callErr() = %v, want it to unwrap to the Connect error", err)
	}
	if got := callErr("x", connect.NewError(connect.CodeUnavailable, errors.New("socket gone"))).Error(); !strings.Contains(got, "socket gone") {
		t.Errorf("a non-internal failure lost its text: %q", got)
	}
}
