package daemon

import (
	"context"
	"crypto"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/api/mesh/v1/meshv1connect"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/human"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

// The pace of the mesh's loop (planning/design-mesh.md, Sync): each bridge dials every
// member bridge that has addresses, backing off from meshBackoffMin to meshBackoffMax
// while one does not answer; a stream that held for meshSteady starts the backoff
// over. With nothing to wake it, the loop looks again every meshRecheck.
const (
	meshBackoffMin = time.Second
	meshBackoffMax = 5 * time.Minute
	meshSteady     = time.Minute
	meshRecheck    = time.Minute
)

// Why a sync or a join is refused.
var (
	errSyncOverSocket = errors.New("sync is a bridge's call, over [remote] listen")
	errNotMeshBridge  = errors.New("only a bridge of the mesh may sync")
	errJoinOverRemote = errors.New("a bridge joins a mesh over its own socket: run `kith join --bridge` on its machine")
	errOwnPairing     = errors.New("that pairing is this bridge's own")
	errInThatMesh     = errors.New("this bridge is in that mesh already")
	errInAnotherMesh  = errors.New("this bridge is in a mesh with other bridges already: remove it there (`kith devices remove`) before it joins another")
	errDigitsDiffer   = errors.New("the bridge shows other digits than this one worked out; nothing was paired")
)

// poke wakes the mesh's loop.
func (d *Devices) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// meshParts is what syncing needs: the mesh store, kithd's certificate, ID and key.
func (d *Devices) meshParts() (RosterStore, tls.Certificate, devicekey.ID, crypto.Signer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cert, self, err := d.identityLocked()
	if err != nil {
		return nil, tls.Certificate{}, devicekey.ID{}, nil, err
	}
	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, tls.Certificate{}, devicekey.ID{}, nil, errors.New("daemon: kithd's device key cannot sign")
	}
	store, err := d.store()
	if err != nil {
		return nil, tls.Certificate{}, devicekey.ID{}, nil, fmt.Errorf("daemon: the mesh store: %w", err)
	}
	return store, cert, self, signer, nil
}

