package daemon_test

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	pc "github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/mesh"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

func newDeviceKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func idOf(t *testing.T, key *ecdsa.PrivateKey) devicekey.ID {
	t.Helper()
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// identityOf is kithd's identity from key, as kithd makes it at start.
func identityOf(t *testing.T, key *ecdsa.PrivateKey) func() (tls.Certificate, error) {
	t.Helper()
	cert, err := devicekey.Certificate(key)
	if err != nil {
		t.Fatal(err)
	}
	return func() (tls.Certificate, error) { return cert, nil }
}

// freeAddr is a loopback address nothing listens on now.
func freeAddr(t *testing.T, ip string) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", ip+":0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// loopbacks hands each caller of loopback a loopback address of its own.
var loopbacks struct {
	sync.Mutex
	next int
}

// loopback is a loopback address no other test dials from, so none meets another's
// throttle; it skips where only 127.0.0.1 is a loopback address.
func loopback(t *testing.T) string {
	t.Helper()
	loopbacks.Lock()
	loopbacks.next++
	n := loopbacks.next
	loopbacks.Unlock()
	ip := fmt.Sprintf("127.0.%d.%d", 1+n/250, 1+n%250)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", ip+":0")
	if err != nil {
		t.Skipf("no loopback address %s here: %v", ip, err)
	}
	_ = ln.Close()
	return ip
}

// storeKey is a mesh store's key, made afresh: never a literal.
func storeKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, sealeddb.KeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

// meshAt is the mesh store at path under key as kithd keeps it: opened when first asked
// for, again after a failure, and closed when the test ends.
func meshAt(t *testing.T, path string, key []byte) func() (daemon.RosterStore, error) {
	t.Helper()
	var mu sync.Mutex
	var store *mesh.Store
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if store != nil {
			_ = store.Close()
		}
	})
	return func() (daemon.RosterStore, error) {
		mu.Lock()
		defer mu.Unlock()
		if store == nil {
			opened, err := mesh.Open(context.Background(), path, key)
			if err != nil {
				return nil, err
			}
			store = opened
		}
		return store, nil
	}
}

// upSettle bounds a bridge coming up: it opens its sealed mesh store first, whose syncs
// to disk wait their turn when every package's tests run at once.
const upSettle = 30 * time.Second

// bridge is a daemon serving its socket, and its listener to paired devices.
type bridge struct {
	t       *testing.T
	socket  *daemon.Remote
	addr    string
	roster  string
	mesh    string // the mesh store's path
	meshKey []byte
	key     *ecdsa.PrivateKey
	self    devicekey.ID
	streams *daemon.Streams
	paired  *daemon.Devices
	logs    *syncBuffer
}

// bridgeOpts shapes a bridge: a roster written before it starts, its identity and key,
// and the directory and key of its mesh store, when the test prepares it.
type bridgeOpts struct {
	roster   string
	identity func() (tls.Certificate, error)
	key      *ecdsa.PrivateKey
	listen   []string
	dir      string
	meshKey  []byte
	// backend serves the bridge's rooms; a fixed list of one when nil.
	backend api.Backend
}

