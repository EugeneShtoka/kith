package instagram

import (
	"context"
	"fmt"

	"go.mau.fi/mautrix-meta/pkg/instameow"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// BlockPerson adds an Instagram user to the account's block list, as Instagram's own
// "Block" does: they can no longer message you or see your profile.
func (a *Adapter) BlockPerson(ctx context.Context, userID string) error {
	return a.setBlocked(ctx, userID, true)
}

// UnblockPerson is BlockPerson's inverse.
func (a *Adapter) UnblockPerson(ctx context.Context, userID string) error {
	return a.setBlocked(ctx, userID, false)
}

// Blocked is empty: Instagram has no call listing the block list (instameow has none),
// so kith keeps no mirror of it.
func (a *Adapter) Blocked(context.Context) ([]string, error) { return nil, nil }

// setBlocked blocks or unblocks userID through a connected account that has met them.
func (a *Adapter) setBlocked(ctx context.Context, userID string, block bool) error {
	what := "unblock"
	if block {
		what = "block"
	}
	if domain.NetworkOf(userID) != domain.ProtocolInstagram {
		return fmt.Errorf("instagram: %s is not an Instagram person", userID)
	}
	client, igid := a.blocker(userID)
	if client == nil {
		return fmt.Errorf("%w: no Instagram account is connected", api.ErrNetworkOff)
	}
	if igid == "" {
		return fmt.Errorf("instagram: %s %s: they are in no thread of a connected account", what, userID)
	}
	if err := client.SetUserBlocked(ctx, igid, block); err != nil {
		return fmt.Errorf("instagram: %s %s: %w", what, userID, err)
	}
	return nil
}

// blocker is a connected account's client and userID's IG ID as that account's inbox
// knows it. Instagram blocks a user by IG ID, which the inbox learns from the threads
// it lists, while kith names them by messaging ID. The IG ID is empty when no
// connected account has met them; the client is nil when none is connected.
func (a *Adapter) blocker(userID string) (*instameow.Client, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var found *instameow.Client
	for _, c := range a.conns {
		c.mu.Lock()
		client := c.client
		c.mu.Unlock()
		if client == nil {
			continue
		}
		found = client
		c.inbox.mu.Lock()
		igid := ""
		for ig, id := range c.inbox.byIGID {
			if id == userID {
				igid = ig
			}
		}
		c.inbox.mu.Unlock()
		if igid != "" {
			return client, igid
		}
	}
	return found, ""
}
