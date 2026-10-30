package daemon_test

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

// pairingEnd is how one side's stream of a pairing ended.
type pairingEnd struct {
	outcome domain.PairingOutcome
	err     error
}

// opened is a pairing opened as `kith pair` opens it.
type opened struct {
	ticket  domain.PairingTicket
	pending chan domain.PendingDevice
	ended   chan pairingEnd
	cancel  context.CancelFunc
}

// openPairing opens a pairing named name through r (the socket, or a paired device).
func (b *bridge) openPairing(r *daemon.Remote, name string) *opened {
	b.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p := &opened{pending: make(chan domain.PendingDevice, 1), ended: make(chan pairingEnd, 1), cancel: cancel}
	tickets := make(chan domain.PairingTicket, 1)
	go func() {
		o, err := r.StartPairing(ctx, name, func(t domain.PairingTicket) { tickets <- t }, func(d domain.PendingDevice) { p.pending <- d })
		p.ended <- pairingEnd{o, err}
	}()
	b.t.Cleanup(cancel)
	select {
	case p.ticket = <-tickets:
	case e := <-p.ended:
		b.t.Fatalf("StartPairing ended before its ticket: %v, %v", e.outcome, e.err)
	case <-time.After(upSettle): // the ticket waits on the bridge's store and key
		b.t.Fatal("StartPairing sent no ticket")
	}
	return p
}

// joiner is a device reaching the listener, unpaired, on one connection it watches.
type joiner struct {
	key    *ecdsa.PrivateKey
	c      backendv1connect.BackendServiceClient
	cut    chan struct{} // closed when the bridge closes the connection
	digits chan string
	ended  chan pairingEnd
	cancel context.CancelFunc
}

// cutWatch closes cut the first time a read from the bridge fails: it closed the
// connection.
type cutWatch struct {
	net.Conn
	once *sync.Once
	cut  chan struct{}
}

func (w cutWatch) Read(p []byte) (int, error) {
	n, err := w.Conn.Read(p)
	if err != nil {
		w.once.Do(func() { close(w.cut) })
	}
	return n, err
}

func (b *bridge) joiner(key *ecdsa.PrivateKey) *joiner {
	b.t.Helper()
	j := &joiner{key: key, cut: make(chan struct{}), digits: make(chan string, 1), ended: make(chan pairingEnd, 1)}
	tr := b.transport(device{key: key, keepAlive: true, from: loopback(b.t)})
	dial, once := tr.DialContext, &sync.Once{}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return cutWatch{Conn: c, once: once, cut: j.cut}, nil
	}
	b.t.Cleanup(tr.CloseIdleConnections)
	j.c = backendv1connect.NewBackendServiceClient(&http.Client{Transport: tr}, "https://"+b.addr)
	return j
}

// pair answers the pairing under token as name, in the background.
func (j *joiner) pair(t *testing.T, token, name string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	t.Cleanup(cancel)
	go func() {
		o, err := readPair(ctx, j.c, token, name, func(d string) { j.digits <- d })
		j.ended <- pairingEnd{o, err}
	}()
}

func readPair(ctx context.Context, c backendv1connect.BackendServiceClient, token, name string, digits func(string)) (domain.PairingOutcome, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*settle)
	defer cancel()
	st, err := c.Pair(ctx, connect.NewRequest(&v1.PairRequest{Token: token, Name: name}))
	if err != nil {
		return 0, err
	}
	defer func() { _ = st.Close() }()
	for st.Receive() {
		switch ev := st.Msg().GetEvent().(type) {
		case *v1.PairResponse_Digits:
			digits(ev.Digits)
		case *v1.PairResponse_Outcome:
			o, _ := pc.ProtoToPairingOutcome(ev.Outcome)
			return o, nil
		}
	}
	return 0, errors.Join(st.Err(), errors.New("the stream ended without an outcome"))
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(settle):
		t.Fatalf("no %s", what)
		panic("unreachable")
	}
}

