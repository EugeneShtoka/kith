//go:build signal

package meow

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	accountpb "go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/rpc/account"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/rpc/common"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
)

// usernameLinkKey is the length of a username link's key, before its 16-byte handle.
const usernameLinkKey = 32

// FindPerson asks Signal for the account behind a number (contact discovery) or a
// username link: the handle fetches the encrypted username, the link's key opens it,
// and the username's hash names the account.
func (n *Network) FindPerson(ctx context.Context, req *signalv1.FindPersonRequest) (*signalv1.FindPersonResponse, error) {
	a, err := n.connected(req.GetAccount())
	if err != nil {
		return nil, err
	}
	if number := req.GetNumber(); number != "" {
		found, _, lookErr := a.lookUp(ctx, []string{number})
		if lookErr != nil || len(found) == 0 {
			return &signalv1.FindPersonResponse{}, lookErr
		}
		return &signalv1.FindPersonResponse{Aci: found[0].String()}, nil
	}
	link := req.GetUsernameLink()
	if len(link) != usernameLinkKey+16 {
		return nil, errors.New("signal: a username link is 48 bytes")
	}
	if a.cli.GRPC == nil {
		return nil, errors.New("signal: not connected to Signal's servers")
	}
	ciphertext, err := a.cli.GRPC.AccountsAnonymous.LookupUsernameLink(ctx,
		&accountpb.LookupUsernameLinkRequest{UsernameLinkHandle: link[usernameLinkKey:]})
	if err != nil {
		return nil, fmt.Errorf("signal: look the username link up: %w", err)
	}
	if ciphertext.GetUsernameCiphertext() == nil {
		return &signalv1.FindPersonResponse{}, nil // a link since reset, or never made
	}
	username, err := decryptUsername(link[:usernameLinkKey], ciphertext.GetUsernameCiphertext())
	if err != nil {
		return nil, err
	}
	hash, err := usernameHash(username)
	if err != nil {
		return nil, err
	}
	found, err := a.cli.GRPC.AccountsAnonymous.LookupUsernameHash(ctx, &accountpb.LookupUsernameHashRequest{UsernameHash: hash})
	if err != nil {
		return nil, fmt.Errorf("signal: look %s up: %w", username, err)
	}
	id := found.GetServiceIdentifier()
	if id == nil || id.GetIdentityType() != common.IdentityType_IDENTITY_TYPE_ACI {
		return &signalv1.FindPersonResponse{Username: username}, nil
	}
	aci, err := uuid.FromBytes(id.GetUuid())
	if err != nil {
		return nil, fmt.Errorf("signal: %s's account: %w", username, err)
	}
	return &signalv1.FindPersonResponse{Aci: aci.String(), Username: username}, nil
}
