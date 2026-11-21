package daemon

import (
	"context"
	"crypto"
	"crypto/tls"
	"errors"
	"fmt"
	"slices"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/api/mesh/v1/meshv1connect"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

// Why a mailbox's call is refused.
var (
	// errMailboxOverRemote is AddMailbox over [remote] listen: it is this machine's kithd
	// that pairs with the mailbox.
	errMailboxOverRemote = errors.New("a bridge pairs with a mailbox over its own socket: run `kith mailbox add` on its machine")
	// errRecoveryOverRemote is a mesh's recovery key asked for, or used, over [remote]
	// listen: like kithd's own, it is shown and typed on kithd's machine alone.
	errRecoveryOverRemote = errors.New("a mesh's recovery key is made and used on its bridge's own machine: run `kith mesh` there")
	// errNoMailbox is recovering on a kithd no mailbox is paired with.
	errNoMailbox = errors.New("pair this kithd with the mailbox first: `kith-mailbox pair` there, then `kith mailbox add` here")
)

// postWith runs one session with the mailbox at addrs. A mailbox that refuses this
// bridge's key may admit only the key it rotated from, while the others have not yet
// told it of the rotation: the session is tried again as that key, which tells it.
func (d *Devices) postWith(ctx context.Context, store RosterStore, cert tls.Certificate, signer crypto.Signer, mailbox devicekey.ID, addrs []string) error {
	self, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	err = d.postAs(ctx, store, cert, signer, self, mailbox, addrs)
	if err == nil || ctx.Err() != nil {
		return err
	}
	d.mu.Lock()
	was := d.rotatedLocked(time.Now())
	d.mu.Unlock()
	if was == nil {
		return err
	}
	if rerr := d.postAs(ctx, store, was.cert, signer, was.id, mailbox, addrs); rerr != nil {
		return errors.Join(err, rerr)
	}
	return nil
}

// postAs runs one session with the mailbox at addrs, presenting cert, whose key is as.
func (d *Devices) postAs(ctx context.Context, store RosterStore, cert tls.Certificate, signer crypto.Signer, as, mailbox devicekey.ID,
	addrs []string,
) error {
	hc := dialBridge(cert, mailbox, addrs)
	defer hc.CloseIdleConnections()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := meshv1connect.NewMeshServiceClient(hc, bridgeBaseURL).Sync(ctx)
	err := store.SyncMailbox(ctx, signer, as, mailbox, clientFrames{stream}, d.log)
	cancel() // the stream's other half, still reading, ends with it
	_ = stream.CloseRequest()
	_ = stream.CloseResponse()
	if err != nil {
		return fmt.Errorf("daemon: sync with a mailbox: %w", err)
	}
	return nil
}

// addMailbox pairs this bridge with the mailbox inv names (kith mailbox add) and, once
// the person at the mailbox confirms it, writes the mailbox into the mesh as name,
// dialed at the invite's addresses; say gets the digits this bridge works out, then
// how the pairing ended.
func (d *Devices) addMailbox(ctx context.Context, inv pairing.Invite, name string, say func(*v1.AddMailboxResponse) error) error {
	store, cert, self, signer, err := d.meshParts()
	if err != nil {
		return err
	}
	if inv.ID == self {
		return errOwnPairing
	}
	// Neither its addresses nor its sealing key: the mailbox needs only its key.
	outcome, err := d.pairAsBridge(ctx, cert, self, inv, name, nil, nil, func(digits string) error {
		return say(&v1.AddMailboxResponse{Event: &v1.AddMailboxResponse_Digits{Digits: digits}})
	})
	if err != nil {
		return err
	}
	if outcome == domain.PairingConfirmed {
		mb := domain.Device{ID: inv.ID.String(), Name: name, Role: domain.RoleMailbox, AddedAt: time.Now().UTC().Truncate(time.Second), AddedBy: self.String()}
		if err := store.AddMailbox(ctx, signer, mb, inv.Addresses); err != nil {
			return fmt.Errorf("daemon: paired, but the mailbox was not written into the mesh (run the same `kith mailbox add` again): %w", err)
		}
		d.log.Info("added a mailbox to the mesh", "mailbox", inv.ID.String())
		d.poke()
	}
	return say(&v1.AddMailboxResponse{Event: &v1.AddMailboxResponse_Outcome{Outcome: pc.PairingOutcomeToProto(outcome)}})
}

// AddMailbox pairs this kithd with the mailbox a pairing names and adds it to the
// mesh: over the socket only.
func (s *server) AddMailbox(ctx context.Context, r *req[v1.AddMailboxRequest], st *connect.ServerStream[v1.AddMailboxResponse]) error {
	if s.Paired.peer(ctx) != nil {
		return connect.NewError(connect.CodePermissionDenied, errMailboxOverRemote)
	}
	inv, err := pairing.ParseURI(r.Msg.GetUri())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	err = s.Paired.addMailbox(ctx, inv, r.Msg.GetName(), func(ev *v1.AddMailboxResponse) error { return sendFrame(st, ev) })
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errOwnPairing):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, errDigitsDiffer):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if cerr, ok := errors.AsType[*connect.Error](err); ok {
		return cerr
	}
	return rpcErr(err)
}

