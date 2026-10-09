package slack

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Signing an account out signs its session out of Slack, as Slack's own "Sign out"
// does, stops its connection and deletes its kept credentials; the account stays in
// the config, and logging in signs it back in. Slack is told through the session, so
// an account not connected is not signed out.

// errNoSuchAccountSpace is a space that is no Slack account's workspace.
var errNoSuchAccountSpace = fmt.Errorf("%w: no Slack account here is that workspace", api.ErrNotOnNetwork)

// SignOut signs out the account whose workspace space is; with forget, its rooms and
// their history go from the cache too, else they stay, readable.
func (a *Adapter) SignOut(ctx context.Context, space domain.SpaceID, forget bool) error {
	var w *workspace
	for _, c := range a.connected() {
		if workspaceSpaceID(c.creds.Team) == space {
			w = c
		}
	}
	if w == nil {
		return fmt.Errorf("slack: %w, or it is not connected, and only its session can sign it out", errNoSuchAccountSpace)
	}
	if _, err := w.client.SendAuthSignoutContext(ctx); err != nil && !isSignedOut(err) {
		return fmt.Errorf("slack: sign %s out: %w", w.account.Name, err)
	}
	a.signOff(w)
	if err := a.secrets.DeleteSecret(credentialsRef(w.account.Name)); err != nil {
		return fmt.Errorf("slack: delete %s's credentials: %w", w.account.Name, err)
	}
	a.session(w.account, SignedOut, "signed out; run `kith login slack "+w.account.Name+"` to sign in again")
	if forget && a.cache != nil {
		if err := a.cache.ForgetOwned(ctx, domain.AccountRooms(domain.ProtocolSlack, w.creds.Team)); err != nil {
			return fmt.Errorf("slack: forget %s's rooms: %w", w.account.Name, err)
		}
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
	return nil
}

// signOff ends w's connection, and makes every connection begun on an older sign-in
// stale, so none of them is adopted (adopt).
func (a *Adapter) signOff(w *workspace) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.signIns[w.account.Name]++
	w.close()
	if a.workspaces[w.account.Name] == w {
		delete(a.workspaces, w.account.Name)
	}
}
