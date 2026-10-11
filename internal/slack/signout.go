package slack

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Signing out ends the session on Slack (as Slack's "Sign out" does), stops the
// connection and deletes stored credentials; the account stays configured. Slack is
// told through the session, so a disconnected account is not signed out there.

// errNoSuchAccountSpace is a space that is no Slack account's workspace.
var errNoSuchAccountSpace = fmt.Errorf("%w: no Slack account here is that workspace", api.ErrNotOnNetwork)

// SignOut signs out the account whose workspace space is; with forget, its rooms and
// history leave the cache too.
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

// signOff ends w's connection and makes connections begun on older sign-ins stale, so
// adopt refuses them.
func (a *Adapter) signOff(w *workspace) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.signIns[w.account.Name]++
	w.close()
	if a.workspaces[w.account.Name] == w {
		delete(a.workspaces, w.account.Name)
	}
}
