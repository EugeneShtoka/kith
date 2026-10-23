package telegram

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// blocklistPageSize bounds one ContactsGetBlocked call. 100 is Telegram's own
// recommended page; the server returns fewer when the list is shorter.
const blocklistPageSize = 100

// BlockPerson adds the user named by kith person ID to this account's Telegram
// block list. Telegram's server stops delivering the user's DMs (the user
// trying to write gets a refusal); shared-group messages still arrive and are
// filtered by kith's own person-mark machinery for a complete silence.
func (a *Adapter) BlockPerson(ctx context.Context, userID string) error {
	userIDnum, c, err := a.resolveBlockTarget(userID)
	if err != nil {
		return err
	}
	peer, err := a.inputPeer(ctx, c, userIDnum)
	if err != nil {
		return fmt.Errorf("telegram: block %s: %w", userID, err)
	}
	if _, err := c.client.API().ContactsBlock(ctx, &tg.ContactsBlockRequest{ID: peer}); err != nil {
		return fmt.Errorf("telegram: block %s: %w", userID, err)
	}
	a.RefreshBlocklist(ctx)
	return nil
}

// UnblockPerson is BlockPerson's inverse. A user not currently blocked is a
// no-op on the server; the call still round-trips.
func (a *Adapter) UnblockPerson(ctx context.Context, userID string) error {
	userIDnum, c, err := a.resolveBlockTarget(userID)
	if err != nil {
		return err
	}
	peer, err := a.inputPeer(ctx, c, userIDnum)
	if err != nil {
		return fmt.Errorf("telegram: unblock %s: %w", userID, err)
	}
	if _, err := c.client.API().ContactsUnblock(ctx, &tg.ContactsUnblockRequest{ID: peer}); err != nil {
		return fmt.Errorf("telegram: unblock %s: %w", userID, err)
	}
	a.RefreshBlocklist(ctx)
	return nil
}

// Blocked lists the Telegram user IDs currently blocked across every connected
// account, from the cache mirror. A fresh install with no connected accounts
// reads empty.
func (a *Adapter) Blocked(ctx context.Context) ([]string, error) {
	if a.cache == nil {
		return nil, nil
	}
	ids, err := a.cache.NetworkBlocked(ctx, domain.ProtocolTelegram)
	if err != nil {
		return nil, fmt.Errorf("telegram: read blocked users: %w", err)
	}
	return ids, nil
}

// RefreshBlocklist pulls the current blocked list for every connected account and
// mirrors the union. Server copy wins; the union matches how WhatsApp's mirror
// collates accounts.
func (a *Adapter) RefreshBlocklist(ctx context.Context) {
	if a.cache == nil {
		return
	}
	all := map[string]bool{}
	for _, c := range a.connected() {
		users, err := a.allBlockedFrom(ctx, c)
		if err != nil {
			a.log.Warn("read blocklist failed", "account", c.user, "err", err)
			continue
		}
		for _, user := range users {
			all[personID(user)] = true
		}
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	if err := a.cache.SetNetworkBlocked(ctx, domain.ProtocolTelegram, ids); err != nil {
		a.log.Warn("mirror blocklist failed", "err", err)
	}
}

// allBlockedFrom pages ContactsGetBlocked until the server stops returning full
// pages. Telegram's slice form may truncate at the server's cap; the full form
// always returns everything, so one call is usually enough.
func (a *Adapter) allBlockedFrom(ctx context.Context, c conn) ([]int64, error) {
	var out []int64
	offset := 0
	for {
		res, err := c.client.API().ContactsGetBlocked(ctx, &tg.ContactsGetBlockedRequest{
			Offset: offset, Limit: blocklistPageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("telegram: list blocked users: %w", err)
		}
		users := blockedUserIDs(res)
		out = append(out, users...)
		if len(users) < blocklistPageSize {
			return out, nil
		}
		offset += len(users)
	}
}

// blockedUserIDs is the user IDs of the blocked peers in a ContactsBlocked box;
// chats are skipped (Telegram's blocklist is user-only despite the type).
func blockedUserIDs(res tg.ContactsBlockedClass) []int64 {
	var peers []tg.PeerBlocked
	switch v := res.(type) {
	case *tg.ContactsBlocked:
		peers = v.Blocked
	case *tg.ContactsBlockedSlice:
		peers = v.Blocked
	default:
		return nil
	}
	out := make([]int64, 0, len(peers))
	for _, p := range peers {
		if u, ok := p.PeerID.(*tg.PeerUser); ok {
			out = append(out, u.UserID)
		}
	}
	return out
}

// resolveBlockTarget turns a kith person ID into (user ID, connection to block
// through). Any connected account can block any user: Telegram's block is per
// user and travels with the account doing the asking.
func (a *Adapter) resolveBlockTarget(userID string) (int64, conn, error) {
	parsed := domain.ParseID(userID)
	if parsed.Network != domain.ProtocolTelegram {
		return 0, conn{}, fmt.Errorf("telegram: %s is not a Telegram person", userID)
	}
	id, err := strconv.ParseInt(parsed.Native, 10, 64)
	if err != nil {
		return 0, conn{}, fmt.Errorf("telegram: %s is not a user number: %w", userID, err)
	}
	cs := a.connected()
	if len(cs) == 0 {
		return 0, conn{}, fmt.Errorf("%w: no Telegram account is connected", api.ErrNetworkOff)
	}
	return id, cs[0], nil
}