// AddMailbox has the kithd of this machine (over its socket) pair with the mailbox
// whose pairing uri names and add it to the mesh as name: digits is called with the
// six it works out; it returns how the pairing ended.
func (r *Remote) AddMailbox(ctx context.Context, uri, name string, digits func(string)) (domain.PairingOutcome, error) {
	st, err := r.c.AddMailbox(ctx, connect.NewRequest(&v1.AddMailboxRequest{Uri: uri, Name: name}))
	if err != nil {
		return 0, callErr("add a mailbox", err)
	}
	defer func() { _ = st.Close() }() // the outcome is read, or the stream failed
	for st.Receive() {
		switch ev := st.Msg().GetEvent().(type) {
		case *v1.AddMailboxResponse_Digits:
			digits(ev.Digits)
		case *v1.AddMailboxResponse_Outcome:
			return pairingOutcome("add a mailbox", ev.Outcome)
		}
	}
	return 0, callErr("add a mailbox", cmpErr(st.Err(), errPairingCut))
}

// recoverMesh starts this bridge's mesh anew from the first mailbox it lists, opened
// with secret, the mesh's recovery key, then settles the roster as a change made here.
func (d *Devices) recoverMesh(ctx context.Context, secret []byte) (domain.MeshRecovered, devicekey.ID, error) {
	store, cert, self, signer, err := d.meshParts()
	if err != nil {
		return domain.MeshRecovered{}, devicekey.ID{}, err
	}
	st, err := store.Status(self)
	if err != nil {
		return domain.MeshRecovered{}, devicekey.ID{}, fmt.Errorf("daemon: %w", err)
	}
	i := slices.IndexFunc(st.Mailboxes, func(mb domain.MeshMailbox) bool { return len(mb.Addresses) > 0 })
	if i < 0 {
		return domain.MeshRecovered{}, devicekey.ID{}, errNoMailbox
	}
	mailbox, err := devicekey.ParseID(st.Mailboxes[i].Mailbox.ID)
	if err != nil {
		return domain.MeshRecovered{}, devicekey.ID{}, fmt.Errorf("daemon: %w", err)
	}
	hc := dialBridge(cert, mailbox, st.Mailboxes[i].Addresses)
	defer hc.CloseIdleConnections()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := meshv1connect.NewMeshServiceClient(hc, bridgeBaseURL).Sync(sctx)
	got, err := store.Recover(sctx, signer, secret, mailbox, clientFrames{stream})
	cancel()
	_ = stream.CloseRequest()
	_ = stream.CloseResponse()
	if err != nil {
		return domain.MeshRecovered{}, mailbox, fmt.Errorf("daemon: recover the mesh: %w", err)
	}
	d.mu.Lock()
	if d.refreshed {
		err = d.settleLocked(&rosterRead{store: store, signer: signer, file: d.rendered, entries: d.known})
	} else {
		_, err = d.refreshLocked()
	}
	d.mu.Unlock()
	d.poke()
	return got, mailbox, err
}

// MakeMeshRecoveryKey makes a recovery key for this bridge's mesh: over the socket only.
func (s *server) MakeMeshRecoveryKey(ctx context.Context, _ *req[v1.MakeMeshRecoveryKeyRequest]) (*resp[v1.MakeMeshRecoveryKeyResponse], error) {
	if s.Paired.peer(ctx) != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errRecoveryOverRemote)
	}
	store, _, _, signer, err := s.Paired.meshParts()
	if err != nil {
		return nil, rpcErr(err)
	}
	key, err := store.NewRecoveryKey(ctx, signer)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&v1.MakeMeshRecoveryKeyResponse{Key: key}), nil
}

// RecoverMesh starts this bridge's mesh anew from its mailbox, with the mesh's recovery
// key: over the socket only.
func (s *server) RecoverMesh(ctx context.Context, r *req[v1.RecoverMeshRequest]) (*resp[v1.RecoverMeshResponse], error) {
	if s.Paired.peer(ctx) != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errRecoveryOverRemote)
	}
	secret, err := devicekey.ParseSecret(r.Msg.GetKey(), devicekey.MeshRecovery)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	got, mailbox, err := s.Paired.recoverMesh(ctx, secret)
	if err != nil {
		if errors.Is(err, errNoMailbox) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, rpcErr(err)
	}
	s.Log.Info("recovered the mesh from a mailbox", "mailbox", mailbox.String(), "keys", got.Keys, "members", len(got.Members))
	return connect.NewResponse(&v1.RecoverMeshResponse{
		Keys: int32(min(got.Keys, 1<<30)), Members: pc.DevicesToProto(got.Members), Mailbox: mailbox.String(), // #nosec G115 -- clamped
	}), nil
}

// MakeMeshRecoveryKey asks the kithd of this machine (over its socket) for a new
// recovery key of its mesh.
func (r *Remote) MakeMeshRecoveryKey(ctx context.Context) (string, error) {
	resp, err := call(ctx, "make the mesh's recovery key", r.c.MakeMeshRecoveryKey, &v1.MakeMeshRecoveryKeyRequest{})
	if err != nil {
		return "", err
	}
	return resp.GetKey(), nil
}

// RecoverMesh has the kithd of this machine (over its socket) start its mesh anew from
// its mailbox with key, the mesh's recovery key as written down: how many keys came
// back, the old members to pair again, and the mailbox.
func (r *Remote) RecoverMesh(ctx context.Context, key string) (domain.MeshRecovered, string, error) {
	resp, err := call(ctx, "recover the mesh", r.c.RecoverMesh, &v1.RecoverMeshRequest{Key: key})
	if err != nil {
		return domain.MeshRecovered{}, "", err
	}
	return domain.MeshRecovered{Keys: int(resp.GetKeys()), Members: pc.ProtoToDevices(resp.GetMembers())}, resp.GetMailbox(), nil
}
