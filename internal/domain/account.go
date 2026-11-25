package domain

import (
	"context"
	"errors"
	"time"
)

// AccountPhase is a network account's connection state.
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

// SyncFault returns nil when err (from api.Backend.Start) is only our own shutdown.
func SyncFault(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return nil //nolint:nilerr // discarding the error is the point: our own shutdown is not a fault
	}
	return err
}

// Heard is what an account's network said about its connection, at At: the evidence
// a bridge's heartbeat is made of (design-clients.md, Heartbeat per network).
type Heard struct {
	Network Protocol
	Account string
	Kind    HeardKind
	At      time.Time
}

// HeardKind is what a Heard says.
type HeardKind int

const (
	// HeardAlive is one answer from the network, alive as of At: a /sync returned, a
	// pong, a heartbeat event.
	HeardAlive HeardKind = iota + 1
	// HeardAnswering is a connection whose keep-alive answers from At on, for networks
	// whose library says only when it stops (HeardSilent) and reconnects on its own:
	// alive until it is silent or the account goes offline.
	HeardAnswering
	// HeardSilent is the keep-alive no longer answering; At is its last answer.
	HeardSilent
	// HeardCaughtUp is what was queued for the account drained since it last connected.
	HeardCaughtUp
	// HeardNotOurs is the account stuck for a reason every bridge shares (Google
	// Messages' phone not answering), so another bridge would fare no better.
	HeardNotOurs
	// HeardOurs is that reason gone.
	HeardOurs
)

// AliveWindow is how long an account of network stays alive after its last evidence
// (design-clients.md, the table's window column). LinkedIn's, Messenger's and
// Instagram's are to be measured (State's hourly "alive gaps" lines).
func AliveWindow(network Protocol) time.Duration {
	switch network {
	case ProtocolTelegram, ProtocolLinkedIn, ProtocolGMessages:
		return 3 * time.Minute
	default:
		return 90 * time.Second
	}
}
