//go:build signal

package meow

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-signal/pkg/signalmeow"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
)

// Block blocks or unblocks a person as mautrix-signal's bridge does: a message-request
// answer (block, or accept to unblock) tells the account's other devices, the phone
// among them, and the person's recipient row is marked, which makes signalmeow drop
// their direct messages, calls and typing from then on (group messages still come).
func (n *Network) Block(ctx context.Context, req *signalv1.BlockRequest) error {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return err
	}
	who, err := uuid.Parse(req.GetPerson())
	if err != nil {
		return fmt.Errorf("signal: %q is no ACI: %w", req.GetPerson(), err)
	}
	answer := signalpb.SyncMessage_MessageRequestResponse_ACCEPT
	if req.GetBlocked() {
		answer = signalpb.SyncMessage_MessageRequestResponse_BLOCK
	}
	res := a.cli.SendMessage(ctx, a.cli.Store.ACIServiceID(), signalmeow.WrapSyncMessage(&signalpb.SyncMessage{
		Content: &signalpb.SyncMessage_MessageRequestResponse_{MessageRequestResponse: &signalpb.SyncMessage_MessageRequestResponse{
			Type: answer.Enum(), ThreadAciBinary: who[:],
		}},
	}))
	if !res.WasSuccessful {
		return fmt.Errorf("signal: tell the account's devices: %w", res.Error)
	}
	_, err = a.cli.Store.RecipientStore.LoadAndUpdateRecipient(ctx, who, uuid.Nil, func(r *types.Recipient) (bool, error) {
		changed := r.Blocked != req.GetBlocked()
		r.Blocked = req.GetBlocked()
		return changed, nil
	})
	if err != nil {
		return fmt.Errorf("signal: keep the block: %w", err)
	}
	return nil
}

// Blocked is the ACIs the connected account aci has blocked. signalmeow lists only
// recipients with a name or a number: one blocked with neither is left out.
func (n *Network) Blocked(ctx context.Context, aci string) ([]string, error) {
	a, err := n.connected(aci)
	if err != nil {
		return nil, err
	}
	all, err := a.cli.Store.RecipientStore.LoadAllContacts(ctx)
	if err != nil {
		return nil, fmt.Errorf("signal: list the recipients: %w", err)
	}
	var out []string
	for _, r := range all {
		if r.Blocked && r.ACI != uuid.Nil {
			out = append(out, r.ACI.String())
		}
	}
	return out, nil
}
