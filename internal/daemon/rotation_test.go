package daemon_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/api/mesh/v1/meshv1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh"
)

// keeper keeps a bridge's key through a rotation in memory, as kithd's keystore does.
type keeper struct {
	mu      sync.Mutex
	key     *ecdsa.PrivateKey
	rotated *ecdsa.PrivateKey
	at      time.Time
}

func (k *keeper) Rotated() (*ecdsa.PrivateKey, time.Time, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.rotated, k.at, nil
}

func (k *keeper) Rotate(next *ecdsa.PrivateKey, at time.Time) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.key, k.rotated, k.at = next, k.key, at
	return nil
}

// rotatingBridge is a bridge that may rotate its key.
func rotatingBridge(t *testing.T) *bridge {
	t.Helper()
	key := newDeviceKey(t)
	b := serveBridge(t, bridgeOpts{key: key, keeper: &keeper{key: key}})
	b.waitListening(b.addr, true)
	return b
}

// rotateBridge rotates b's key over its socket and takes its new ID as b's.
func (b *bridge) rotateBridge() devicekey.ID {
	b.t.Helper()
	was, id, err := b.socket.RotateBridge(b.t.Context())
	if err != nil {
		b.t.Fatalf("RotateBridge: %v", err)
	}
	if was != b.self.String() || id == was {
		b.t.Fatalf("RotateBridge = %s → %s, want from %s to another", was, id, b.self)
	}
	next, err := devicekey.ParseID(id)
	if err != nil {
		b.t.Fatal(err)
	}
	b.self = next
	return next
}

// pinning is a Remote of b as the device key, pinning pin as a client does.
func (b *bridge) pinning(key *ecdsa.PrivateKey, pin devicekey.ID) *daemon.Remote {
	b.t.Helper()
	cert, err := devicekey.Certificate(key)
	if err != nil {
		b.t.Fatal(err)
	}
	r := daemon.NewBridgeRemote(cert, pin, []string{b.addr})
	b.t.Cleanup(r.Stop)
	return r
}

// A device rotates its own key over the old one, signed by both: from then on the
// bridge serves the new key under the device's name and refuses the old. A rotation
// signed by one key only, one over the socket, and one by a bridge's key are refused
// and change nothing.
func TestARotatedDeviceIsServedOnlyAsItsNewKey(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	laptop, next, stranger := newDeviceKey(t), newDeviceKey(t), newDeviceKey(t)
	b.pair(laptop, "laptop")
	rot, key, err := devicekey.Rotate(laptop, next)
	if err != nil {
		t.Fatal(err)
	}
	forged, _, err := devicekey.Rotate(stranger, stranger)
	if err != nil {
		t.Fatal(err)
	}
	c := b.client(device{key: laptop})
	for _, tc := range []struct {
		name string
		req  *v1.RotateDeviceRequest
	}{
		{"the new key's signature another's", &v1.RotateDeviceRequest{Key: key, FromSig: rot.FromSig, ToSig: forged.ToSig}},
		{"the old key's signature another's", &v1.RotateDeviceRequest{Key: key, FromSig: forged.FromSig, ToSig: rot.ToSig}},
		{"signed for another key", &v1.RotateDeviceRequest{Key: forged.From, FromSig: rot.FromSig, ToSig: rot.ToSig}},
	} {
		if _, rerr := c.RotateDevice(t.Context(), connect.NewRequest(tc.req)); connect.CodeOf(rerr) != connect.CodeInvalidArgument {
			t.Errorf("%s: RotateDevice = %v, want invalid argument", tc.name, rerr)
		}
	}
	if _, serr := b.socket.RotateDevice(t.Context(), key, rot); connect.CodeOf(serr) != connect.CodePermissionDenied {
		t.Errorf("over the socket: RotateDevice = %v, want permission denied", serr)
	}
	if err = b.status(device{key: laptop}); err != nil {
		t.Fatalf("a refused rotation cut the device off: %v", err)
	}
	// A connection the old key holds open when the rotation lands is cut too.
	open := b.client(device{key: laptop, keepAlive: true})
	if _, err = open.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); err != nil {
		t.Fatal(err)
	}

	got, err := c.RotateDevice(t.Context(), connect.NewRequest(&v1.RotateDeviceRequest{Key: key, FromSig: rot.FromSig, ToSig: rot.ToSig}))
	if err != nil {
		t.Fatalf("RotateDevice: %v", err)
	}
	if d := got.Msg.GetDevice(); d.GetId() != idOf(t, next).String() || d.GetName() != "laptop" || d.GetRole() != string(domain.RoleDevice) {
		t.Fatalf("RotateDevice = %+v, want the laptop under its new ID", d)
	}
	if err := b.status(device{key: next}); err != nil {
		t.Errorf("the new key: %v", err)
	}
	eventually(t, "the old key is refused", func() bool { return b.status(device{key: laptop}) != nil })
	eventually(t, "the old key's open connection is cut", func() bool {
		_, err := open.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{}))
		return err != nil
	})
	if devices := b.devices(); lists(devices, idOf(t, laptop)) || !lists(devices, idOf(t, next)) {
		t.Errorf("the roster lists %+v, want the new key and not the old", devices)
	}
}