func (b *bridge) devices() []domain.Device {
	b.t.Helper()
	_, devices, err := b.socket.Devices(b.t.Context())
	if err != nil {
		b.t.Fatal(err)
	}
	return devices
}

func (b *bridge) remoteAs(key *ecdsa.PrivateKey, addrs ...string) *daemon.Remote {
	b.t.Helper()
	cert, err := devicekey.Certificate(key)
	if err != nil {
		b.t.Fatal(err)
	}
	if len(addrs) == 0 {
		addrs = []string{b.addr}
	}
	r := daemon.NewBridgeRemote(cert, b.self, addrs)
	b.t.Cleanup(r.Stop)
	return r
}

// A whole pairing as `kith pair` and `kith join` make it, through the clients they use:
// the ticket names where to dial and whom to pin, both sides see the same digits, and
// once confirmed the new key is served every call on a new connection.
func TestAPairingFromTicketToServedKey(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	p := b.openPairing(b.socket, "")
	tk := p.ticket
	if len(tk.Addresses) != 1 || tk.Addresses[0] != b.addr || tk.ID != b.self.String() {
		t.Fatalf("ticket %+v, want %s pinned at %s", tk, b.self, b.addr)
	}
	if left := time.Until(tk.Expires); left < 9*time.Minute || left > 10*time.Minute {
		t.Fatalf("the ticket expires in %s, want ten minutes", left)
	}
	token, err := pairing.ParseToken(tk.Token)
	if err != nil {
		t.Fatal(err)
	}

	key := newDeviceKey(t)
	// An address that does not answer first: the next is tried.
	joinAs := b.remoteAs(key, freeAddr(t, "127.0.0.1"), b.addr)
	digits, ended := make(chan string, 1), make(chan pairingEnd, 1)
	go func() {
		o, perr := joinAs.Pair(t.Context(), tk.Token, "laptop", func(d string) { digits <- d })
		ended <- pairingEnd{o, perr}
	}()
	want := pairing.Digits(b.self, idOf(t, key), token)
	if got := recv(t, digits, "digits for the device"); got != want {
		t.Fatalf("the device was shown %s, want %s", got, want)
	}
	pending := recv(t, p.pending, "pending device")
	if pending != (domain.PendingDevice{Name: "laptop", ID: idOf(t, key).String(), Digits: want, Role: domain.RoleDevice}) {
		t.Fatalf("pending %+v, want laptop %s showing %s", pending, idOf(t, key), want)
	}
	added, err := b.socket.ConfirmPairing(t.Context(), tk.Token, true)
	if err != nil || added.ID != idOf(t, key).String() || added.Name != "laptop" || added.AddedBy != b.self.String() {
		t.Fatalf("ConfirmPairing = %+v, %v; want laptop added by the bridge", added, err)
	}
	for side, ch := range map[string]chan pairingEnd{"trusted": p.ended, "device": ended} {
		if e := recv(t, ch, side+" outcome"); e.err != nil || e.outcome != domain.PairingConfirmed {
			t.Errorf("the %s side ended %v, %v; want confirmed", side, e.outcome, e.err)
		}
	}
	if devices := b.devices(); len(devices) != 1 || devices[0].ID != idOf(t, key).String() {
		t.Fatalf("roster %+v, want the laptop", devices)
	}
	// Served everything now, on a connection of its own.
	served := b.remoteAs(key)
	if _, _, _, err := served.Status(t.Context()); err != nil {
		t.Fatalf("Status as the paired device: %v", err)
	}
	if rooms, err := served.Rooms(t.Context()); err != nil || len(rooms) != 1 {
		t.Fatalf("Rooms as the paired device = %v, %v", rooms, err)
	}
	// The token is spent.
	if _, err := b.socket.ConfirmPairing(t.Context(), tk.Token, true); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("confirming a spent token: %v, want not_found", err)
	}
}

