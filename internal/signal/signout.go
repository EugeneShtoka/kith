package signal

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

var errNoSuchAccountSpace = errors.New("signal: no linked account has that space")

// SignOut unlinks the account whose space is space: Signal is told and the device's keys
// deleted (by kith-signal, which holds them, so it must be running), then kith forgets
// which Signal account the name was, and with forget its rooms. What kith had cached
// otherwise stays readable.
func (a *Adapter) SignOut(ctx context.Context, space domain.SpaceID, forget bool) error {
	name, aci := "", ""
	for account, linked := range a.linkedAccounts() {
		if accountSpaceID(linked) == space {
			name, aci = account, linked
		}
	}
	if aci == "" {
		return fmt.Errorf("%w: %s", errNoSuchAccountSpace, space)
	}
	a.mu.Lock()
	h := a.helper
	a.mu.Unlock()
	if h == nil {
		return fmt.Errorf("%w: kith-signal is not running, and it holds the device's keys", api.ErrNetworkOff)
	}
	if _, err := h.client.Unlink(ctx, connect.NewRequest(&signalv1.UnlinkRequest{Account: aci})); err != nil {
		return fmt.Errorf("signal: unlink %s: %w", name, err)
	}
	h.remove(aci)
	if err := a.secrets.DeleteSecret(linkRef(name)); err != nil {
		return fmt.Errorf("signal: forget which Signal account %s was: %w", name, err)
	}
	a.log.Info("signed out", "account", name, "aci", aci)
	if forget && a.cache != nil {
		if err := a.cache.ForgetOwned(ctx, owner(aci)); err != nil {
			return fmt.Errorf("signal: forget %s's rooms: %w", name, err)
		}
	}
	a.announceAll()
	a.roomsChanged()
	return nil
}
