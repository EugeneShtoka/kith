package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
)

// sentinelHeader names a contract sentinel alongside a Connect error so the client can
// rebuild it; a status code cannot, as every sentinel would share one.
const sentinelHeader = "Mx-Sentinel"

// sentinels is the set whose identity crosses the wire; the names are protocol. Every
// sentinel in internal/api belongs here (a test asserts it).
var sentinels = map[string]error{
	"no-encryption":     api.ErrNoEncryption,
	"no-key-backup":     api.ErrNoKeyBackup,
	"bad-recovery-key":  api.ErrBadRecoveryKey,
	"no-power":          api.ErrNoPower,
	"no-space-parent":   api.ErrNoSpaceParent,
	"bad-key-file":      api.ErrBadKeyFile,
	"no-room-keys":      api.ErrNoRoomKeys,
	"key-backup-exists": api.ErrKeyBackupExists,
	"bad-password":      api.ErrBadPassword,
	"session-rejected":  api.ErrSessionRejected,
	"unreachable":       api.ErrUnreachable,
	"edits-remain":      api.ErrEditsRemain,
	"spell-unavailable": api.ErrSpellUnavailable,
	"seat-taken":        api.ErrSeatTaken,
	"network-off":       api.ErrNetworkOff,
	"not-on-network":    api.ErrNotOnNetwork,
	"config-moved":      api.ErrConfigMoved,
	"refused":           api.ErrRefused,
	"unknown-profile":   api.ErrUnknownProfile,
	"daemon-waiting":    api.ErrDaemonWaiting,
}

// sentinelOrder fixes the order sentinels are tried in, so an error wrapping two is
// always tagged the same way.
var sentinelOrder = slices.Sorted(maps.Keys(sentinels))

// sentinelName reports the wire name of the sentinel err wraps, if any.
func sentinelName(err error) (string, bool) {
	for _, name := range sentinelOrder {
		if errors.Is(err, sentinels[name]) {
			return name, true
		}
	}
	return "", false
}

// callErr converts a failed RPC into the error the TUI sees, restoring a tagged
// sentinel so errors.Is works across the socket. op names the call.
func callErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if sentinel, ok := taggedSentinel(err); ok {
		return fmt.Errorf("daemon: %s: %w", op, keepDetail(sentinel, err))
	}
	// The daemon gave up for a context: callers check for that, not a code.
	if code := connect.CodeOf(err); code == connect.CodeCanceled {
		return fmt.Errorf("daemon: %s: %w", op, errors.Join(context.Canceled, err))
	} else if code == connect.CodeDeadlineExceeded {
		return fmt.Errorf("daemon: %s: %w", op, errors.Join(context.DeadlineExceeded, err))
	}
	var cerr *connect.Error
	if errors.As(err, &cerr) && cerr.Code() == connect.CodeInternal && cerr.Message() != "" {
		// Every backend failure crosses as "internal"; the daemon's message says what went
		// wrong (M_BAD_JSON, M_FORBIDDEN ...).
		return fmt.Errorf("daemon: %s: %w", op, remoteErr{cerr})
	}
	return fmt.Errorf("daemon: %s: %w", op, err)
}

// remoteErr prints a daemon-reported failure as the daemon's message, without
// Connect's code prefix; it still unwraps to the *connect.Error.
type remoteErr struct{ cerr *connect.Error }

func (r remoteErr) Error() string { return r.cerr.Message() }
func (r remoteErr) Unwrap() error { return r.cerr }

// detailed is a sentinel carrying the daemon's own, more specific message.
type detailed struct {
	msg      string
	sentinel error
}

func (d detailed) Error() string { return d.msg }
func (d detailed) Unwrap() error { return d.sentinel }

// keepDetail keeps the daemon's message when it says more than the sentinel, else
// returns the bare sentinel.
func keepDetail(sentinel, err error) error {
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		return sentinel
	}
	msg := cerr.Message()
	if msg == "" || msg == sentinel.Error() {
		return sentinel
	}
	return detailed{msg: msg, sentinel: sentinel}
}

// taggedSentinel recovers the sentinel a Connect error was tagged with; an unknown
// name yields none.
func taggedSentinel(err error) (error, bool) {
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		return nil, false
	}
	sentinel, ok := sentinels[cerr.Meta().Get(sentinelHeader)]
	return sentinel, ok
}