// Each way a pairing ends, with a second pairing open beside it: what each side is
// told, whether the device is paired, the unpaired one's connection cut by its own
// pairing's end, the token never good again, and once no pairing is open, every key let
// in to pair cut and an unpaired key refused in the handshake once more.
func TestEveryWayAPairingEnds(t *testing.T) {
	t.Parallel()
	type stage struct {
		b *bridge
		p *opened
		j *joiner
	}
	confirm := func(accept bool) func(stage) {
		return func(s stage) {
			if _, err := s.b.socket.ConfirmPairing(s.b.t.Context(), s.p.ticket.Token, accept); err != nil {
				s.b.t.Fatalf("ConfirmPairing(%v): %v", accept, err)
			}
		}
	}
	expire := func(s stage) { s.b.paired.ExpirePairings() }
	for _, tc := range []struct {
		name string
		// answered: the device answers before the end; else it only connects.
		answered bool
		end      func(stage)
		// What each side is told; 0 is a side that went away and is told nothing.
		trusted, device domain.PairingOutcome
		// expires: the end fires every open pairing's timer, so the other opens after.
		expires bool
	}{
		{"confirmed", true, confirm(true), domain.PairingConfirmed, domain.PairingConfirmed, false},
		{"refused", true, confirm(false), domain.PairingRefused, domain.PairingRefused, false},
		{"refused unanswered", false, confirm(false), domain.PairingRefused, 0, false},
		{"expired unconfirmed", true, expire, domain.PairingExpired, domain.PairingExpired, true},
		{"expired unanswered", false, expire, domain.PairingExpired, 0, true},
		{"the trusted side went away", true, func(s stage) { s.p.cancel() }, 0, domain.PairingAbandoned, false},
		{"the trusted side went away unanswered", false, func(s stage) { s.p.cancel() }, 0, 0, false},
		{"the device went away", true, func(s stage) { s.j.cancel() }, domain.PairingAbandoned, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := serveBridge(t, bridgeOpts{})
			b.waitListening(b.addr, true)
			p := b.openPairing(b.socket, "")
			j := b.joiner(newDeviceKey(t))
			if tc.answered {
				j.pair(t, p.ticket.Token, "laptop")
				recv(t, j.digits, "digits")
				recv(t, p.pending, "pending device")
			} else {
				// Let in only to pair: anything else is refused, and it stays connected.
				if _, err := j.c.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
					t.Fatalf("Status while a pairing is open: %v, want permission_denied", err)
				}
			}
			var other *opened
			if !tc.expires {
				other = b.openPairing(b.socket, "")
			}
			tc.end(stage{b, p, j})
			if tc.trusted != 0 {
				if e := recv(t, p.ended, "trusted outcome"); e.err != nil || e.outcome != tc.trusted {
					t.Errorf("the trusted side ended %v, %v; want %v", e.outcome, e.err, tc.trusted)
				}
			}
			if tc.device != 0 {
				if e := recv(t, j.ended, "device outcome"); e.err != nil || e.outcome != tc.device {
					t.Errorf("the device ended %v, %v; want %v", e.outcome, e.err, tc.device)
				}
			}
			if other == nil {
				other = b.openPairing(b.socket, "")
			}
			confirmed := tc.trusted == domain.PairingConfirmed
			if devices := b.devices(); (len(devices) == 1) != confirmed {
				t.Fatalf("roster %+v, want the device paired %v", devices, confirmed)
			}
			switch {
			case confirmed:
				// The connection it paired over is its own, past any cut of those let in to
				// pair; unpairing it cuts it.
				select {
				case <-j.cut:
					t.Fatal("the paired device's connection was cut")
				case <-time.After(2 * time.Second):
				}
				b.unpair(j.key)
				recv(t, j.cut, "cut of the unpaired device's connection")
			case tc.answered:
				recv(t, j.cut, "cut of the device left unpaired")
			default:
				// Not an answer to this pairing: it may yet answer the other.
				select {
				case <-j.cut:
					t.Fatal("a key let in to pair was cut while a pairing is open")
				case <-time.After(2 * time.Second):
				}
			}

			// The token is good no more, while the other pairing lets keys in.
			late := b.joiner(newDeviceKey(t))
			if _, err := readPair(t.Context(), late.c, p.ticket.Token, "late", func(string) {}); connect.CodeOf(err) != connect.CodeNotFound {
				t.Errorf("Pair with the ended token: %v, want not_found", err)
			}
			other.cancel()
			recv(t, other.ended, "the other pairing's end")
			// With no pairing open, every key let in to pair is cut, and an unpaired key is
			// refused before any call.
			recv(t, late.cut, "cut of a key let in to pair, with no pairing open")
			recv(t, j.cut, "cut of the device, with no pairing open")
			if err := b.status(device{key: newDeviceKey(t), from: loopback(t)}); err == nil || connect.CodeOf(err) == connect.CodePermissionDenied {
				t.Errorf("an unpaired key with no pairing open: %v, want refused in the handshake", err)
			}
		})
	}
}