func serveBridge(t *testing.T, opts bridgeOpts) *bridge {
	t.Helper()
	dir := cmp.Or(opts.dir, t.TempDir())
	b := &bridge{
		t: t, roster: filepath.Join(dir, "devices.toml"), mesh: filepath.Join(dir, "mesh.db"),
		meshKey: opts.meshKey, key: opts.key, logs: &syncBuffer{},
	}
	if opts.roster != "" {
		if err := os.WriteFile(b.roster, []byte(opts.roster), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if b.key == nil {
		b.key = newDeviceKey(t)
	}
	if b.meshKey == nil {
		b.meshKey = storeKey(t)
	}
	b.self = idOf(t, b.key)
	if opts.identity == nil {
		opts.identity = identityOf(t, b.key)
	}
	store := meshAt(t, b.mesh, b.meshKey)
	b.addr = freeAddr(t, "127.0.0.1")
	if opts.listen == nil {
		opts.listen = []string{b.addr}
	}
	log := slog.New(slog.NewTextHandler(b.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b.paired = daemon.NewDevices(b.roster, store, opts.identity, log)
	b.paired.Listen(opts.listen)
	b.streams = daemon.NewStreams()
	path := filepath.Join(dir, "kithd.sock")
	ln, err := daemon.Listen(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	backend := opts.backend
	if backend == nil {
		backend = &fakeBackend{Nop: nopWithChannels(), rooms: []domain.Room{{ID: "matrix:0123456789abcdef/!a:x", Name: "A"}}}
	}
	d := complete(t, &daemon.Daemon{Backend: backend, Streams: b.streams, Paired: b.paired, Log: log})
	go func() { served <- daemon.Serve(ctx, ln, d) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("Serve() = %v", err)
		}
	})
	b.socket = daemon.NewRemote(path)
	b.waitStatus()
	return b
}

// waitStatus waits for the socket to answer.
func (b *bridge) waitStatus() {
	b.t.Helper()
	deadline := time.Now().Add(settle)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _, _, err := b.socket.Status(ctx)
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("the socket never answered: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitLog waits for a log line saying every one of says.
func (b *bridge) waitLog(says ...string) {
	b.t.Helper()
	deadline := time.Now().Add(upSettle) // what a bridge says as it comes up, too
	for {
		logs := b.logs.String()
		if !slices.ContainsFunc(says, func(s string) bool { return !strings.Contains(logs, s) }) {
			return
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("the log does not say %q:\n%s", says, logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// listening reports whether something takes TCP connections on addr.
func listening(addr string) bool {
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// waitListening waits for addr to take connections, or to stop, as want says.
func (b *bridge) waitListening(addr string, want bool) {
	b.t.Helper()
	wait := settle
	if want {
		wait = upSettle
	}
	deadline := time.Now().Add(wait)
	for listening(addr) != want {
		if time.Now().After(deadline) {
			b.t.Fatalf("listening on %s = %v, want %v; log:\n%s", addr, !want, want, b.logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *bridge) pair(key *ecdsa.PrivateKey, name string) {
	b.t.Helper()
	if _, err := b.socket.AddDevice(b.t.Context(), idOf(b.t, key).String(), name); err != nil {
		b.t.Fatalf("AddDevice: %v", err)
	}
}

func (b *bridge) unpair(key *ecdsa.PrivateKey) {
	b.t.Helper()
	if err := b.socket.RemoveDevice(b.t.Context(), idOf(b.t, key).String()); err != nil {
		b.t.Fatalf("RemoveDevice: %v", err)
	}
}

// device is how a test client reaches the bridge: its key (none: no certificate), the
// local address it dials from, and the TLS it speaks.
type device struct {
	key        *ecdsa.PrivateKey
	from       string
	maxVersion uint16
	cache      tls.ClientSessionCache
	keepAlive  bool
}

// transport dials b over TLS as d, checking the bridge's key as a client pins it.
func (b *bridge) transport(d device) *http.Transport {
	b.t.Helper()
	conf := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         d.maxVersion,
		InsecureSkipVerify: true, // the key is pinned below, not a CA's word
		ClientSessionCache: d.cache,
		VerifyConnection: func(state tls.ConnectionState) error {
			if id, err := devicekey.PeerID(state); err != nil || id != b.self {
				return fmt.Errorf("the bridge's key is %v (%w), want %v", id, err, b.self)
			}
			return nil
		},
	}
	if d.key != nil {
		cert, err := devicekey.Certificate(d.key)
		if err != nil {
			b.t.Fatal(err)
		}
		conf.Certificates = []tls.Certificate{cert}
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	if d.from != "" {
		dialer.LocalAddr = &net.TCPAddr{IP: net.ParseIP(d.from)}
	}
	return &http.Transport{
		TLSClientConfig: conf, ForceAttemptHTTP2: true, DialContext: dialer.DialContext,
		DisableKeepAlives: !d.keepAlive, ResponseHeaderTimeout: 30 * time.Second, // a loaded machine's fsyncs
	}
}

func (b *bridge) client(d device) backendv1connect.BackendServiceClient {
	b.t.Helper()
	return backendv1connect.NewBackendServiceClient(&http.Client{Transport: b.transport(d)}, "https://"+b.addr)
}

// status is one call over TLS as d.
func (b *bridge) status(d device) error {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(b.t.Context(), 5*time.Second)
	defer cancel()
	_, err := b.client(d).Status(ctx, connect.NewRequest(&v1.StatusRequest{}))
	return err
}

func TestAPairedDeviceIsServedWhatTheSocketServes(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	key := newDeviceKey(t)
	b.pair(key, "laptop")

	c := b.client(device{key: key})
	ctx := t.Context()
	overTLS, err := c.Rooms(ctx, connect.NewRequest(&v1.RoomsRequest{}))
	if err != nil {
		t.Fatalf("Rooms over TLS: %v", err)
	}
	overSocket, err := b.socket.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := overTLS.Msg.GetRooms(); len(got) != len(overSocket) || got[0].GetId() != string(overSocket[0].ID) {
		t.Fatalf("Rooms over TLS = %v, over the socket %v", got, overSocket)
	}
	// The profile gate stands on this path too.
	refused := connect.NewRequest(&v1.RoomsRequest{})
	refused.Header().Set("Kith-Profile", "no such profile")
	if _, err = c.Rooms(ctx, refused); err == nil {
		t.Fatal("an unknown profile was served over TLS")
	}

	// Over the socket a device is paired by the bridge itself; over TLS by the device.
	self, devices, err := b.socket.Devices(ctx)
	if err != nil || self != b.self.String() || len(devices) != 1 || devices[0].AddedBy != self || devices[0].Name != "laptop" {
		t.Fatalf("Devices = %q, %+v, %v; want laptop added by %s", self, devices, err, b.self)
	}
	other := newDeviceKey(t)
	added, err := c.AddDevice(ctx, connect.NewRequest(&v1.AddDeviceRequest{Id: idOf(t, other).String(), Name: "phone"}))
	if err != nil {
		t.Fatal(err)
	}
	if by := added.Msg.GetDevice().GetAddedBy(); by != idOf(t, key).String() {
		t.Fatalf("a device paired over TLS was added by %q, want the laptop %s", by, idOf(t, key))
	}

	// TLS 1.3, and HTTP/2 offered.
	conn, err := (&tls.Dialer{Config: b.transport(device{key: key}).TLSClientConfig.Clone()}).DialContext(ctx, "tcp", b.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if st := conn.(*tls.Conn).ConnectionState(); st.Version != tls.VersionTLS13 {
		t.Fatalf("TLS version %x, want 1.3", st.Version)
	}
	h2 := b.transport(device{key: key}).TLSClientConfig.Clone()
	h2.NextProtos = []string{"h2"}
	conn2, err := (&tls.Dialer{Config: h2}).DialContext(ctx, "tcp", b.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn2.Close() }()
	if p := conn2.(*tls.Conn).ConnectionState().NegotiatedProtocol; p != "h2" {
		t.Fatalf("negotiated %q, want h2", p)
	}
}

// Every way a device can come to the listener, and whether it is served: only a key
// the roster holds now, over TLS 1.3, with a certificate.
func TestOnlyAPairedKeyIsServed(t *testing.T) {
	t.Parallel()
	byHand := newDeviceKey(t)
	roster := fmt.Sprintf("[[device]]\nname = \"by hand\"\nid = %q\n", strings.ToLower(idOf(t, byHand).String()))
	b := serveBridge(t, bridgeOpts{roster: roster})
	b.waitListening(b.addr, true)
	paired, unpaired, removed := newDeviceKey(t), newDeviceKey(t), newDeviceKey(t)
	b.pair(paired, "paired")
	b.pair(removed, "removed")
	if err := b.status(device{key: removed}); err != nil {
		t.Fatalf("before removal: %v", err)
	}
	b.unpair(removed)

	for _, tc := range []struct {
		name   string
		device device
		served bool
	}{
		{"paired", device{key: paired}, true},
		{"paired by editing the roster", device{key: byHand}, true},
		{"unpaired", device{key: unpaired}, false},
		{"removed", device{key: removed}, false},
		{"no certificate", device{}, false},
		{"paired, over TLS 1.2", device{key: paired, maxVersion: tls.VersionTLS12}, false},
	} {
		// Each from an address of its own: the throttle is another test's.
		tc.device.from = loopback(t)
		if err := b.status(tc.device); (err == nil) != tc.served {
			t.Errorf("%s: Status = %v, want served %v", tc.name, err, tc.served)
		}
	}
	// The bridge's own key is no client's.
	if _, err := b.socket.AddDevice(t.Context(), b.self.String(), "me"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("pairing the bridge's own key: %v, want invalid_argument", err)
	}
}

// A session ticket would let a device resume after it was unpaired: none is issued,
// and a resumption is checked as a handshake is.
func TestNoSessionOutlivesAnUnpairing(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	key := newDeviceKey(t)
	b.pair(key, "laptop")
	cache := tls.NewLRUClientSessionCache(4)
	tr := b.transport(device{key: key, cache: cache, keepAlive: true})
	post := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			"https://"+b.addr+backendv1connect.BackendServiceStatusProcedure, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		return tr.RoundTrip(req)
	}
	for i := range 2 {
		resp, err := post()
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.TLS.DidResume {
			t.Fatalf("call %d: status %d, resumed %v; want 200 on a full handshake", i, resp.StatusCode, resp.TLS.DidResume)
		}
		tr.CloseIdleConnections()
	}
	b.unpair(key)
	if resp, err := post(); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("after unpairing: status %d, resumed %v; want refused", resp.StatusCode, resp.TLS.DidResume)
	}
}

// Unpairing cuts that device's live streams at once, and only its own.
func TestUnpairingCutsTheDevicesLiveStreams(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	gone, stays := newDeviceKey(t), newDeviceKey(t)
	b.pair(gone, "gone")
	b.pair(stays, "stays")

	goneFrames, staysFrames := b.changes(gone), b.changes(stays)
	b.await(goneFrames, domain.ChangeRooms, b.streams.RoomsChanged)
	b.await(staysFrames, domain.ChangeRooms, b.streams.RoomsChanged)

	b.unpair(gone)
	b.awaitEnd(goneFrames)
	if err := b.status(device{key: gone}); err == nil {
		t.Fatal("the unpaired device reconnected")
	}
	// A kind of change sent only now: the other device's stream outlived the cut.
	b.await(staysFrames, domain.ChangeAccounts, b.streams.AccountsChanged)
}

// changes opens the change stream as key's device, and hands on its frames until it
// ends.
func (b *bridge) changes(key *ecdsa.PrivateKey) <-chan domain.Change {
	b.t.Helper()
	st, err := b.client(device{key: key, keepAlive: true}).Changed(b.t.Context(), connect.NewRequest(&v1.ChangedRequest{}))
	if err != nil {
		b.t.Fatal(err)
	}
	frames := make(chan domain.Change, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(frames)
		for st.Receive() {
			select {
			case frames <- pc.ProtoToChange(st.Msg().GetChange()):
			default: // the test reads only what it waits for
			}
		}
	}()
	b.t.Cleanup(func() { _ = st.Close(); <-done })
	return frames
}

// await sends until frames carries want; the stream subscribes when it pleases.
func (b *bridge) await(frames <-chan domain.Change, want domain.Change, send func()) {
	b.t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(settle)
	for {
		select {
		case got, ok := <-frames:
			if !ok {
				b.t.Fatalf("the stream ended waiting for change %v", want)
			}
			if got == want {
				return
			}
		case <-tick.C:
			send()
		case <-deadline:
			b.t.Fatalf("no change %v on the stream", want)
		}
	}
}

// awaitEnd waits for the stream to end, past what it already had.
func (b *bridge) awaitEnd(frames <-chan domain.Change) {
	b.t.Helper()
	deadline := time.After(settle)
	for {
		select {
		case _, ok := <-frames:
			if !ok {
				return
			}
		case <-deadline:
			b.t.Fatal("the stream was not cut")
		}
	}
}

// The roster as a hand might leave it: each that does not read keeps every listener
// down, and the socket up.
func TestARosterThatDoesNotReadListensNowhere(t *testing.T) {
	t.Parallel()
	id := idOf(t, newDeviceKey(t)).String()
	for _, tc := range []struct{ name, roster string }{
		{"not TOML", "[[device]\nid = "},
		{"an unknown key", fmt.Sprintf("[[device]]\nid = %q\nadmin = true\n", id)},
		{"an unknown role", fmt.Sprintf("[[device]]\nid = %q\nrole = \"admin\"\n", id)},
		{"an ID that does not read", "[[device]]\nid = \"ABCD-EFGH\"\n"},
		{"no ID", "[[device]]\nname = \"laptop\"\n"},
		{"a device twice", fmt.Sprintf("[[device]]\nid = %q\n[[device]]\nid = %q\n", id, strings.ToLower(id))},
		{"a name twice", fmt.Sprintf("[[device]]\nname = \"a\"\nid = %q\n[[device]]\nname = \"a\"\nid = %q\n",
			id, idOf(t, newDeviceKey(t)).String())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := serveBridge(t, bridgeOpts{roster: tc.roster})
			b.waitLog("not listening for paired devices", b.roster)
			if listening(b.addr) {
				t.Error("listening with a roster that does not read")
			}
			if _, err := b.socket.AddDevice(t.Context(), idOf(t, newDeviceKey(t)).String(), ""); err == nil {
				t.Error("a device was added over a roster that does not read")
			}
			if body, _ := os.ReadFile(b.roster); string(body) != tc.roster {
				t.Errorf("the roster was rewritten:\n%s", body)
			}
		})
	}
}

// Whatever keeps the listener down, the socket is served and the log says why.
func TestTheSocketOutlivesEveryListenerFailure(t *testing.T) {
	t.Parallel()
	taken, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taken.Close() })
	for _, tc := range []struct {
		name string
		opts bridgeOpts
		says string
	}{
		{"a corrupt roster", bridgeOpts{roster: "garbage"}, "not listening"},
		{"no device key", bridgeOpts{identity: func() (tls.Certificate, error) {
			return tls.Certificate{}, errors.New("the keyring is locked")
		}}, "the keyring is locked"},
		{"an address taken", bridgeOpts{listen: []string{taken.Addr().String()}}, "cannot listen"},
		{"an address not here", bridgeOpts{listen: []string{"192.0.2.1:7443"}}, "cannot listen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := serveBridge(t, tc.opts) // waits for the socket to answer
			b.waitLog(tc.says)
		})
	}
}

// A reload names the addresses from then on: one dropped stops, one added starts, and
// none listens nowhere.
func TestAReloadMovesTheListener(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	key := newDeviceKey(t)
	b.pair(key, "laptop")
	next := freeAddr(t, "127.0.0.1")
	b.paired.Listen([]string{next})
	b.waitListening(b.addr, false)
	b.waitListening(next, true)
	b.addr = next
	if err := b.status(device{key: key}); err != nil {
		t.Fatalf("on the new address: %v", err)
	}
	b.paired.Listen(nil)
	b.waitListening(next, false)
}

// AddDevice and RemoveDevice refuse what they cannot do, each with its code, and
// change nothing.
func TestARefusedChangeLeavesTheRoster(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	key := newDeviceKey(t)
	b.pair(key, "laptop")
	before, err := os.ReadFile(b.roster)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		err  error
		code connect.Code
	}{
		{"not an ID", func() error { _, err := b.socket.AddDevice(ctx, "laptop", ""); return err }(), connect.CodeInvalidArgument},
		{"its own ID", func() error { _, err := b.socket.AddDevice(ctx, b.self.String(), ""); return err }(), connect.CodeInvalidArgument},
		{"paired already", func() error {
			_, err := b.socket.AddDevice(ctx, strings.ToLower(idOf(t, key).String()), "again")
			return err
		}(), connect.CodeAlreadyExists},
		{"a name taken", func() error {
			_, err := b.socket.AddDevice(ctx, idOf(t, newDeviceKey(t)).String(), "laptop")
			return err
		}(), connect.CodeAlreadyExists},
		{"removing one not paired", b.socket.RemoveDevice(ctx, idOf(t, newDeviceKey(t)).String()), connect.CodeNotFound},
		{"removing not an ID", b.socket.RemoveDevice(ctx, "laptop"), connect.CodeInvalidArgument},
	} {
		if connect.CodeOf(tc.err) != tc.code {
			t.Errorf("%s: %v, want %v", tc.name, tc.err, tc.code)
		}
	}
	if after, _ := os.ReadFile(b.roster); !bytes.Equal(after, before) {
		t.Fatalf("a refused change wrote the roster:\n%s", after)
	}
	if info, err := os.Stat(b.roster); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the roster's mode: %v, %v; want 0600", info, err)
	}
}

// Changes at once, over the socket and from a paired device, leave the roster holding
// exactly what was added and not removed, in the file as in what is served.
func TestChangesAtOnceKeepTheRosterWhole(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	via := newDeviceKey(t)
	b.pair(via, "via")
	overTLS := b.client(device{key: via, keepAlive: true})
	// One connection first, which HTTP/2 then carries every call on: calls racing to
	// a new connection each dial, and would meet the throttle.
	if _, err := overTLS.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); err != nil {
		t.Fatal(err)
	}
	const n = 12
	var early, late []*ecdsa.PrivateKey
	for range n {
		k := newDeviceKey(t)
		b.pair(k, "")
		early = append(early, k)
		late = append(late, newDeviceKey(t))
	}
	ctx := t.Context()
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := range n {
		gone, added, name := idOf(t, early[i]).String(), idOf(t, late[i]).String(), fmt.Sprintf("late %d", i)
		wg.Go(func() {
			if i%2 == 0 {
				_, err := overTLS.RemoveDevice(ctx, connect.NewRequest(&v1.RemoveDeviceRequest{Id: gone}))
				errs <- err
				return
			}
			errs <- b.socket.RemoveDevice(ctx, gone)
		})
		wg.Go(func() {
			if i%2 == 1 {
				_, err := overTLS.AddDevice(ctx, connect.NewRequest(&v1.AddDeviceRequest{Id: added, Name: name}))
				errs <- err
				return
			}
			_, err := b.socket.AddDevice(ctx, added, name)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	want := []string{idOf(t, via).String()}
	for _, k := range late {
		want = append(want, idOf(t, k).String())
	}
	slices.Sort(want)
	// What is served, and the file read afresh.
	_, served, err := b.socket.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, inFile, err := daemon.NewDevices(b.roster, meshAt(t, b.mesh, b.meshKey), identityOf(t, b.key), logging.Discard()).List()
	if err != nil {
		t.Fatal(err)
	}
	for name, list := range map[string][]domain.Device{"served": served, "in the file": inFile} {
		var got []string
		for _, d := range list {
			got = append(got, d.ID)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: %d devices, want via and the %d added late:\n%v\n%v", name, len(got), n, got, want)
		}
	}
}

// A flood of handshakes from one address is cut to its bucket before TLS, and another
// address is let in all the while.
func TestAFloodFromOneAddressLeavesOthersIn(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	key := newDeviceKey(t)
	b.pair(key, "laptop")
	flood := device{key: key, from: loopback(t)}
	start := time.Now()
	const tries = 3 * daemon.HandshakeBurst
	served := 0
	for range tries {
		if b.status(flood) == nil {
			served++
		}
	}
	// A token comes back every two seconds; a slow machine may earn a few.
	refilled := int(time.Since(start)/(2*time.Second)) + 1
	if served < daemon.HandshakeBurst || served > daemon.HandshakeBurst+refilled {
		t.Fatalf("%d of %d handshakes from one address served, want the burst of %d (+%d refilled)",
			served, tries, daemon.HandshakeBurst, refilled)
	}
	if err := b.status(device{key: key, from: loopback(t)}); err != nil {
		t.Fatalf("another address during the flood: %v", err)
	}
}

// Handshakes that stall hold at most the cap of slots; once one ends, the next
// device is let in.
func TestStalledHandshakesHoldTheGateOnlyWhileTheyLast(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	key := newDeviceKey(t)
	b.pair(key, "laptop")
	// Silent connections, each within its address's bucket, filling every slot.
	var stalled []net.Conn
	var from string
	for i := range daemon.MaxHandshakes {
		if i%(daemon.HandshakeBurst-1) == 0 {
			from = loopback(t) // a fresh address per few connections
		}
		dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(from)}}
		c, err := dialer.DialContext(t.Context(), "tcp", b.addr)
		if err != nil {
			t.Fatal(err)
		}
		stalled = append(stalled, c)
	}
	t.Cleanup(func() {
		for _, c := range stalled {
			_ = c.Close()
		}
	})
	next := device{key: key, from: loopback(t)}
	tr := b.transport(next)
	tr.ResponseHeaderTimeout = 0
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	c := backendv1connect.NewBackendServiceClient(&http.Client{Transport: tr}, "https://"+b.addr)
	if _, err := c.Status(ctx, connect.NewRequest(&v1.StatusRequest{})); err == nil {
		t.Fatal("a handshake ran past the cap")
	}
	_ = stalled[0].Close()
	if err := b.status(next); err != nil {
		t.Fatalf("after a stalled handshake ended: %v", err)
	}
}

// The roster file is the truth: a hand edit while kithd runs is kept by the next
// change, whichever it is, and a device edited out is cut off by it.
func TestAHandEditIsNeitherLostNorIgnored(t *testing.T) {
	t.Parallel()
	b := serveBridge(t, bridgeOpts{})
	b.waitListening(b.addr, true)
	out, spare, byHand, later := newDeviceKey(t), newDeviceKey(t), newDeviceKey(t), newDeviceKey(t)
	b.pair(out, "out")
	b.pair(spare, "spare")
	outFrames := b.changes(out)
	b.await(outFrames, domain.ChangeRooms, b.streams.RoomsChanged)

	// Out goes, by hand comes in.
	edited := fmt.Sprintf("[[device]]\nname = \"spare\"\nid = %q\n\n[[device]]\nname = \"by hand\"\nid = %q\n",
		idOf(t, spare).String(), idOf(t, byHand).String())
	if err := os.WriteFile(b.roster, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		t.Helper()
		_, devices, err := b.socket.Devices(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range devices {
			out = append(out, d.Name)
		}
		return out
	}
	b.unpair(spare)
	if got := names(); !slices.Equal(got, []string{"by hand"}) {
		t.Fatalf("after the edit and an unpairing: %q, want by hand", got)
	}
	b.awaitEnd(outFrames)
	b.pair(later, "later")
	if got := names(); !slices.Equal(got, []string{"by hand", "later"}) {
		t.Fatalf("after a pairing: %q, want by hand and later", got)
	}
	from := loopback(t)
	if err := b.status(device{key: byHand, from: from}); err != nil {
		t.Errorf("the device paired by hand: %v", err)
	}
	if err := b.status(device{key: out, from: from}); err == nil {
		t.Error("the device edited out was served")
	}
}
