package domain

import (
	"context"
	"errors"
)

// AccountPhase is where one network account is: what a client shows beside it.
type AccountPhase int

const (
	// AccountLoggedOut has no session (never logged in, rejected, or unlinked).
	AccountLoggedOut AccountPhase = iota + 1
	// AccountConnecting has a session and is reaching its network.
	AccountConnecting
	// AccountOnline has synced: its rooms in the cache are current.
	AccountOnline
	// AccountFailed stopped with an error.
	AccountFailed
)

// AccountStatus is one network account's state, as its adapter reports it.
type AccountStatus struct {
	Network Protocol
	// Account is what the config calls it: an MXID, or an account's name.
	Account string
	Phase   AccountPhase
	// Detail is why it is logged out or failed, or what it waits for.
	Detail string
}

// SyncFault returns nil when err (from api.Backend.Start) is just our own
// shutdown, so a clean stop does not exit non-zero or clear readiness.
func SyncFault(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return nil //nolint:nilerr // discarding the error is the point: our own shutdown is not a fault
	}
	return err
}
