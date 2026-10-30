package daemon_test

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/api/mesh/v1/meshv1connect"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

// uri is a pairing's ticket as `kith pair` prints it.
func uri(t *testing.T, ticket domain.PairingTicket) string {
	t.Helper()
	id, err := devicekey.ParseID(ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := pairing.ParseToken(ticket.Token)
	if err != nil {
		t.Fatal(err)
	}
	return pairing.Invite{Addresses: ticket.Addresses, ID: id, Token: token, Expires: ticket.Expires}.URI()
}

// joined is how a bridge's `kith join --bridge` ended.
type joined struct {
	outcome  domain.PairingOutcome
	members  []domain.Device
	bringing []domain.Device
	err      error
}

// joinMesh has b's kithd join the mesh of the pairing p opened, in the background.
func (b *bridge) joinMesh(p *opened) <-chan joined {
	b.t.Helper()
	out := make(chan joined, 1)
	go func() {
		var j joined
		j.outcome, j.members, j.err = b.socket.JoinMesh(b.t.Context(), uri(b.t, p.ticket), "laptop",
			func(d []domain.Device) { j.bringing = d }, func(string) {})
		out <- j
	}()
	return out
}

// join has b join a's mesh, confirmed on a's socket.
func join(t *testing.T, a, b *bridge) joined {
	t.Helper()
	p := a.openPairing(a.socket, "")
	done := b.joinMesh(p)
	pending := recv(t, p.pending, "the bridge asking to pair")
	if pending.Role != domain.RoleBridge || pending.ID != b.self.String() {
		t.Fatalf("pending %+v, want bridge %s", pending, b.self)
	}
	if _, err := a.socket.ConfirmPairing(t.Context(), p.ticket.Token, true); err != nil {
		t.Fatalf("ConfirmPairing: %v", err)
	}
	j := recv(t, done, "the join's end")
	if j.err != nil || j.outcome != domain.PairingConfirmed {
		t.Fatalf("JoinMesh = %v, %v", j.outcome, j.err)
	}
	return j
}

// eventually waits for ok.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * settle)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func lists(devices []domain.Device, id devicekey.ID) bool {
	return slices.ContainsFunc(devices, func(d domain.Device) bool { return d.ID == id.String() })
}

// connected reports whether b holds a sync stream open with peer.
func (b *bridge) connected(peer devicekey.ID) bool {
	b.t.Helper()
	st, err := b.socket.MeshStatus(b.t.Context())
	if err != nil {
		b.t.Fatal(err)
	}
	return slices.ContainsFunc(st.Peers, func(p domain.MeshPeer) bool { return p.Bridge.ID == peer.String() && p.Connected })
}

// A bridge joins another's mesh by its pairing: each admits the other's devices from
// then on, its own devices go with it, and a device unpaired on one is cut off by both.
func TestTwoBridgesShareTheirDevicesAndUnpairEverywhere(t *testing.T) {
	t.Parallel()
	a, b := serveBridge(t, bridgeOpts{}), serveBridge(t, bridgeOpts{})
	phone, laptop := newDeviceKey(t), newDeviceKey(t)
	a.pair(phone, "phone")
	b.pair(laptop, "laptop")
	j := join(t, a, b)
	if !lists(j.bringing, idOf(t, laptop)) || !lists(j.members, a.self) || !lists(j.members, idOf(t, phone)) {
		t.Fatalf("the join brought %v and admits %v; want the laptop brought, the bridge and its phone admitted", j.bringing, j.members)
	}
	// Listing reads the roster file for hand edits: one not rendered yet since the sync
	// must not read as the laptop taken out.
	eventually(t, "the first bridge admits the second's laptop", func() bool { return lists(a.devices(), idOf(t, laptop)) })
	eventually(t, "the phone paired with the first is served by the second", func() bool { return b.status(device{key: phone}) == nil })
	eventually(t, "the bridges sync", func() bool { return a.connected(b.self) && b.connected(a.self) })
	b.unpair(phone)
	eventually(t, "the phone unpaired on the second is refused by the first", func() bool { return a.status(device{key: phone}) != nil })
}

