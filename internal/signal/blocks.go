package signal

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Signal's server has no block list: each device keeps one, and the phone shares its
// own through Signal's storage service. A block from kith tells the phone (which keeps
// it from then on) and the helper's store, so the helper drops the person's direct
// messages and calls before kithd sees them, as the phone does.

// BlockPerson blocks a Signal person from a connected account.
func (a *Adapter) BlockPerson(ctx context.Context, userID string) error {
	return a.setBlocked(ctx, userID, true)
}

// UnblockPerson is BlockPerson's inverse.
func (a *Adapter) UnblockPerson(ctx context.Context, userID string) error {
	return a.setBlocked(ctx, userID, false)
}

// Blocked is the people blocked, from the mirror RefreshBlocklist keeps.
func (a *Adapter) Blocked(ctx context.Context) ([]string, error) {
	if a.cache == nil {
		return nil, nil
	}
	ids, err := a.cache.NetworkBlocked(ctx, domain.ProtocolSignal)
	if err != nil {
		return nil, fmt.Errorf("signal: read blocked people: %w", err)
	}
	return ids, nil
}

func (a *Adapter) setBlocked(ctx context.Context, userID string, block bool) error {
	what := "unblock"
	if block {
		what = "block"
	}
	id := domain.ParseID(userID)
	if id.Network != domain.ProtocolSignal || !isACI(id.Native) {
		return fmt.Errorf("signal: %s is not a Signal person", userID)
	}
	selves := a.connectedSelves()
	if len(selves) == 0 {
		return fmt.Errorf("%w: no Signal account is connected", api.ErrNetworkOff)
	}
	h, err := a.sender(selves[0])
	if err != nil {
		return err
	}
	req := &signalv1.BlockRequest{Account: selves[0], Person: id.Native, Blocked: block}
	if _, err := h.client.Block(ctx, connect.NewRequest(req)); err != nil {
		return fmt.Errorf("signal: %s %s: %w", what, userID, err)
	}
	a.RefreshBlocklist(ctx)
	return nil
}

// RefreshBlocklist mirrors the union of the connected accounts' block lists, as
// WhatsApp's and Telegram's do.
func (a *Adapter) RefreshBlocklist(ctx context.Context) {
	if a.cache == nil {
		return
	}
	all := map[string]bool{}
	for _, self := range a.connectedSelves() {
		h, err := a.sender(self)
		if err != nil {
			continue
		}
		res, err := h.client.Blocked(ctx, connect.NewRequest(&signalv1.BlockedRequest{Account: self}))
		if err != nil {
			a.log.Warn("read the block list failed", "account", self, "err", err)
			continue
		}
		for _, aci := range res.Msg.GetPeople() {
			all[person(aci)] = true
		}
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	if err := a.cache.SetNetworkBlocked(ctx, domain.ProtocolSignal, ids); err != nil {
		a.log.Warn("mirror the block list failed", "err", err)
	}
}

// connectedSelves is the ACIs of the configured accounts connected now.
func (a *Adapter) connectedSelves() []string {
	var out []string
	for _, account := range a.accountsNow() {
		aci, ok, err := a.linkedTo(account.Name)
		if err != nil || !ok {
			continue
		}
		if _, err := a.sender(aci); err == nil {
			out = append(out, aci)
		}
	}
	return out
}
