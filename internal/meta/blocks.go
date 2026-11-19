package meta

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"
	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/domain"
)

var errBlockRefused = errors.New("meta: Meta refused the block list change")

// BlockPerson adds a Messenger user to the account's block list, as Messenger's own
// "Block" does: Meta stops their messages and calls reaching you.
func (a *Adapter) BlockPerson(ctx context.Context, userID string) error {
	return a.setBlocked(ctx, userID, true)
}

// UnblockPerson is BlockPerson's inverse.
func (a *Adapter) UnblockPerson(ctx context.Context, userID string) error {
	return a.setBlocked(ctx, userID, false)
}

// Blocked is empty: Messenger has no call listing the block list (messagix has none),
// so kith keeps no mirror of it.
func (a *Adapter) Blocked(context.Context) ([]string, error) { return nil, nil }

// setBlocked blocks or unblocks userID through a connected account, as messagix's
// bridge does: a block-status task, whose answer says whether Meta took it.
func (a *Adapter) setBlocked(ctx context.Context, userID string, block bool) error {
	id := domain.ParseID(userID)
	who, parseErr := strconv.ParseInt(id.Native, 10, 64)
	if id.Network != domain.ProtocolMessenger || parseErr != nil {
		return fmt.Errorf("meta: %s is not a Messenger person", userID)
	}
	client := a.anyClient()
	if client == nil {
		return errNotConnected
	}
	action, what := 0, "unblock"
	if block {
		action, what = 1, "block"
	}
	err := a.guard("changing the block list", func() error {
		if err := client.WaitUntilCanSendMessages(ctx, sendWait); err != nil {
			return fmt.Errorf("%w: %w", errNotConnected, err)
		}
		resp, err := client.ExecuteTasks(ctx, &socket.SetUserBlockStatusTask{
			BlockeeID: who, RequestID: strconv.FormatInt(methods.GenerateEpochID(), 10), UserBlockAction: action,
		})
		if err != nil {
			return fmt.Errorf("send the block status: %w", err)
		}
		return blockRefused(resp, who)
	})
	if err != nil {
		return fmt.Errorf("meta: %s %s: %w", what, userID, err)
	}
	return nil
}

// blockRefused is errBlockRefused when Meta's answer says the change for who failed.
func blockRefused(resp *table.LSTable, who int64) error {
	if resp == nil {
		return nil
	}
	for _, r := range resp.LSResetUserBlockStatus {
		if r.ContactID == who && !r.Success {
			return errBlockRefused
		}
	}
	return nil
}

// anyClient is a connected account's client, or nil. Any account can block anyone: a
// Messenger user's ID is the same from every account.
func (a *Adapter) anyClient() *messagix.Client {
	a.mu.Lock()
	conns := slices.Collect(maps.Values(a.conns))
	a.mu.Unlock()
	for _, c := range conns {
		c.mu.Lock()
		client := c.client
		c.mu.Unlock()
		if client != nil {
			return client
		}
	}
	return nil
}
