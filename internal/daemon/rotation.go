package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"slices"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// rotationGrace is how long kithd answers as the key it rotated from to a client or
// bridge naming that key (devicekey.ID.ServerName), which then learns the new one: the
// mesh's horizon (mesh.Horizon), past which a bridge unheard comes back by a snapshot.
// A client that has not dialed for longer is refused, and joins again.
const rotationGrace = 30 * 24 * time.Hour

// followTimeout bounds FollowRotation's question, so a bridge that does not answer
// leaves the dial that follows to say why.
const followTimeout = 10 * time.Second

// Why a rotation is refused.
var (
	errCannotRotate     = errors.New("this kithd keeps no key it could rotate")
	errRotateOverRemote = errors.New("a bridge rotates its key over its own socket: run `kith devices rotate --bridge` on its machine")
	errRotateOverSocket = errors.New("a device rotates its own key over [remote] listen: run `kith devices rotate` on it")
	errRotateABridge    = errors.New("a bridge rotates its key with `kith devices rotate --bridge`, on its machine")
)

// KeyKeeper keeps kithd's device key through a rotation: cmd/kithd's, over the keystore.
type KeyKeeper interface {
	// Rotated is the key kithd rotated from, and when; nil when it has none.
	Rotated() (*ecdsa.PrivateKey, time.Time, error)
	// Rotate keeps next as kithd's device key, and the one it replaces as rotated from
	// at at.
	Rotate(next *ecdsa.PrivateKey, at time.Time) error
}

// rotatedKey is the key kithd rotated from: as a certificate, its ID, and when.
type rotatedKey struct {
	cert tls.Certificate
	id   devicekey.ID
	at   time.Time
}

// Rotates has kithd keep its key through k, which lets it rotate the key and answer as
// the one it rotated from.
func (d *Devices) Rotates(k KeyKeeper) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keeper = k
}

// rotatedLocked is the key kithd rotated from while rotationGrace lasts at now: read
// once it reads (a keyring not answering is asked again).
func (d *Devices) rotatedLocked(now time.Time) *rotatedKey {
	if d.rotated == nil && d.keeper != nil {
		key, at, err := d.keeper.Rotated()
		switch {
		case err != nil:
			d.log.Warn("the key this bridge rotated from does not read: a client pinning it is refused", "err", err)
		case key == nil:
			d.rotated = &rotatedKey{}
		default:
			cert, cerr := devicekey.Certificate(key)
			id, ierr := devicekey.IDOf(key.Public())
			if err := errors.Join(cerr, ierr); err != nil {
				d.log.Warn("the key this bridge rotated from", "err", err)
			}
			d.rotated = &rotatedKey{cert: cert, id: id, at: at}
		}
	}
	if d.rotated == nil || d.rotated.cert.PrivateKey == nil || now.Sub(d.rotated.at) >= rotationGrace {
		return nil
	}
	return d.rotated
}

// answerAs is the certificate kithd answers c with, whose client named name: the key it
// rotated from, while rotationGrace lasts, to one naming that key (c is marked so);
// else its key now.
func (d *Devices) answerAs(name string, c *remoteConn) tls.Certificate {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.rotatedLocked(time.Now()); r != nil && name == r.id.ServerName() {
		c.asRotated = true
		return r.cert
	}
	cert, _, err := d.identityLocked()
	if err != nil {
		return tls.Certificate{} // no key, so no handshake; nothing listens without one anyway
	}
	return cert
}

// rotatedID is the key kithd rotated from, while rotationGrace lasts: what it says it
// is to a bridge that dialed it pinning that key, which learns the rotation from it.
func (d *Devices) rotatedID() (devicekey.ID, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.rotatedLocked(time.Now())
	if r == nil {
		return devicekey.ID{}, false
	}
	return r.id, true
}

// RotateBridge moves kithd to a new device key: the rotation, signed by both keys,
// written by the new one, and the old one kept to answer whoever still pins it, for
// rotationGrace. was and id are the old ID and the new.
func (d *Devices) RotateBridge(ctx context.Context) (was, id devicekey.ID, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.keeper == nil {
		return was, id, errCannotRotate
	}
	r, err := d.refreshLocked()
	if err != nil {
		return was, id, err
	}
	old, ok := d.cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return was, id, errCannotRotate
	}
	next, err := devicekey.Generate()
	if err != nil {
		return was, id, fmt.Errorf("daemon: rotate: %w", err)
	}
	cert, err := devicekey.Certificate(next)
	if err != nil {
		return was, id, fmt.Errorf("daemon: rotate: %w", err)
	}
	if id, err = devicekey.IDOf(next.Public()); err != nil {
		return was, id, fmt.Errorf("daemon: rotate: %w", err)
	}
	now := time.Now()
	// Kept first: a kithd stopped before the store takes the rotation finishes it at its
	// next start (finishRotationLocked), with both keys.
	if err := d.keeper.Rotate(next, now); err != nil {
		return was, id, fmt.Errorf("daemon: keep the new key: %w", err)
	}
	if err := r.store.RotateSelf(ctx, old, next); err != nil {
		d.rotated = nil
		if kerr := d.keeper.Rotate(old, time.Time{}); kerr != nil {
			err = errors.Join(err, fmt.Errorf("the old key could not be put back, so this bridge is out of its mesh until it pairs again: %w", kerr))
		}
		return was, id, fmt.Errorf("daemon: rotate: %w", err)
	}
	was = d.self
	d.rotated = &rotatedKey{cert: *d.cert, id: was, at: now}
	d.cert, d.self, r.signer = &cert, id, next
	d.log.Info("rotated this bridge's device key", "was", was.String(), "id", id.String())
	d.poke()
	return was, id, d.settleLocked(&r)
}

