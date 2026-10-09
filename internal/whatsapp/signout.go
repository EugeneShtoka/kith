package whatsapp

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Signing an account out unlinks kith from the phone, as removing it from the phone's
// linked devices does: WhatsApp forgets this device and kith's store forgets its keys.
// The account stays in the config, and logging in pairs it again. Unlinking is said to
// WhatsApp through the connection, so an account not connected is not signed out.

// errNoSuchAccountSpace is a space that is no WhatsApp account's own.
var errNoSuchAccountSpace = fmt.Errorf("%w: no WhatsApp account here is that space", api.ErrNotOnNetwork)

// SignOut unlinks the account whose own space space is; with forget, its rooms and
// their history go from the cache too, else they stay, readable.
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
