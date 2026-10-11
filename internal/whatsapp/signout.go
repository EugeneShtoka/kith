package whatsapp

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Signing out unlinks this device: WhatsApp forgets it and the store drops its keys.
// The account stays configured; logging in pairs again. It needs a live connection.

// errNoSuchAccountSpace: the space is no WhatsApp account's.
var errNoSuchAccountSpace = fmt.Errorf("%w: no WhatsApp account here is that space", api.ErrNotOnNetwork)

// SignOut unlinks the account owning space; forget also drops its rooms from the cache.
func (a *Adapter) SignOut(ctx context.Context, space domain.SpaceID, forget bool) error {
	var account Account
	for _, acc := range a.accountsNow() {
		if accountSpaceID(acc.Digits) == space {
			account = acc
		}
	}
	if account.Digits == "" {
		return errNoSuchAccountSpace
	}
	_, client, ok := a.clientFor(account.Digits)
	if !ok {
		return fmt.Errorf("whatsapp: %s is not connected, and only a connection can unlink it: %w", account.Name, errNetworkOff)
	}
	if err := client.Logout(ctx); err != nil {
		return fmt.Errorf("whatsapp: unlink %s: %w", account.Name, err)
	}
	a.mu.Lock()
	if a.clients[account.Digits] == client {
		delete(a.clients, account.Digits)
	}
	a.mu.Unlock()
	a.link(account, Unlinked, "signed out; run `kith login whatsapp "+account.Name+"` to link it again")
	if forget && a.cache != nil {
		if err := a.cache.ForgetOwned(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits)); err != nil {
			return fmt.Errorf("whatsapp: forget %s's rooms: %w", account.Name, err)
		}
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
	return nil
}