// One token, many devices at once: one is pending, every other is refused, and only
// that one is paired. Confirmed over TLS by a paired device, which is named as adding
// it.
func TestDevicesRacingOneTokenPairOne(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	trusted := newDeviceKey(t)
	b.pair(trusted, "desk")
	p := b.openPairing(b.remoteAs(trusted), "phone")

	const racers = 6
	joiners := make([]*joiner, racers)
	for i := range joiners {
		joiners[i] = b.joiner(newDeviceKey(t))
	}
	start := make(chan struct{})
	for _, j := range joiners {
		go func() {
			<-start
			// Each gives a name; the trusted side's wins.
			o, err := readPair(t.Context(), j.c, p.ticket.Token, "racer", func(d string) { j.digits <- d })
			j.ended <- pairingEnd{o, err}
		}()
	}
	close(start)
	pending := recv(t, p.pending, "pending device")
	if pending.Name != "phone" {
		t.Errorf("pending as %q, want the name the trusted side gave, phone", pending.Name)
	}
	var winner *joiner
	for _, j := range joiners {
		if idOf(t, j.key).String() == pending.ID {
			winner = j
			continue
		}
		if e := recv(t, j.ended, "a refused racer"); connect.CodeOf(e.err) != connect.CodeFailedPrecondition {
			t.Errorf("a second device on one token: %v, %v; want failed_precondition", e.outcome, e.err)
		}
	}
	if winner == nil {
		t.Fatalf("the pending device %s is none of the racers", pending.ID)
	}
	// The pending device cannot answer twice, nor confirm itself.
	if _, err := readPair(t.Context(), winner.c, p.ticket.Token, "", func(string) {}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("the pending device answering again: %v, want failed_precondition", err)
	}
	if _, err := winner.c.ConfirmPairing(t.Context(), connect.NewRequest(&v1.ConfirmPairingRequest{Token: p.ticket.Token, Accept: true})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("the pending device confirming itself: %v, want permission_denied", err)
	}
	added, err := b.remoteAs(trusted).ConfirmPairing(t.Context(), p.ticket.Token, true)
	if err != nil || added.AddedBy != idOf(t, trusted).String() {
		t.Fatalf("ConfirmPairing over TLS = %+v, %v; want added by desk", added, err)
	}
	if e := recv(t, winner.ended, "the winner's outcome"); e.outcome != domain.PairingConfirmed {
		t.Fatalf("the pending device ended %v, %v", e.outcome, e.err)
	}
	if devices := b.devices(); len(devices) != 2 || devices[1].ID != pending.ID {
		t.Fatalf("roster %+v, want desk and the one pending", devices)
	}
}

