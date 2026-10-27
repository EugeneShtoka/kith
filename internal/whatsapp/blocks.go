package whatsapp

import (
	"context"
	"fmt"
	"maps"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// BlockPerson adds userID (a WhatsApp person ID, kith-shaped) to this account's
// server-side block list. WhatsApp drops the blocked user's DMs and presence
// server-side; their line in shared groups is suppressed by whatsmeow's own
// filter on the receive side.
func (a *Adapter) BlockPerson(ctx context.Context, userID string) error {
	return a.updateBlock(ctx, userID, events.BlocklistChangeActionBlock)
}

// UnblockPerson is BlockPerson's inverse. A user not on the list is a no-op on
// the server; whatsmeow still round-trips.
func (a *Adapter) UnblockPerson(ctx context.Context, userID string) error {
	return a.updateBlock(ctx, userID, events.BlocklistChangeActionUnblock)
}

// Blocked lists the people this account currently has blocked, from the mirror
// (the server copy is pulled on connect and after every BlocklistChange event).
// A fresh install with nothing connected reads empty.
func (a *Adapter) Blocked(ctx context.Context) ([]string, error) {
	if a.cache == nil {
		return nil, nil
	}
	ids, err := a.cache.NetworkBlocked(ctx, domain.ProtocolWhatsApp)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read blocked users: %w", err)
	}
	return ids, nil
}

// updateBlock applies the action with whichever linked account knows the JID,
// then mirrors the whole server list of every connected account. A user ID with
// no connected WhatsApp account refuses with ErrNetworkOff: there is no way to
// address the server at all.
func (a *Adapter) updateBlock(ctx context.Context, userID string, action events.BlocklistChangeAction) error {
	jid, err := types.ParseJID(domain.ParseID(userID).Native)
	if err != nil {
		return fmt.Errorf("whatsapp: %s is not a WhatsApp person: %w", userID, err)
	}
	client, err := a.anyConnectedClient()
	if err != nil {
		return err
	}
	if _, err := client.UpdateBlocklist(ctx, jid, action, ""); err != nil {
		return fmt.Errorf("whatsapp: %s %s: %w", action, userID, err)
	}
	a.RefreshBlocklist(ctx)
	return nil
}

// RefreshBlocklist pulls the current server list for every connected account and
// mirrors the union: the person-level mirror does not distinguish which of the
// account's lists the block came from, which matches how WhatsApp person IDs are
// account-independent elsewhere in the adapter (see rooms.go personID).
func (a *Adapter) RefreshBlocklist(ctx context.Context) {
	if a.cache == nil {
		return
	}
	clients := a.connectedClients()
	all := map[string]bool{}
	for digits, client := range clients {
		list, err := client.GetBlocklist(ctx, "") // no dhash: always the whole list
		if err != nil {
			a.log.Warn("read blocklist failed", "account", digits, "err", err)
			continue
		}
		if list == nil {
			continue
		}
		for _, jid := range list.LIDs() {
			all[domain.NativePerson(domain.ProtocolWhatsApp, jid.ToNonAD().String())] = true
		}
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	if err := a.cache.SetNetworkBlocked(ctx, domain.ProtocolWhatsApp, ids); err != nil {
		a.log.Warn("mirror blocklist failed", "err", err)
	}
}

// anyConnectedClient is whichever linked WhatsApp account is reachable now.
// Picking the first one is fine: block state is per-account on your end anyway,
// and the mirror collates them (see RefreshBlocklist's comment).
func (a *Adapter) anyConnectedClient() (*whatsmeow.Client, error) {
	for _, client := range a.connectedClients() {
		return client, nil
	}
	return nil, fmt.Errorf("%w: no WhatsApp account is connected", api.ErrNetworkOff)
}

// connectedClients copies the linked clients map while holding the adapter mutex,
// so callers can iterate without the lock.
func (a *Adapter) connectedClients() map[string]*whatsmeow.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]*whatsmeow.Client, len(a.clients))
	maps.Copy(out, a.clients)
	return out
}
