package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/zalando/go-keyring"
	"google.golang.org/protobuf/proto"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/keystore"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/mesh"
	"github.com/EugeneShtoka/kith/internal/schedule"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// TestMain keeps the relay's device key off the system keyring: an in-memory one
// stands in.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
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

func certOf(t *testing.T, key *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	cert, err := devicekey.Certificate(key)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// freeAddr is a loopback address nothing listens on now.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// messageLog is a cache's numbered changes, which kithd's Messages stream reads.
type messageLog struct {
	mu      sync.Mutex
	changes []domain.MessageChange
	streams *daemon.Streams
}

func (l *messageLog) LastChange(context.Context) (string, int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return "one", int64(len(l.changes)), nil
}

func (l *messageLog) ChangesAfter(_ context.Context, after int64, limit int) (string, []domain.MessageChange, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	end := min(int64(len(l.changes)), after+int64(limit))
	return "one", slices.Clone(l.changes[after:end]), nil
}

// says commits one message for each of bodies, in order, and returns them.
func (l *messageLog) says(prefix string, n int) []string {
	var bodies []string
	l.mu.Lock()
	for i := range n {
		body := fmt.Sprintf("%s%d", prefix, i)
		bodies = append(bodies, body)
		number := int64(len(l.changes)) + 1
		l.changes = append(l.changes, domain.MessageChange{Number: number, Message: domain.Message{
			ID: domain.EventID(fmt.Sprintf("matrix:0123456789abcdef/$%d", number)), RoomID: "matrix:0123456789abcdef/!r:x",
			Sender: "matrix:@alice:x", Body: body, Timestamp: time.Now(),
		}})
	}
	l.mu.Unlock()
	l.streams.MessagesChanged()
	return bodies
}

// world is a backend knowing no rooms; a room is judged by its ID.
type world struct{ apitest.Nop }

func (world) ThreadParticipant(context.Context, domain.RoomID, domain.EventID) bool { return false }

// bridgeKithd is a kithd elsewhere, serving the devices its roster pairs over TLS at
// addr, with its own key.
type bridgeKithd struct {
	addr     string
	id       devicekey.ID
	paired   *daemon.Devices
	messages *messageLog
	// stop is kithd exiting; idempotent.
	stop func()
}

// serveBridge serves a kithd whose config has the profiles Home and Work, to the
// devices whose keys are paired, until the test ends.
func serveBridge(t *testing.T, paired ...*ecdsa.PrivateKey) *bridgeKithd {
	t.Helper()
	dir := t.TempDir()
	var roster strings.Builder
	for i, key := range paired {
		fmt.Fprintf(&roster, "[[device]]\nname = \"device%d\"\nid = %q\n", i, idOf(t, key).String())
	}
	if err := os.WriteFile(filepath.Join(dir, "devices.toml"), []byte(roster.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Profiles: []config.Profile{{Name: "Home", Include: []string{"*"}}, {Name: "Work", Include: []string{"*"}}}}
	cfgPath := filepath.Join(dir, "config.toml")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	key := newKey(t)
	cert := certOf(t, key)
	backend := world{}
	n, err := daemon.NewNotifications(cfg, backend)
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := daemon.NewUploads(filepath.Join(dir, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := daemon.Listen(t.Context(), filepath.Join(dir, "kithd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	b := &bridgeKithd{addr: freeAddr(t), id: idOf(t, key), messages: &messageLog{streams: daemon.NewStreams()}}
	b.paired = daemon.NewDevices(filepath.Join(dir, "devices.toml"), meshAt(t, filepath.Join(dir, "mesh.db")),
		func() (tls.Certificate, error) { return cert, nil }, logging.Discard())
	b.paired.Listen([]string{b.addr})
	d := &daemon.Daemon{
		Backend: backend, Streams: b.messages.streams, MessageLog: b.messages, State: daemon.NewState(), Notifications: n,
		Reload:      func(context.Context) error { return nil },
		CheckConfig: func(context.Context, config.Config) error { return nil },
		Config:      daemon.NewConfigFile(cfgPath),
		Scheduler: daemon.NewScheduler(schedule.New(filepath.Join(dir, "scheduled.toml")), backend,
			func() time.Duration { return time.Hour }, nil),
		Logins:  daemon.NewLogins(t.Context(), func() []api.LoginLeader { return nil }),
		Uploads: uploads,
		Log:     logging.Discard(),
		Paired:  b.paired,
	}
	d.State.Synced(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { defer close(served); _ = daemon.Serve(ctx, ln, d) }()
	b.stop = func() { cancel(); <-served }
	t.Cleanup(b.stop)
	within(t, 5*time.Second, "the bridge listening", func() bool {
		for {
			c, derr := (&net.Dialer{}).DialContext(t.Context(), "tcp", b.addr)
			if derr == nil {
				_ = c.Close()
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	return b
}

// meshAt is the bridge's mesh store at path, as kithd keeps it: opened when first asked
// for, under a key of the test's own, and closed when the test ends.
func meshAt(t *testing.T, path string) func() (daemon.RosterStore, error) {
	t.Helper()
	key := make([]byte, sealeddb.KeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
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

// unpair removes the device whose key is key from the bridge's roster, as `kith devices
// remove` there does: its live connections are cut.
func (b *bridgeKithd) unpair(t *testing.T, key *ecdsa.PrivateKey) {
	t.Helper()
	if err := b.paired.Remove(idOf(t, key).String()); err != nil {
		t.Fatal(err)
	}
}

// pair adds the device whose key is key to the bridge's roster, as a pairing confirmed
// there does.
func (b *bridgeKithd) pair(t *testing.T, key *ecdsa.PrivateKey) {
	t.Helper()
	if _, err := b.paired.Add(idOf(t, key).String(), "laptop", nil); err != nil {
		t.Fatal(err)
	}
}

// panics fails the test on a panic the relay's server logs: it survives one in a
// handler, but the window's connection does not.
type panics struct{ t *testing.T }

func (p panics) Write(line []byte) (int, error) {
	if bytes.Contains(line, []byte("panic")) {
		p.t.Errorf("the relay's server: %s", line)
	}
	return len(line), nil
}

// relayTo serves the relay's handler for kithd at on a socket of its own, with the
// device file beside configPath.
func relayTo(t *testing.T, at kithdAt, configPath string) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "relay.sock")
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:           newHandler(at, configPath, &commandRuns{}, slog.New(slog.DiscardHandler)),
		ReadHeaderTimeout: time.Second, ErrorLog: log.New(panics{t}, "", 0),
	}
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); <-served })
	return socket
}

// viaBridge is a relay reaching the bridge named home whose key's ID is pin at addrs,
// as the device whose key is key; its device file is the defaults.
func viaBridge(t *testing.T, key *ecdsa.PrivateKey, pin devicekey.ID, addrs ...string) string {
	t.Helper()
	at, err := bridged(certOf(t, key), config.Bridge{Name: "home", ID: pin.String(), Addresses: addrs})
	if err != nil {
		t.Fatal(err)
	}
	return relayTo(t, at, filepath.Join(t.TempDir(), "config.toml"))
}

// clientDevice is a machine that runs no kithd: a config holding only where it keeps its
// own files (no profile, no account), and its device key, as every client here makes it.
// It returns the config's path and the key.
func clientDevice(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.Save(path, config.Config{Storage: config.Storage{
		Instance: "laptop", DataDir: filepath.Join(dir, "data"), StateDir: filepath.Join(dir, "state"),
		CacheDir: filepath.Join(dir, "cache"), RuntimeDir: filepath.Join(dir, "run"),
	}}); err != nil {
		t.Fatal(err)
	}
	storage, allowFile, err := setup.ClientStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := keystore.LoadOrCreateDeviceKey(keystore.DeviceStore(storage, "client-device"), allowFile)
	if err != nil {
		t.Fatal(err)
	}
	return path, key
}

// joins writes the device file beside path as `kith join` leaves it, naming the bridge
// b, and the profile this device runs in.
func joins(t *testing.T, path string, b *bridgeKithd, profile string) {
	t.Helper()
	handWrite(t, path, fmt.Sprintf("profile_in_use = %q\n\n[[bridge]]\nname = \"home\"\nid = %q\naddresses = [%q]\n",
		profile, b.id.String(), b.addr))
}

// On a device that joined a bridge, the relay needs no kithd and no config of its own:
// the window's calls reach the bridge, the profiles are the bridge's config's, the one in
// use is this device's, and the window is told where it is connected. Its socket is
// owner-only as ever, and nothing it tells the window holds this device's key.
func TestARelayOnAJoinedDeviceReachesTheBridge(t *testing.T) {
	t.Parallel()
	path, laptop := clientDevice(t)
	b := serveBridge(t, laptop)
	joins(t, path, b, "work")
	w := openWindow(t, path)

	dir := filepath.Dir(w.socket)
	for p, want := range map[string]os.FileMode{dir: 0o700, w.socket: 0o600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s is %o, want %o", p, got, want)
		}
	}
	got, err := deviceOf(t, w.socket)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetBridge() != "home" || got.GetSocket() != "" || got.GetProfile() != "Work" ||
		strings.Join(got.GetProfiles(), ",") != "Home,Work" {
		t.Errorf("Device = %v, want bridge home, no socket, the bridge's profiles Home,Work and Work in use", got)
	}
	status, err := backendVia(w.socket).Status(t.Context(), connect.NewRequest(&v1.StatusRequest{}))
	if err != nil || !status.Msg.GetReady() {
		t.Errorf("Status through the relay = %v, %v; want the bridge's, ready", status, err)
	}

	settings, err := deviceClient(w.socket).DeviceSettings(t.Context(), connect.NewRequest(&devicev1.DeviceSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(laptop)
	if err != nil {
		t.Fatal(err)
	}
	pem, err := devicekey.Marshal(laptop)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := laptop.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []proto.Message{got, settings.Msg} {
		wire, merr := proto.Marshal(answer)
		if merr != nil {
			t.Fatal(merr)
		}
		for name, secret := range map[string][]byte{
			"its ID": []byte(idOf(t, laptop).String()), "its key": der, "its key, kept": pem, "its scalar": raw,
		} {
			if bytes.Contains(wire, secret) {
				t.Errorf("%T tells the window this device's key (%s)", answer, name)
			}
		}
	}
	w.closed(t)
}

// A bridge's addresses are tried in order, and one answering with another key is passed
// over, never trusted; with only such an address, the window's call is refused as
// unavailable, naming the key that answered.
func TestABridgeAddressAnsweringWithAnotherKeyIsPassedOver(t *testing.T) {
	t.Parallel()
	laptop := newKey(t)
	home, other := serveBridge(t, laptop), serveBridge(t, laptop) // other: paired, but not the bridge
	nobody := freeAddr(t)

	relay := viaBridge(t, laptop, home.id, nobody, other.addr, home.addr)
	if _, err := backendVia(relay).Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); err != nil {
		t.Errorf("Status past an address nobody answers and one with another key = %v, want the bridge's", err)
	}
	if got, err := deviceOf(t, relay); err != nil || strings.Join(got.GetProfiles(), ",") != "Home,Work" {
		t.Errorf("Device = %v, %v; want the bridge's profiles", got, err)
	}

	alone := viaBridge(t, laptop, home.id, nobody, other.addr)
	_, err := backendVia(alone).Status(t.Context(), connect.NewRequest(&v1.StatusRequest{}))
	if connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), other.id.String()) {
		t.Errorf("Status with only another key answering = %v, want unavailable naming %s", err, other.id)
	}
	if _, err := deviceOf(t, alone); connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), other.id.String()) {
		t.Errorf("Device with only another key answering = %v, want unavailable naming %s", err, other.id)
	}
}

// A device unpaired on the bridge while its window streams: the stream ends, and once
// the window tries again it is refused as permission denied, saying the bridge refused
// this device, which the window shows; it is not unavailable, which reads as the bridge
// being away. The refusal comes after this device's handshake is done, so a try can meet
// it reading its answer or writing itself (a reset connection), and past a few the
// bridge drops handshakes before reading the key: once told, every try after, call or
// stream, is told the same.
func TestADeviceUnpairedMidStreamIsToldTheBridgeRefusedIt(t *testing.T) {
	t.Parallel()
	laptop := newKey(t)
	b := serveBridge(t, laptop)
	relay := viaBridge(t, laptop, b.id, b.addr)
	backend := backendVia(relay)
	stream := changed(t, backend)

	b.unpair(t, laptop)
	if within(t, 5*time.Second, "the stream ending with the pairing", stream.Receive) {
		t.Fatalf("a frame %v after the device was unpaired", stream.Msg())
	}
	isRefusal := func(err error) bool {
		return connect.CodeOf(err) == connect.CodePermissionDenied && strings.Contains(err.Error(), "bridge home refused this device")
	}
	within(t, 5*time.Second, "the window told the bridge refused it", func() bool {
		for {
			_, err := backend.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{}))
			if isRefusal(err) {
				return true
			}
			if err == nil {
				t.Error("Status answered after unpairing")
				return false
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	refused := func(what string, err error) {
		t.Helper()
		if !isRefusal(err) {
			t.Errorf("%s after the refusal = %v, want permission denied saying bridge home refused this device", what, err)
		}
	}
	for range 20 {
		_, err := backend.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{}))
		refused("Status", err)
		_, err = deviceOf(t, relay)
		refused("Device", err)
		again, err := backend.Changed(t.Context(), connect.NewRequest(&v1.ChangedRequest{}))
		if err == nil {
			if within(t, 5*time.Second, "the reopened stream's answer", again.Receive) {
				t.Fatalf("a frame %v on a stream reopened after unpairing", again.Msg())
			}
			err = again.Err()
			_ = again.Close()
		}
		refused("Changed", err)
	}

	// Paired again, the device is answered once the bridge takes a handshake again, and
	// the refusal is forgotten: the bridge going away later reads as unavailable.
	b.pair(t, laptop)
	within(t, 15*time.Second, "the bridge answering the device paired again", func() bool {
		for {
			if _, err := deviceOf(t, relay); err == nil {
				return true
			}
			time.Sleep(250 * time.Millisecond)
		}
	})
	b.stop()
	if _, err := backend.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("Status with the bridge gone, once it answered the device paired again = %v, want unavailable", err)
	}
}

// follow reads the Messages stream through backend as the window's main does: reopened
// after any end from the last change it saw, its messages' bodies sent on the channel.
// It returns once the stream has said where it starts.
func follow(t *testing.T, backend backendv1connect.BackendServiceClient) <-chan string {
	t.Helper()
	bodies := make(chan string, 256)
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		epoch, change := "", int64(0)
		for ctx.Err() == nil {
			stream, err := backend.Messages(ctx, connect.NewRequest(&v1.MessagesRequest{SinceEpoch: epoch, SinceChange: change}))
			if err == nil {
				for stream.Receive() {
					m := stream.Msg()
					if m.GetReadAgain() {
						t.Errorf("the stream could not resume from change %d of %q", change, epoch)
					}
					if epoch == "" {
						close(started)
					}
					epoch, change = m.GetEpoch(), m.GetChange()
					if m.GetMessage() != nil {
						bodies <- m.GetMessage().GetBody()
					}
				}
				_ = stream.Close()
			}
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	within(t, 5*time.Second, "the message stream starting", func() bool { <-started; return true })
	return bodies
}

// read fails unless the next bodies are want, in order, within d.
func read(t *testing.T, bodies <-chan string, want []string, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for _, body := range want {
		select {
		case got := <-bodies:
			if got != body {
				t.Fatalf("got %q, want %q", got, body)
			}
		case <-deadline:
			t.Fatalf("no message %q within %s", body, d)
		}
	}
}

// dimmer forwards connections to a bridge, and on dark leaves every connection it has
// forwarded open but silent, as a network a laptop has left does; connections after
// that are forwarded, as over the network it joined.
type dimmer struct {
	ln   net.Listener
	to   string
	gen  atomic.Int64
	wg   sync.WaitGroup
	done chan struct{}
}

func newDimmer(t *testing.T, to string) *dimmer {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &dimmer{ln: ln, to: to, done: make(chan struct{})}
	d.wg.Go(d.accept)
	t.Cleanup(func() {
		close(d.done)
		_ = ln.Close()
		d.wg.Wait()
	})
	return d
}

func (d *dimmer) addr() string { return d.ln.Addr().String() }

// dark silences every connection forwarded so far.
func (d *dimmer) dark() { d.gen.Add(1) }

func (d *dimmer) accept() {
	for {
		in, err := d.ln.Accept()
		if err != nil {
			return
		}
		out, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", d.to)
		if err != nil {
			_ = in.Close()
			continue
		}
		gen := d.gen.Load()
		d.wg.Go(func() { d.pipe(in, out, gen) })
		d.wg.Go(func() { d.pipe(out, in, gen) })
		d.wg.Go(func() {
			<-d.done
			_ = in.Close()
			_ = out.Close()
		})
	}
}

// pipe copies from to to while the connection's generation is current; after that it
// reads and drops, never closing, so neither end hears a thing.
func (d *dimmer) pipe(from, to net.Conn, gen int64) {
	buf := make([]byte, 32<<10)
	for {
		n, err := from.Read(buf)
		if n > 0 && d.gen.Load() == gen {
			if _, werr := to.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// A laptop that changes networks under the window has its connection to the bridge go
// silent, not closed: the relay's PING finds it dead, the window's stream through the
// relay ends, and reopened it resumes over a new connection, losing nothing said
// meanwhile.
func TestASilentBridgeConnectionIsFoundDeadAndTheStreamResumes(t *testing.T) {
	t.Parallel()
	laptop := newKey(t)
	b := serveBridge(t, laptop)
	network := newDimmer(t, b.addr)
	bodies := follow(t, backendVia(viaBridge(t, laptop, b.id, network.addr())))
	read(t, bodies, b.messages.says("before", 5), 5*time.Second)

	network.dark()
	// The PING's wait and its answer's, then the window reopening the stream.
	read(t, bodies, b.messages.says("away", 20), time.Minute)
	read(t, bodies, b.messages.says("after", 5), 5*time.Second)
}
