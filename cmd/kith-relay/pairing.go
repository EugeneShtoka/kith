package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/keystore"
	"github.com/EugeneShtoka/kith/internal/pairing"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Pairing, as the relay helps the window with it. kithd runs a pairing of another device
// itself (BackendService's StartPairing and ConfirmPairing, forwarded); the relay writes
// its ticket as the URI the new device joins with. Joining a bridge from this device is
// the relay's alone, as `kith join` is: it holds this machine's device key and device.toml.

// errNoAddress answers a ticket that names nowhere to dial.
var errNoAddress = errors.New("a pairing's ticket names no address to dial")

// PairingURI is the ticket as `kith pair` writes it for `kith join` (pairing.Invite).
func (device) PairingURI(_ context.Context, req *connect.Request[devicev1.PairingURIRequest]) (*connect.Response[devicev1.PairingURIResponse], error) {
	id, err := devicekey.ParseID(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the pairing's ID: %w", err))
	}
	token, err := pairing.ParseToken(req.Msg.GetToken())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the pairing's token: %w", err))
	}
	addrs := req.Msg.GetAddresses()
	if len(addrs) == 0 || req.Msg.GetExpires() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errNoAddress)
	}
	inv := pairing.Invite{Addresses: addrs, ID: id, Token: token, Expires: req.Msg.GetExpires().AsTime()}
	return connect.NewResponse(&devicev1.PairingURIResponse{Uri: inv.URI()}), nil
}

// Invite is the bridge a kith://pair URI names, read as `kith join` reads it.
func (device) Invite(_ context.Context, req *connect.Request[devicev1.InviteRequest]) (*connect.Response[devicev1.InviteResponse], error) {
	inv, err := pairing.ParseURI(req.Msg.GetUri())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&devicev1.InviteResponse{
		Addresses: inv.Addresses, Id: inv.ID.String(), Expires: timestamppb.New(inv.Expires),
	}), nil
}

// JoinBridge is `kith join` here: the same key (setup.ClientStorage's "client-device"),
// the same answer to the pairing (daemon.Remote.Join, which checks the digits) and the
// same entry written (setup.RememberBridge). The write follows a confirmation even when
// the window has gone meanwhile: the bridge has paired this device by then.
func (d device) JoinBridge(ctx context.Context, req *connect.Request[devicev1.JoinBridgeRequest], st *connect.ServerStream[devicev1.JoinBridgeResponse]) error {
	inv, err := pairing.ParseURI(req.Msg.GetUri())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	name := req.Msg.GetName()
	if name == "" {
		name, _ = os.Hostname() // none leaves the name to the bridge's side, as kith join does
	}
	storage, allowFile, err := setup.ClientStorage(d.configPath)
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	cert, err := keystore.ClientCertificate(storage, allowFile)
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	self, err := certID(cert)
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	b := daemon.NewBridgeRemote(cert, inv.ID, inv.Addresses)
	defer b.Stop()
	if jerr := b.Join(ctx, inv, self, name, func(digits string) {
		_ = st.Send(&devicev1.JoinBridgeResponse{Event: &devicev1.JoinBridgeResponse_Digits{Digits: digits}}) // a window gone ends the pairing through ctx
	}); jerr != nil {
		return joinErr(ctx, jerr)
	}
	entry, err := setup.RememberBridge(config.DevicePath(d.configPath), inv)
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("paired, but %w", err))
	}
	if err := st.Send(&devicev1.JoinBridgeResponse{Event: &devicev1.JoinBridgeResponse_Joined{Joined: &devicev1.JoinedBridge{
		Name: entry.Name, Id: entry.ID, Addresses: entry.Addresses,
	}}}); err != nil {
		return fmt.Errorf("telling the window the bridge joined: %w", err)
	}
	return nil
}

// joinErr is why a join ended unconfirmed, as the window reads it: the window going
// away (ctx) is canceled; the bridge's own refusal keeps its code; a connection cut
// otherwise, or refused in its handshake (a bridge with no pairing open admits no
// unpaired key), is unavailable; and the pairing ending unconfirmed (refused, expired,
// abandoned, other digits) is a failed precondition.
func joinErr(ctx context.Context, err error) *connect.Error {
	code := connect.CodeOf(err)
	switch {
	case ctx.Err() != nil:
		code = connect.CodeCanceled
	case code == connect.CodeCanceled:
		code = connect.CodeUnavailable
	case code == connect.CodeUnknown:
		code = connect.CodeFailedPrecondition
	}
	return connect.NewError(code, err)
}

// ThisDevice is the ID of the key this relay dials its bridge with.
func (d device) ThisDevice(context.Context, *connect.Request[devicev1.ThisDeviceRequest]) (*connect.Response[devicev1.ThisDeviceResponse], error) {
	return connect.NewResponse(&devicev1.ThisDeviceResponse{Id: d.at.id}), nil
}

// certID is the device ID of the key cert carries (devicekey.Certificate's, whose leaf
// it keeps).
func certID(cert tls.Certificate) (devicekey.ID, error) {
	if cert.Leaf == nil {
		return devicekey.ID{}, errors.New("this machine's device key: no certificate")
	}
	id, err := devicekey.IDOf(cert.Leaf.PublicKey)
	if err != nil {
		return devicekey.ID{}, fmt.Errorf("this machine's device key: %w", err)
	}
	return id, nil
}
