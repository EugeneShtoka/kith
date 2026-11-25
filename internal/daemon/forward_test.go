package daemon_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// serveProxy forwards BackendService to up on storage's kith-proxy socket, as kith-proxy
// does, until the returned stop (idempotent) or the test's end.
func serveProxy(t *testing.T, storage domain.Storage, up daemon.Upstream) (stop func()) {
	t.Helper()
	ln, err := daemon.Listen(context.Background(), storage.ProxySocketPath())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	daemon.HandleBackend(mux, up.Forward(slog.New(slog.DiscardHandler)))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: settle}
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(ln) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		_ = srv.Close()
		<-served
		if err := os.Remove(storage.ProxySocketPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove the proxy's socket: %v", err)
		}
	}
	t.Cleanup(stop)
	return stop
}

// Several clients of one machine reach the bridge through its one proxy, and each keeps
// every message, once and in order, across the bridge restarting and the proxy
// restarting under them. The clients' own key is not paired and the address they hold
// answers nothing: only the proxy reaches the bridge.
func TestClientsThroughOneProxyResumeAcrossRestarts(t *testing.T) {
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
	up, err := daemon.BridgeUpstream(certOf(t, laptop), b)
	if err != nil {
		t.Fatal(err)
	}
	storage := domain.Storage{RuntimeDir: t.TempDir(), Instance: "kith"}
	stopProxy := serveProxy(t, storage, up)

	const clients = 3
	held := config.Bridge{Name: "home", ID: b.ID, Addresses: []string{freeAddr(t, "127.0.0.1")}}
	var remotes []*daemon.Remote
	for range clients {
		r, err := daemon.AttachBridge(t.Context(), storage, certOf(t, newDeviceKey(t)), held, settle)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = r.Start(context.Background()) }()
		t.Cleanup(r.Stop)
		remotes = append(remotes, r)
	}
	run.waitAttached(t, clients)

	want := burst(cache, "before", 100)
	run.stop(t)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove socket: %v", err)
	}
	want = append(want, burst(cache, "bridge-down", 20)...)
	run = serve()
	run.waitAttached(t, clients)
	want = append(want, burst(cache, "bridge-back", 20)...)

	stopProxy()
	want = append(want, burst(cache, "proxy-down", 20)...)
	serveProxy(t, storage, up)
	run.waitAttached(t, clients)
	want = append(want, burst(cache, "proxy-back", 20)...)

	for _, r := range remotes {
		readBodies(t, r, want)
	}
}

// A bridge refusing the proxy's key reaches a client through the proxy as permission
// denied naming the bridge, as it would dialing the bridge itself: kept, not read as
// the proxy unavailable.
func TestABridgesRefusalReachesAClientThroughTheProxy(t *testing.T) {
	t.Parallel()
	laptop, bridgeKey, stranger := newDeviceKey(t), newDeviceKey(t), newDeviceKey(t)
	addr := freeAddr(t, "127.0.0.1")
	runDaemon(t, filepath.Join(t.TempDir(), "kithd.sock"), &fakeBackend{Nop: nopWithChannels()},
		overTLS(t, addr, bridgeKey, laptop))
	(&bridge{t: t, logs: &syncBuffer{}}).waitListening(addr, true)
	up, err := daemon.BridgeUpstream(certOf(t, stranger), config.Bridge{Name: "home", ID: idOf(t, bridgeKey).String(), Addresses: []string{addr}})
	if err != nil {
		t.Fatal(err)
	}
	storage := domain.Storage{RuntimeDir: t.TempDir(), Instance: "kith"}
	serveProxy(t, storage, up)

	client := backendv1connect.NewBackendServiceClient(daemon.Dial(storage.ProxySocketPath()), "http://kithd")
	// A refusal can meet the connection's reset before its alert, which says nothing of
	// the key (Upstream.refused): the alert comes on a later attempt, and is kept after.
	deadline := time.Now().Add(settle)
	for {
		_, err = client.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{}))
		if connect.CodeOf(err) == connect.CodePermissionDenied || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "bridge home refused this device") {
		t.Errorf("Status through the proxy of a key the bridge refuses = %v, want permission denied naming bridge home", err)
	}
	if _, err = client.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("Status again after the refusal = %v, want it still permission denied", err)
	}
}

// A client asks whether the proxy answers as it starts: with none listening, or only a
// socket file a proxy left behind, it dials the bridge itself.
func TestNoProxyListeningIsDialedPast(t *testing.T) {
	t.Parallel()
	storage := domain.Storage{RuntimeDir: t.TempDir(), Instance: "kith"}
	if daemon.ProxyAnswers(t.Context(), storage) {
		t.Error("ProxyAnswers() with no socket = true")
	}
	if err := os.WriteFile(storage.ProxySocketPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if daemon.ProxyAnswers(t.Context(), storage) {
		t.Error("ProxyAnswers() with a stale file = true")
	}
	if daemon.ProxyAnswers(t.Context(), domain.Storage{}) {
		t.Error("ProxyAnswers() with no runtime dir = true")
	}
}