// A bridge that rotated its key answers a client naming its old one with that key for
// the grace, and says its new ID, which the client pins from then on; a client naming
// the new one, or none, is answered with the new key.
func TestAClientPinningARotatedBridgeRewritesItsPin(t *testing.T) {
	t.Parallel()
	b := rotatingBridge(t)
	laptop := newDeviceKey(t)
	b.pair(laptop, "laptop")
	old := b.self
	if _, _, _, err := b.pinning(laptop, old).Status(t.Context()); err != nil {
		t.Fatalf("before the rotation: %v", err)
	}
	next := b.rotateBridge()
	if _, _, _, err := b.pinning(laptop, old).Status(t.Context()); err != nil {
		t.Errorf("pinning the old key, within the grace: %v", err)
	}
	if _, _, _, err := b.pinning(laptop, next).Status(t.Context()); err != nil {
		t.Errorf("pinning the new key: %v", err)
	}
	if err := b.status(device{key: laptop}); err != nil {
		t.Errorf("naming no key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "device.toml")
	entry := config.Bridge{Name: "home", Addresses: []string{b.addr}, ID: old.String()}
	if _, err := config.UpdateDevice(path, config.Device{}, config.Device{Bridges: []config.Bridge{entry}}); err != nil {
		t.Fatal(err)
	}
	cert, err := devicekey.Certificate(laptop)
	if err != nil {
		t.Fatal(err)
	}
	followed, err := daemon.FollowRotation(t.Context(), cert, path, entry)
	if err != nil || followed.ID != next.String() || followed.Name != "home" {
		t.Fatalf("FollowRotation = %+v, %v; want home pinned to %s", followed, err, next)
	}
	dev, err := config.LoadDevice(path)
	if err != nil || len(dev.Bridges) != 1 || dev.Bridges[0].ID != next.String() {
		t.Fatalf("the device file holds %+v, %v; want the new pin", dev.Bridges, err)
	}
	again, err := daemon.FollowRotation(t.Context(), cert, path, followed)
	if err != nil || again.ID != followed.ID {
		t.Errorf("following again = %+v, %v; want it as it was", again, err)
	}
}

// Past the grace a bridge answers only with its new key: a client still pinning the old
// one is refused, and told to join again. Within it, the old key answers.
func TestAClientOfflinePastTheGraceIsToldToJoinAgain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		ago    time.Duration
		served bool
	}{
		{"within the grace", 29 * 24 * time.Hour, true},
		{"past the grace", 31 * 24 * time.Hour, false},
	} {
		key, old := newDeviceKey(t), newDeviceKey(t)
		b := serveBridge(t, bridgeOpts{key: key, keeper: &keeper{key: key, rotated: old, at: time.Now().Add(-tc.ago)}})
		b.waitListening(b.addr, true)
		laptop := newDeviceKey(t)
		b.pair(laptop, "laptop")
		_, _, _, err := b.pinning(laptop, idOf(t, old)).Status(t.Context())
		if (err == nil) != tc.served {
			t.Errorf("%s: pinning the old key = %v, want served %v", tc.name, err, tc.served)
		}
		if err != nil && !strings.Contains(err.Error(), "`kith join` it again") {
			t.Errorf("%s: the refusal %q does not say to join again", tc.name, err)
		}
	}
}