// A connection let in only to pair is refused every call but Pair, each by the gate:
// every procedure the service has, unary or streaming, asked over it.
func TestAKeyLetInToPairMayCallOnlyPair(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	b.openPairing(b.socket, "")
	tr := b.transport(device{key: newDeviceKey(t), keepAlive: true, from: loopback(t)})
	t.Cleanup(tr.CloseIdleConnections)
	hc := &http.Client{Transport: tr}

	methods := v1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods()
	for i := range methods.Len() {
		m := methods.Get(i)
		name := string(m.Name())
		if name == "Pair" {
			continue
		}
		contentType, body := "application/json", []byte("{}")
		if m.IsStreamingServer() {
			// One enveloped message: flags 0, then its length.
			contentType, body = "application/connect+json", append([]byte{0, 0, 0, 0, 2}, "{}"...)
		}
		// Bounded: a stream let through would never end.
		ctx, cancel := context.WithTimeout(t.Context(), settle)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://"+b.addr+"/"+string(m.Parent().FullName())+"/"+name, strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		if !strings.Contains(string(got), "permission_denied") || !strings.Contains(string(got), "only answer a pairing") {
			t.Errorf("%s over a connection let in only to pair: %d %s, want permission_denied", name, resp.StatusCode, got)
		}
	}
}

// Pair over the socket, from a device already paired, under a name another device
// has, or with no listener to dial: each refused, and none pairs anything.
func TestAPairingRefusedAtTheStart(t *testing.T) {
	t.Parallel()
	off := serveBridge(t, bridgeOpts{listen: []string{}})
	// Bounded: a pairing opened anyway would wait for a device.
	ctx, cancel := context.WithTimeout(t.Context(), settle)
	defer cancel()
	if _, err := off.socket.StartPairing(ctx, "", func(domain.PairingTicket) {}, func(domain.PendingDevice) {}); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "[remote] listen") {
		t.Errorf("StartPairing listening nowhere: %v, want failed_precondition naming [remote] listen", err)
	}

	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	p := b.openPairing(b.socket, "")
	socket := daemon.Client(filepath.Join(filepath.Dir(b.roster), "kithd.sock"))
	if _, err := readPair(t.Context(), socket, p.ticket.Token, "", func(string) {}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("Pair over the socket: %v, want invalid_argument", err)
	}
	already := newDeviceKey(t)
	b.pair(already, "laptop")
	if _, err := readPair(t.Context(), b.client(device{key: already, from: loopback(t)}), p.ticket.Token, "", func(string) {}); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("Pair from a paired device: %v, want already_exists", err)
	}
	if _, err := readPair(t.Context(), b.joiner(newDeviceKey(t)).c, p.ticket.Token, "laptop", func(string) {}); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("Pair under a name taken: %v, want already_exists", err)
	}
	if _, err := readPair(t.Context(), b.joiner(newDeviceKey(t)).c, "not a token", "", func(string) {}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("Pair with no token: %v, want invalid_argument", err)
	}
	// None of those took the token: a device may still answer it.
	j := b.joiner(newDeviceKey(t))
	j.pair(t, p.ticket.Token, "phone")
	recv(t, j.digits, "digits after the refusals")
	if devices := b.devices(); len(devices) != 1 {
		t.Fatalf("roster %+v, want the laptop alone", devices)
	}
}

// A device that pins another key than the bridge's refuses it in the handshake: the
// bridge never sees a Pair, and nothing is pending.
func TestAJoinerPinningAnotherKeyNeverReachesPair(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	p := b.openPairing(b.socket, "")
	cert, err := devicekey.Certificate(newDeviceKey(t))
	if err != nil {
		t.Fatal(err)
	}
	wrong := idOf(t, newDeviceKey(t))
	r := daemon.NewBridgeRemote(cert, wrong, []string{b.addr})
	t.Cleanup(r.Stop)
	// Bounded: a Pair let through would wait for a confirmation.
	ctx, cancel := context.WithTimeout(t.Context(), settle)
	defer cancel()
	if _, err := r.Pair(ctx, p.ticket.Token, "x", func(string) { t.Error("digits from a bridge with another key") }); err == nil || !strings.Contains(err.Error(), "another key") {
		t.Fatalf("Pair pinning another key: %v, want refused for another key", err)
	}
	select {
	case d := <-p.pending:
		t.Fatalf("the bridge holds %+v pending", d)
	case <-time.After(100 * time.Millisecond):
	}
}
