package telegram

import (
	"context"
	"fmt"

	"github.com/gotd/td/telegram/auth"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Signing an account out ends its session on Telegram (it leaves the account's active
// sessions), stops its connection and deletes its kept credentials; the account stays
// in the config, and logging in signs it back in. The session is ended through the
// connection, so an account not connected is not signed out: its session would live on
// at Telegram.

// errNoSuchAccountSpace is a space that is no Telegram account's own.
var errNoSuchAccountSpace = fmt.Errorf("%w: no Telegram account here is that space", api.ErrNotOnNetwork)

// SignOut signs out the account whose own space space is; with forget, its rooms and
// their history go from the cache too, else they stay, readable.
func (a *Adapter) SignOut(ctx context.Context, space domain.SpaceID, forget bool) error {
	account, self, ok := a.accountOfSpace(space)
	if !ok {
		return errNoSuchAccountSpace
	}
	a.mu.Lock()
	var c conn
	if live := a.conns[account.Name]; live != nil {
		c = *live
	}
	a.mu.Unlock()
	if c.client == nil {
		return fmt.Errorf("telegram: %s is not connected, and only a connection can end its session: %w", account.Name, api.ErrNetworkOff)
	}
	if _, err := c.client.API().AuthLogOut(ctx); err != nil && !auth.IsUnauthorized(err) {
		return fmt.Errorf("telegram: sign %s out: %w", account.Name, err)
	}
	a.letGo(account)
	a.keeping.Lock()
	err := a.secrets.DeleteSecret(credentialsRef(account.Digits))
	a.keeping.Unlock()
	if err != nil {
		return fmt.Errorf("telegram: delete %s's credentials: %w", account.Name, err)
	}
	a.session(account, LoggedOut, "signed out; "+loginHint(account))
	if forget && a.cache != nil {
		if err := a.cache.ForgetOwned(ctx, domain.AccountRooms(domain.ProtocolTelegram, fmt.Sprint(self))); err != nil {
			return fmt.Errorf("telegram: forget %s's rooms: %w", account.Name, err)
		}
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
	return nil
}

// letGo ends an account's connection and any login of it, and makes every login and
// connection begun before now stale, so none of them keeps credentials or connects
// again (current, connectAs).
func (a *Adapter) letGo(account Account) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.signIns[account.Name]++
	if c := a.conns[account.Name]; c != nil {
		c.cancel()
		delete(a.conns, account.Name)
	}
	if l := a.logins[account.Name]; l != nil {
		l.cancel()
		delete(a.logins, account.Name)
	}
}

// accountOfSpace is the configured account whose own space space is, and its user.
func (a *Adapter) accountOfSpace(space domain.SpaceID) (Account, int64, bool) {
	for _, account := range a.accountsNow() {
		if self, ok := a.selfOf(account); ok && accountSpaceID(self) == space {
			return account, self, true
		}
	}
	return Account{}, 0, false
}