// runMesh is the mesh's loop while Serve runs: after every change to the store (a write
// here, or one a peer sent) it settles the roster as a change made here would (the file
// rendered, the removed cut off), keeps this bridge's own addresses in its entry, and
// dials every other bridge that has addresses, each on a loop of its own.
func (d *Devices) runMesh(ctx context.Context) {
	type dialing struct {
		addrs []string
		stop  context.CancelFunc
	}
	dialers := map[devicekey.ID]dialing{}
	var running sync.WaitGroup
	defer func() {
		for _, dl := range dialers {
			dl.stop()
		}
		running.Wait()
	}()
	for {
		changed, want := d.synced(ctx)
		for id, dl := range dialers {
			if addrs, ok := want[id]; !ok || !slices.Equal(addrs, dl.addrs) {
				dl.stop()
				delete(dialers, id)
			}
		}
		for id, addrs := range want {
			if _, ok := dialers[id]; ok {
				continue
			}
			dctx, stop := context.WithCancel(ctx)
			dialers[id] = dialing{addrs: addrs, stop: stop}
			running.Go(func() { d.dial(dctx, id, addrs) })
		}
		timer := time.NewTimer(meshRecheck)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-changed:
		case <-d.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// synced settles the roster after a change and is the bridges to dial, by ID, and
// what closes on the store's next change (nil when the store is not to be had). A
// bridge removed from its mesh stops listening, and dials on only to pass the news on:
// each bridge refuses it once it holds the removal.
func (d *Devices) synced(ctx context.Context) (<-chan struct{}, map[devicekey.ID][]string) {
	addrs, derr := d.dialable() // none: this bridge only dials
	store, _, self, signer, err := d.meshParts()
	if err != nil {
		d.log.Debug("the mesh waits for its store and kithd's key", "err", err)
		return nil, nil
	}
	changed := store.Changed() // before reading, so nothing committed after is missed
	d.mu.Lock()
	if d.refreshed {
		err = d.settleLocked(&rosterRead{store: store, signer: signer, file: d.rendered, entries: d.known})
	} else {
		_, err = d.refreshLocked()
	}
	d.mu.Unlock()
	st, serr := store.Status(self)
	if err = errors.Join(err, serr); serr != nil || (err != nil && !errors.Is(err, errOutOfMesh)) {
		d.log.Warn("the mesh's roster", "err", err)
		return changed, nil
	}
	d.mu.Lock()
	newlyOut := st.Removed && !d.outOfMesh
	d.outOfMesh = st.Removed
	d.mu.Unlock()
	if newlyOut {
		d.log.Error("not listening; syncing only to pass the removal on", "err", errOutOfMesh)
		d.relisten() // the roster no longer reads as this bridge's: the listeners stop
	}
	// A listener the config names but that is down for now (a roster that does not read,
	// an address not bound yet) keeps the addresses published: the other bridges go on
	// dialing it, and it answers once it listens again. Only a config naming no address
	// takes them back.
	down := derr != nil && d.wantsListening()
	if !st.Removed && !down && !slices.Equal(st.Addresses, addrs) {
		d.publishAddresses(ctx, store, signer, addrs)
	}
	want := map[devicekey.ID][]string{}
	for i := range st.Peers {
		p := &st.Peers[i]
		if id, perr := devicekey.ParseID(p.Bridge.ID); perr == nil && !p.Forked && len(p.Addresses) > 0 {
			want[id] = p.Addresses
		}
	}
	return changed, want
}

// wantsListening reports whether the config names addresses to listen on.
func (d *Devices) wantsListening() bool {
	d.lmu.Lock()
	defer d.lmu.Unlock()
	return len(d.want) > 0
}

// publishAddresses writes the addresses this bridge is dialed at into its own entry.
func (d *Devices) publishAddresses(ctx context.Context, store RosterStore, signer crypto.Signer, addrs []string) {
	members, _, err := store.Roster()
	if err != nil {
		d.log.Warn("write this bridge's addresses into the mesh", "err", err)
		return
	}
	d.mu.Lock()
	self := d.self.String()
	d.mu.Unlock()
	if i := slices.IndexFunc(members, func(m domain.Device) bool { return m.ID == self }); i >= 0 {
		if err := store.Bridge(ctx, signer, members[i], nil, addrs); err != nil {
			d.log.Warn("write this bridge's addresses into the mesh", "err", err)
		}
	}
}

// relisten reconciles the listeners with the roster as it reads now.
func (d *Devices) relisten() {
	d.lmu.Lock()
	defer d.lmu.Unlock()
	d.reconcileLocked()
}

// dial keeps a sync stream open with the bridge peer at addrs while ctx lasts: when
// none is (the peer may have dialed this bridge), it dials, backing off while the peer
// does not answer.
func (d *Devices) dial(ctx context.Context, peer devicekey.ID, addrs []string) {
	backoff := meshBackoffMin
	for ctx.Err() == nil {
		store, cert, _, signer, err := d.meshParts()
		if err == nil && store.Connected(peer) {
			backoff = meshBackoffMin
			if !sleepUntil(ctx, meshRecheck) {
				return
			}
			continue
		}
		started := time.Now()
		if err == nil {
			err = d.syncWith(ctx, store, cert, signer, peer, addrs)
		}
		if time.Since(started) >= meshSteady {
			backoff = meshBackoffMin
		}
		if err != nil && ctx.Err() == nil {
			d.log.Info("no sync with a bridge of the mesh; trying again", "bridge", peer.String(), "in", human.Duration(backoff), "err", err)
		}
		if !sleepUntil(ctx, backoff) {
			return
		}
		backoff = min(2*backoff, meshBackoffMax)
	}
}

// syncWith runs one sync session with peer, dialed at addrs.
func (d *Devices) syncWith(ctx context.Context, store RosterStore, cert tls.Certificate, signer crypto.Signer, peer devicekey.ID, addrs []string) error {
	hc := dialBridge(cert, peer, addrs)
	defer hc.CloseIdleConnections()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := meshv1connect.NewMeshServiceClient(hc, bridgeBaseURL).Sync(ctx)
	err := store.Sync(ctx, signer, peer, true, clientFrames{stream}, d.log)
	cancel() // the stream's other half, still reading, ends with it
	_ = stream.CloseRequest()
	_ = stream.CloseResponse()
	if err != nil {
		return fmt.Errorf("daemon: sync: %w", err)
	}
	return nil
}

// clientFrames is the dialing side's stream as mesh frames.
type clientFrames struct {
	st *connect.BidiStreamForClient[meshv1.SyncRequest, meshv1.SyncResponse]
}

func (f clientFrames) Send(frame *meshv1.SyncFrame) error {
	if err := f.st.Send(&meshv1.SyncRequest{Frame: frame}); err != nil {
		return fmt.Errorf("daemon: send a sync frame: %w", err)
	}
	return nil
}

// Receive reads the next frame; io.EOF (wrapped) once the other side closed.
func (f clientFrames) Receive() (*meshv1.SyncFrame, error) {
	r, err := f.st.Receive()
	if err != nil {
		return nil, fmt.Errorf("daemon: read a sync frame: %w", err)
	}
	return r.GetFrame(), nil
}

// serverFrames is the dialed side's stream as mesh frames.
type serverFrames struct {
	st *connect.BidiStream[meshv1.SyncRequest, meshv1.SyncResponse]
}

func (f serverFrames) Send(frame *meshv1.SyncFrame) error {
	if err := f.st.Send(&meshv1.SyncResponse{Frame: frame}); err != nil {
		return fmt.Errorf("daemon: send a sync frame: %w", err)
	}
	return nil
}

// Receive reads the next frame; io.EOF (wrapped) once the other side closed.
func (f serverFrames) Receive() (*meshv1.SyncFrame, error) {
	r, err := f.st.Receive()
	if err != nil {
		return nil, fmt.Errorf("daemon: read a sync frame: %w", err)
	}
	return r.GetFrame(), nil
}

// bridgePeer is the gate in front of Sync: the bridge a request came from, if it came
// over [remote] listen from a key the roster lists as a bridge. The socket, a device,
// and a key let in only to pair are refused.
func (d *Devices) bridgePeer(ctx context.Context) (devicekey.ID, error) {
	c, ok := ctx.Value(peerKey{}).(*remoteConn)
	if !ok {
		return devicekey.ID{}, errSyncOverSocket
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !c.onlyPairs && slices.ContainsFunc(d.paired, func(p pairedDevice) bool { return p.id == c.id && p.Role == domain.RoleBridge }) {
		return c.id, nil
	}
	return devicekey.ID{}, errNotMeshBridge
}

// meshService serves MeshService on the listener's mux.
type meshService struct {
	devices *Devices
	log     *slog.Logger
}

var _ meshv1connect.MeshServiceHandler = meshService{}

// Sync is a member bridge's sync session, behind bridgePeer.
func (m meshService) Sync(ctx context.Context, st *connect.BidiStream[meshv1.SyncRequest, meshv1.SyncResponse]) error {
	peer, err := m.devices.bridgePeer(ctx)
	if err != nil {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	store, _, _, signer, err := m.devices.meshParts()
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	if err := store.Sync(ctx, signer, peer, false, serverFrames{st}, m.log); err != nil {
		return connect.NewError(connect.CodeAborted, err)
	}
	return nil
}

// joinMesh joins this bridge to the mesh of the bridge inv names (kith join --bridge):
// it answers the pairing as a bridge, dialed at this bridge's addresses, and once the
// pairing is confirmed there takes the mesh's state (RosterStore.Join). say gets the
// devices this bridge brings, the digits, how the pairing ended, and the members it
// admits once joined. A pairing confirmed whose outcome was lost (or one confirmed
// before) is joined all the same: the mesh lets in only a bridge it lists.
func (d *Devices) joinMesh(ctx context.Context, inv pairing.Invite, name string, say func(*v1.JoinMeshResponse) error) error {
	store, cert, self, signer, err := d.meshParts()
	if err != nil {
		return err
	}
	if inv.ID == self {
		return errOwnPairing
	}
	members, removed, err := store.Roster()
	if err != nil {
		return fmt.Errorf("daemon: the mesh store: %w", err)
	}
	// A bridge removed from its mesh may join one again; one in a mesh may not.
	out := slices.Contains(removed, self.String())
	var bringing []domain.Device
	for _, m := range members {
		switch {
		case m.ID == self.String():
		case !out && m.ID == inv.ID.String():
			return errInThatMesh
		case !out && m.Role == domain.RoleBridge:
			return errInAnotherMesh
		case m.Role == domain.RoleDevice:
			bringing = append(bringing, m)
		}
	}
	if err := say(&v1.JoinMeshResponse{Event: &v1.JoinMeshResponse_Bringing{Bringing: &v1.MeshDevices{Devices: pc.DevicesToProto(bringing)}}}); err != nil {
		return err
	}
	addrs, _ := d.dialable() // none: the mesh's bridges never dial this one, it dials them
	outcome, perr := d.pairAsBridge(ctx, cert, self, inv, name, addrs, say)
	if errors.Is(perr, errDigitsDiffer) || ctx.Err() != nil {
		return perr
	}
	joined, jerr := d.adopt(ctx, store, cert, self, signer, inv)
	switch {
	case jerr != nil && (perr != nil || outcome != domain.PairingConfirmed):
		if perr != nil {
			return perr
		}
		return say(&v1.JoinMeshResponse{Event: &v1.JoinMeshResponse_Outcome{Outcome: pc.PairingOutcomeToProto(outcome)}})
	case jerr != nil:
		return fmt.Errorf("daemon: paired, but the mesh's state did not arrive (run the same `kith join --bridge` again: it takes it without asking again): %w", jerr)
	}
	d.log.Info("joined a mesh", "bridge", inv.ID.String(), "members", len(joined))
	if err := say(&v1.JoinMeshResponse{Event: &v1.JoinMeshResponse_Outcome{Outcome: pc.PairingOutcomeToProto(domain.PairingConfirmed)}}); err != nil {
		return err
	}
	return say(&v1.JoinMeshResponse{Event: &v1.JoinMeshResponse_Joined{Joined: &v1.MeshDevices{Devices: pc.DevicesToProto(joined)}}})
}

// pairAsBridge answers inv's pairing as this bridge (self), checking the digits the
// other side shows against its own; say gets them.
func (d *Devices) pairAsBridge(ctx context.Context, cert tls.Certificate, self devicekey.ID, inv pairing.Invite, name string,
	addrs []string, say func(*v1.JoinMeshResponse) error,
) (domain.PairingOutcome, error) {
	remote := NewBridgeRemote(cert, inv.ID, inv.Addresses)
	defer remote.Stop()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	want := pairing.Digits(inv.ID, self, inv.Token)
	outcome, err := remote.pair(ctx, &v1.PairRequest{
		Token: inv.Token.String(), Name: name, Role: string(domain.RoleBridge), Addresses: addrs,
	}, func(got string) {
		if got != want {
			cancel(errDigitsDiffer) // ends the pairing unconfirmed
			return
		}
		if err := say(&v1.JoinMeshResponse{Event: &v1.JoinMeshResponse_Digits{Digits: want}}); err != nil {
			cancel(err)
		}
	})
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return 0, fmt.Errorf("daemon: join: %w", cause)
	}
	return outcome, err
}

// adopt takes the mesh's state from its bridge inv names, then settles the roster
// here and listens again: the mesh's devices are admitted from now on.
func (d *Devices) adopt(ctx context.Context, store RosterStore, cert tls.Certificate, self devicekey.ID, signer crypto.Signer,
	inv pairing.Invite,
) ([]domain.Device, error) {
	hc := dialBridge(cert, inv.ID, inv.Addresses)
	defer hc.CloseIdleConnections()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := meshv1connect.NewMeshServiceClient(hc, bridgeBaseURL).Sync(sctx)
	joined, err := store.Join(sctx, self, inv.ID, signer, clientFrames{stream})
	cancel()
	_ = stream.CloseRequest()
	_ = stream.CloseResponse()
	if err != nil {
		return nil, fmt.Errorf("daemon: join: %w", err)
	}
	d.mu.Lock()
	d.outOfMesh = false
	r := rosterRead{store: store, signer: signer, file: d.rendered, entries: d.known}
	if d.refreshed {
		err = d.settleLocked(&r)
	} else {
		_, err = d.refreshLocked()
	}
	d.mu.Unlock()
	d.relisten()
	return joined, err
}

// meshStatus is the mesh as this bridge sees it.
func (d *Devices) meshStatus() (domain.MeshStatus, error) {
	store, _, self, _, err := d.meshParts()
	if err != nil {
		return domain.MeshStatus{}, err
	}
	st, err := store.Status(self)
	if err != nil {
		return domain.MeshStatus{}, fmt.Errorf("daemon: %w", err)
	}
	return st, nil
}

// publishAccounts writes the accounts this bridge runs into the mesh.
func (d *Devices) publishAccounts(ctx context.Context, accounts []domain.Account) {
	store, _, _, signer, err := d.meshParts()
	if err != nil {
		return
	}
	list := make([]domain.MeshAccount, 0, len(accounts))
	for _, a := range accounts {
		list = append(list, domain.MeshAccount{Network: string(a.Network), ID: string(a.On), Label: a.Name})
	}
	if err := store.PublishAccounts(ctx, signer, list); err != nil {
		d.log.Debug("publish this bridge's accounts to the mesh", "err", err)
	}
}

// publishAccounts keeps this bridge's accounts in the mesh (bridge/<ID>/accounts) as it
// logs in and out, and after the store changes (a join drops what was there) until
// ctx ends.
func (s *server) publishAccounts(ctx context.Context) {
	id, changes := s.Streams.changes.watchWith(subBuffer)
	defer s.Streams.changes.release(id)
	for {
		if accounts, err := s.Backend.Accounts(ctx); err == nil {
			s.Paired.publishAccounts(ctx, accounts)
		}
		var changed <-chan struct{}
		if store, _, _, _, err := s.Paired.meshParts(); err == nil {
			changed = store.Changed()
		}
		timer := time.NewTimer(meshRecheck)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case c, ok := <-changes:
			if !ok {
				changes = nil
			} else if c.what != domain.ChangeAccounts {
				timer.Stop()
				continue
			}
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// startMesh runs publishAccounts and followReads until ctx ends; wait returns once
// both have.
func (s *server) startMesh(ctx context.Context) (wait func()) {
	var running sync.WaitGroup
	running.Go(func() { s.publishAccounts(ctx) })
	running.Go(func() { s.followReads(ctx) })
	return running.Wait
}

// ── Handlers ──

// JoinMesh has this kithd join the mesh of the bridge a pairing names: over the
// socket only, as it is this machine's kithd that joins.
func (s *server) JoinMesh(ctx context.Context, r *req[v1.JoinMeshRequest], st *connect.ServerStream[v1.JoinMeshResponse]) error {
	if s.Paired.peer(ctx) != nil {
		return connect.NewError(connect.CodePermissionDenied, errJoinOverRemote)
	}
	inv, err := pairing.ParseURI(r.Msg.GetUri())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	err = s.Paired.joinMesh(ctx, inv, r.Msg.GetName(), func(ev *v1.JoinMeshResponse) error { return sendFrame(st, ev) })
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errOwnPairing):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, errInThatMesh), errors.Is(err, errInAnotherMesh), errors.Is(err, errDigitsDiffer):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if cerr, ok := errors.AsType[*connect.Error](err); ok {
		return cerr
	}
	return rpcErr(err)
}

// MeshStatus is this bridge's view of its mesh.
func (s *server) MeshStatus(ctx context.Context, _ *req[v1.MeshStatusRequest]) (*resp[v1.MeshStatusResponse], error) {
	st, err := s.Paired.meshStatus()
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(pc.MeshStatusToProto(st)), nil
}
