package daemon_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// overTLS has a daemon also serve addr over TLS as the bridge whose key is kithd, to the
// device whose key is client, and report itself ready.
func overTLS(t *testing.T, addr string, kithd, client *ecdsa.PrivateKey) func(*daemon.Daemon) {
	t.Helper()
	roster := filepath.Join(t.TempDir(), "devices.toml")
	if err := os.WriteFile(roster, fmt.Appendf(nil, "[[device]]\nname = \"laptop\"\nid = %q\n", idOf(t, client).String()), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := identityOf(t, kithd)
	return func(d *daemon.Daemon) {
		d.Paired = daemon.NewDevices(roster, identity, slog.New(slog.DiscardHandler))
		d.Paired.Listen([]string{addr})
		d.State.Synced(time.Now())
	}
}

// certOf is the certificate a device with key presents.
func certOf(t *testing.T, key *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	cert, err := devicekey.Certificate(key)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// A bridge's addresses are tried in order and the first answering with its key is the
// one used: an address nobody listens on and one where another kithd answers (its key
// not the one pinned) are passed over. With only those, attaching fails saying why.
func TestABridgeIsReachedPastAddressesThatAreNotIt(t *testing.T) {
	t.Parallel()
	laptop, bridgeKey := newDeviceKey(t), newDeviceKey(t)
	other := serveBridge(t, bridgeOpts{}) // another kithd, whose key is not the bridge's
	addr := freeAddr(t, "127.0.0.1")
	cache := &numberedCache{epoch: "one"}
	runDaemon(t, filepath.Join(t.TempDir(), "kithd.sock"), &fakeBackend{Nop: nopWithChannels()},
		cache.serves, overTLS(t, addr, bridgeKey, laptop))
	pin := idOf(t, bridgeKey).String()

	nobody := freeAddr(t, "127.0.0.1")
	b := config.Bridge{Name: "home", ID: pin, Addresses: []string{nobody, other.addr, addr}}
	r, err := daemon.AttachBridge(t.Context(), certOf(t, laptop), b, settle)
	if err != nil {
		t.Fatalf("AttachBridge() = %v, want the third address", err)
	}
	t.Cleanup(r.Stop)

	b.Addresses = []string{nobody, other.addr}
	_, err = daemon.AttachBridge(t.Context(), certOf(t, laptop), b, 500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "another key") {
		t.Fatalf("AttachBridge() past no address of the bridge = %v, want it to name the other key", err)
	}
}

// Across a bridge restart in the middle of a burst, a client over TLS gets every message
// once and in order, as one over the socket does (#387): those it held, those it was cut
// off from, those committed while the bridge was down, and what follows.
func TestAClientOverTLSResumesAcrossABridgeRestart(t *testing.T) {
	t.Parallel()
	laptop, bridgeKey := newDeviceKey(t), newDeviceKey(t)
	addr := freeAddr(t, "127.0.0.1")
	path := filepath.Join(t.TempDir(), "kithd.sock")
	cache := &numberedCache{epoch: "one"}
	serve := func() *daemonRun {
		return runDaemon(t, path, &fakeBackend{Nop: nopWithChannels()}, cache.serves, overTLS(t, addr, bridgeKey, laptop))
	}
	run := serve()
	b := config.Bridge{Name: "home", ID: idOf(t, bridgeKey).String(), Addresses: []string{addr}}
	client, err := daemon.AttachBridge(t.Context(), certOf(t, laptop), b, settle)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = client.Start(context.Background()) }()
	t.Cleanup(client.Stop)
	run.waitAttached(t, 1)

	want := burst(cache, "before", 300)
	time.Sleep(20 * time.Millisecond) // the client takes what its queue holds
	run.stop(t)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove socket: %v", err)
	}
	want = append(want, burst(cache, "down", 50)...)
	run = serve()
	run.waitAttached(t, 1)
	want = append(want, burst(cache, "after", 50)...)
	readBodies(t, client, want)
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
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
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

// A laptop that changes networks has its connection go silent, not closed: HTTP/2's
// PING finds it dead, the client reattaches over a new one, and the message stream
// resumes, losing nothing committed meanwhile.
func TestAClientFindsASilentConnectionDeadAndResumes(t *testing.T) {
	t.Parallel()
	laptop, bridgeKey := newDeviceKey(t), newDeviceKey(t)
	addr := freeAddr(t, "127.0.0.1")
	cache := &numberedCache{epoch: "one"}
	run := runDaemon(t, filepath.Join(t.TempDir(), "kithd.sock"), &fakeBackend{Nop: nopWithChannels()},
		cache.serves, overTLS(t, addr, bridgeKey, laptop))
	network := newDimmer(t, addr)
	b := config.Bridge{Name: "home", ID: idOf(t, bridgeKey).String(), Addresses: []string{network.addr()}}
	client, err := daemon.AttachBridge(t.Context(), certOf(t, laptop), b, settle)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = client.Start(context.Background()) }()
	t.Cleanup(client.Stop)
	run.waitAttached(t, 1)
	readBodies(t, client, burst(cache, "before", 5))

	network.dark()
	want := burst(cache, "away", 20)
	// The PING's wait and its answer's, then a reattach.
	deadline := time.After(time.Minute)
	for _, body := range want {
		select {
		case got, ok := <-client.Messages():
			if !ok {
				t.Fatal("the message channel closed; the client detached for good")
			}
			if got.Body != body {
				t.Fatalf("got %q, want %q", got.Body, body)
			}
		case <-deadline:
			t.Fatalf("no message %q a minute after the network changed", body)
		}
	}
}