// syncAs opens Sync on b as d and is how it ended: the handshake refused, or the call.
func (b *bridge) syncAs(d device) error {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(b.t.Context(), settle)
	defer cancel()
	tr := b.transport(d)
	defer tr.CloseIdleConnections()
	st := meshv1connect.NewMeshServiceClient(&http.Client{Transport: tr}, "https://"+b.addr).Sync(ctx)
	defer func() { _ = st.CloseResponse() }()
	if err := st.Send(&meshv1.SyncRequest{Frame: &meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Hello{Hello: &meshv1.Hello{}}}}); err != nil {
		return err
	}
	_, err := st.Receive()
	return err
}

// Sync is served to a bridge of the mesh alone: a device, a key never paired, a key let
// in to answer a pairing, and the socket are each refused.
func TestOnlyABridgeOfTheMeshIsServedSync(t *testing.T) {
	t.Parallel()
	a := serveBridge(t, bridgeOpts{})
	phone := newDeviceKey(t)
	a.pair(phone, "phone")
	if err := a.syncAs(device{key: phone}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a device's Sync = %v, want permission denied", err)
	}
	if err := a.syncAs(device{key: newDeviceKey(t)}); err == nil || connect.CodeOf(err) == connect.CodeUnknown && err.Error() == "" {
		t.Errorf("an unpaired key's Sync = %v, want refused", err)
	}
	a.openPairing(a.socket, "")
	if err := a.syncAs(device{key: newDeviceKey(t), from: loopback(t)}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a key let in to pair: Sync = %v, want permission denied", err)
	}
	socket := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(filepath.Dir(a.roster), "kithd.sock"))
	}}}
	ctx, cancel := context.WithTimeout(t.Context(), settle)
	defer cancel()
	st := meshv1connect.NewMeshServiceClient(socket, "http://kithd").Sync(ctx)
	_ = st.Send(&meshv1.SyncRequest{})
	if _, err := st.Receive(); err == nil {
		t.Error("Sync over the socket was served")
	}
	_ = st.CloseResponse()
}

// A bridge unpaired while it syncs is cut off: its streams end, it is dialed no more,
// and its key is refused from then on.
func TestABridgeUnpairedMidStreamIsCutOff(t *testing.T) {
	t.Parallel()
	a, b := serveBridge(t, bridgeOpts{}), serveBridge(t, bridgeOpts{})
	join(t, a, b)
	eventually(t, "the bridges sync", func() bool { return a.connected(b.self) && b.connected(a.self) })
	if err := a.socket.RemoveDevice(t.Context(), b.self.String()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both ends of the stream end", func() bool { return !a.connected(b.self) && !b.connected(a.self) })
	if err := a.syncAs(device{key: b.key}); err == nil {
		t.Error("the unpaired bridge's key was served Sync")
	}
	time.Sleep(2 * time.Second) // the dial loops' first backoff, past
	if a.connected(b.self) || b.connected(a.self) {
		t.Error("a stream opened again with the unpaired bridge")
	}
}

// A join whose confirmation never comes (the other side refuses it, or goes away)
// changes nothing on either bridge.
func TestAJoinNotConfirmedChangesNothing(t *testing.T) {
	t.Parallel()
	for _, refuse := range []bool{false, true} {
		a, b := serveBridge(t, bridgeOpts{}), serveBridge(t, bridgeOpts{})
		laptop := newDeviceKey(t)
		b.pair(laptop, "laptop")
		p := a.openPairing(a.socket, "")
		done := b.joinMesh(p)
		recv(t, p.pending, "the bridge asking to pair")
		want := domain.PairingAbandoned
		if refuse {
			want = domain.PairingRefused
			if _, err := a.socket.ConfirmPairing(t.Context(), p.ticket.Token, false); err != nil {
				t.Fatal(err)
			}
		} else {
			p.cancel()
		}
		j := recv(t, done, "the join's end")
		if j.err != nil || j.outcome != want {
			t.Errorf("refused %v: JoinMesh = %v, %v; want %v", refuse, j.outcome, j.err, want)
		}
		if lists(a.devices(), b.self) {
			t.Errorf("refused %v: the first bridge lists the second", refuse)
		}
		if devices := b.devices(); len(devices) != 1 || !lists(devices, idOf(t, laptop)) {
			t.Errorf("refused %v: the second bridge lists %v, want its laptop alone", refuse, devices)
		}
		if st, err := b.socket.MeshStatus(t.Context()); err != nil || len(st.Peers) != 0 {
			t.Errorf("refused %v: the second bridge's mesh: %+v, %v; want no peer", refuse, st, err)
		}
	}
}
