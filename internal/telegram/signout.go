package telegram

import (
	"context"
	"fmt"

	"github.com/gotd/td/telegram/auth"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Sign-out ends the Telegram session, stops the connection and deletes credentials;
// the account stays configured. It needs a live connection, or the session would
// survive at Telegram.

// errNoSuchAccountSpace is a space that isn't a Telegram account's.
var errNoSuchAccountSpace = fmt.Errorf("%w: no Telegram account here is that space", api.ErrNotOnNetwork)

// SignOut signs out space's account; forget also drops its rooms from the cache.
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

// letGo cancels an account's connection and login and bumps its generation so no
// earlier work stores credentials or reconnects.
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

// accountOfSpace is space's configured account and user ID.
func (a *Adapter) accountOfSpace(space domain.SpaceID) (Account, int64, bool) {
	for _, account := range a.accountsNow() {
		if self, ok := a.selfOf(account); ok && accountSpaceID(self) == space {
			return account, self, true
		}
	}
	return Account{}, 0, false
}