// A bridge of the mesh still pinning a bridge's old key reaches it on that key, learns
// the rotation from it, and syncs with the new key from then on: the devices of each are
// still served by the other.
func TestAPeerLearnsABridgesRotationFromIt(t *testing.T) {
	t.Parallel()
	a, b := rotatingBridge(t), rotatingBridge(t)
	phone, laptop := newDeviceKey(t), newDeviceKey(t)
	a.pair(phone, "phone")
	b.pair(laptop, "laptop")
	join(t, a, b)
	eventually(t, "the bridges sync", func() bool { return a.connected(b.self) && b.connected(a.self) })
	old := a.self
	next := a.rotateBridge()
	if replica := a.helloTo(b.key, old); !bytes.Equal(replica, old[:]) {
		t.Errorf("dialed pinning its old key, the rotated bridge says hello as %x, want the old key %s", replica, old)
	}
	eventually(t, "the peer lists the new key as a bridge, and not the old", func() bool {
		devices := b.devices()
		return lists(devices, next) && !lists(devices, old)
	})
	eventually(t, "the bridges sync under the new key", func() bool { return a.connected(b.self) && b.connected(next) })
	eventually(t, "the laptop is served by the rotated bridge", func() bool { return a.status(device{key: laptop}) == nil })
	if err := b.status(device{key: phone}); err != nil {
		t.Errorf("the phone on the peer: %v", err)
	}
}

// A kithd stopped after keeping its new key, before its store took the rotation, comes
// up under the new key all the same: it writes the rotation then, with both keys.
func TestARotationCutShortIsFinishedAtTheNextStart(t *testing.T) {
	t.Parallel()
	old, next := newDeviceKey(t), newDeviceKey(t)
	dir, meshKey := t.TempDir(), storeKey(t)
	store, err := mesh.Open(t.Context(), filepath.Join(dir, "mesh.db"), meshKey)
	if err != nil {
		t.Fatal(err)
	}
	self := domain.Device{ID: idOf(t, old).String(), Role: domain.RoleBridge, AddedAt: time.Now().UTC().Truncate(time.Second), AddedBy: idOf(t, old).String()}
	if err = store.ChangeRoster(context.Background(), old, []domain.Device{self}, nil); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	b := serveBridge(t, bridgeOpts{key: next, dir: dir, meshKey: meshKey, keeper: &keeper{key: next, rotated: old, at: time.Now()}})
	b.waitListening(b.addr, true)
	self2, _, err := b.socket.Devices(t.Context())
	if err != nil || self2 != idOf(t, next).String() {
		t.Fatalf("Devices = %s, %v; want the new key", self2, err)
	}
	b.waitLog("finished this bridge's key rotation")
}

// helloTo opens Sync on b as the bridge whose key is key, pinning pin as a bridge of the
// mesh dials it, and is the replica b's hello names.
func (b *bridge) helloTo(key *ecdsa.PrivateKey, pin devicekey.ID) []byte {
	b.t.Helper()
	cert, err := devicekey.Certificate(key)
	if err != nil {
		b.t.Fatal(err)
	}
	conf := &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: pin.ServerName(), Certificates: []tls.Certificate{cert},
		InsecureSkipVerify: true, // pinned below
		VerifyConnection: func(state tls.ConnectionState) error {
			if id, perr := devicekey.PeerID(state); perr != nil || id != pin {
				return fmt.Errorf("the bridge answered as %v (%w), want %v", id, perr, pin)
			}
			return nil
		},
	}
	tr := &http.Transport{TLSClientConfig: conf, ForceAttemptHTTP2: true}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(b.t.Context(), settle)
	defer cancel()
	st := meshv1connect.NewMeshServiceClient(&http.Client{Transport: tr}, "https://"+b.addr).Sync(ctx)
	defer func() { _ = st.CloseResponse() }()
	if err = st.Send(&meshv1.SyncRequest{Frame: &meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Hello{Hello: &meshv1.Hello{}}}}); err != nil && !errors.Is(err, io.EOF) {
		b.t.Fatal(err)
	}
	r, err := st.Receive()
	if err != nil {
		b.t.Fatalf("Sync pinning %s: %v", pin, err)
	}
	return r.GetFrame().GetHello().GetReplica()
}