// finishRotationLocked writes the rotation of this bridge's key from the one it
// rotated from, when the store still holds that one as a bridge: a kithd stopped
// between keeping its new key and the store taking the rotation. False when there is
// none to finish.
func (d *Devices) finishRotationLocked(store RosterStore, members []domain.Device) bool {
	if d.keeper == nil {
		return false
	}
	old, _, err := d.keeper.Rotated()
	if err != nil || old == nil {
		return false
	}
	from, err := devicekey.IDOf(old.Public())
	if err != nil || !slices.ContainsFunc(members, func(m domain.Device) bool { return m.ID == from.String() && m.Role == domain.RoleBridge }) {
		return false
	}
	next, ok := d.cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return false
	}
	if err := store.RotateSelf(context.Background(), old, next); err != nil {
		d.log.Warn("finish this bridge's key rotation", "was", from.String(), "err", err)
		return false
	}
	d.log.Info("finished this bridge's key rotation, cut short by a stop", "was", from.String(), "id", d.self.String())
	return true
}

// RotateDevice moves the device c is (the key it presented) to key, signed by both keys
// (fromSig, toSig), written by this bridge: the old key is refused from then on. c's
// device's connections are cut after pairingCutGrace, so it reads the answer first.
func (d *Devices) RotateDevice(ctx context.Context, c *remoteConn, key, fromSig, toSig []byte) (domain.Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, err := d.refreshLocked()
	if err != nil {
		return domain.Device{}, err
	}
	rot := devicekey.Rotation{From: c.key, FromSig: fromSig, ToSig: toSig}
	if from, verr := rot.Verify(key); verr != nil || from != c.id {
		return domain.Device{}, devicekey.ErrBadRotation
	}
	i := slices.IndexFunc(d.paired, func(p pairedDevice) bool { return p.id == c.id })
	switch {
	case i < 0:
		return domain.Device{}, errUnknownDevice
	case d.paired[i].Role == domain.RoleBridge:
		return domain.Device{}, errRotateABridge
	}
	conns := d.conns[c.id]
	delete(d.conns, c.id) // settling would cut them before the answer is read
	if err := r.store.Rotate(ctx, r.signer, c.id, key, rot); err != nil {
		d.conns[c.id] = conns
		return domain.Device{}, fmt.Errorf("daemon: rotate: %w", err)
	}
	time.AfterFunc(pairingCutGrace, func() {
		for conn := range conns {
			_ = conn.Conn.Close() // the old key is refused now; its handlers see the close
		}
	})
	if err := d.settleLocked(&r); err != nil {
		return domain.Device{}, err
	}
	next := devicekey.ID(sha256.Sum256(key)) // an ID is its key's hash (devicekey.IDOf)
	for _, p := range d.paired {
		if p.id == next {
			return p.Device, nil
		}
	}
	return domain.Device{}, errUnknownDevice
}

// FollowRotation is b as the bridge it names stands now. A bridge that rotated its key
// answers for rotationGrace on the key pinned, and says its ID is now another: the pin
// in the device file at path is rewritten to it. One that does not answer leaves b as
// it is, for the dial that follows to say why.
func FollowRotation(ctx context.Context, cert tls.Certificate, path string, b config.Bridge) (config.Bridge, error) {
	pin, err := bridgePin(b)
	if err != nil {
		return b, err
	}
	r := NewBridgeRemote(cert, pin, b.Addresses)
	defer r.Stop()
	ctx, cancel := context.WithTimeout(ctx, followTimeout)
	defer cancel()
	if self, _, err := r.Devices(ctx); err == nil {
		if now, perr := devicekey.ParseID(self); perr == nil && now != pin {
			return setup.RepinBridge(path, pin, now)
		}
	}
	return b, nil
}

// ── Handlers ──

// RotateDevice moves the calling device to a new key: over [remote] listen only.
func (s *server) RotateDevice(ctx context.Context, r *req[v1.RotateDeviceRequest]) (*resp[v1.RotateDeviceResponse], error) {
	c, ok := ctx.Value(peerKey{}).(*remoteConn)
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, errRotateOverSocket)
	}
	d, err := s.Paired.RotateDevice(ctx, c, r.Msg.GetKey(), r.Msg.GetFromSig(), r.Msg.GetToSig())
	switch {
	case errors.Is(err, devicekey.ErrBadRotation), errors.Is(err, errRotateABridge):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case err != nil:
		return nil, deviceErr(err)
	}
	return connect.NewResponse(&v1.RotateDeviceResponse{Device: pc.DeviceToProto(d)}), nil
}

// RotateBridge moves this kithd to a new device key: over its socket only.
func (s *server) RotateBridge(ctx context.Context, _ *req[v1.RotateBridgeRequest]) (*resp[v1.RotateBridgeResponse], error) {
	if s.Paired.peer(ctx) != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errRotateOverRemote)
	}
	was, id, err := s.Paired.RotateBridge(ctx)
	if err != nil {
		return nil, deviceErr(err)
	}
	return connect.NewResponse(&v1.RotateBridgeResponse{Was: was.String(), Id: id.String()}), nil
}
