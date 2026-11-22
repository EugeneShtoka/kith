package daemon

import (
	"context"
	"crypto"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/api/mesh/v1/meshv1connect"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

// errMailboxOverRemote is AddMailbox over [remote] listen: it is this machine's kithd
// that pairs with the mailbox.
var errMailboxOverRemote = errors.New("a bridge pairs with a mailbox over its own socket: run `kith mailbox add` on its machine")

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
