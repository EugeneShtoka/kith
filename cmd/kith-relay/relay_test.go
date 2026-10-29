package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/api/device/v1/devicev1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

const relayBase = "http://kith-relay"

// kithd is a fake daemon on a socket: Status answers, Changed opens at once, sends what
// the test puts on frames, and holds the stream open until the client or kithd goes.
type kithd struct {
	backendv1connect.UnimplementedBackendServiceHandler
	frames chan v1.Change

	mu       sync.Mutex
	profiles []string // Kith-Profile of each Status, in order
	srv      *http.Server
	served   chan struct{}
}

func newKithd() *kithd { return &kithd{frames: make(chan v1.Change, 16)} }

func (k *kithd) Status(_ context.Context, req *connect.Request[v1.StatusRequest]) (*connect.Response[v1.StatusResponse], error) {
	k.mu.Lock()
	k.profiles = append(k.profiles, req.Header().Get("Kith-Profile"))
	k.mu.Unlock()
	return connect.NewResponse(&v1.StatusResponse{Ready: true}), nil
}

func (k *kithd) Changed(ctx context.Context, _ *connect.Request[v1.ChangedRequest], stream *connect.ServerStream[v1.ChangedResponse]) error {
	// Headers as it opens, as kithd sends them: a stream may be silent for hours.
	if err := stream.Send(nil); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case c := <-k.frames:
			if err := stream.Send(&v1.ChangedResponse{Change: c}); err != nil {
				return err
			}
		}
	}
}

func (k *kithd) seen() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.profiles...)
}

// start serves k on socket until stop.
func (k *kithd) start(t *testing.T, socket string) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(backendv1connect.NewBackendServiceHandler(k))
	k.srv = &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	k.served = make(chan struct{})
	go func() { defer close(k.served); _ = k.srv.Serve(ln) }()
	t.Cleanup(k.stop)
}

// stop is kithd exiting: every connection is cut, streams mid-frame included.
func (k *kithd) stop() {
	_ = k.srv.Close()
	<-k.served
}

// relayOn serves the relay's handler on a socket of its own, forwarding to kithdSocket,
// for relayConfig's config.
func relayOn(t *testing.T, kithdSocket string) string {
	t.Helper()
	configPath, _ := relayConfig(t, t.TempDir())
	return relayFor(t, kithdSocket, configPath)
}

// relayFor is relayOn for the config at configPath.
func relayFor(t *testing.T, kithdSocket, configPath string) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "relay.sock")
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: newHandler(kithdSocket, configPath, slog.New(slog.DiscardHandler)), ReadHeaderTimeout: time.Second}
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); <-served })
	return socket
}

func backendVia(socket string) backendv1connect.BackendServiceClient {
	return backendv1connect.NewBackendServiceClient(daemon.Dial(socket), relayBase)
}

// changed opens Changed through backend: kithd sends its headers as it opens a stream,
// so the opening crosses the relay at once too, or the window waits on a silent stream.
func changed(t *testing.T, backend backendv1connect.BackendServiceClient) *connect.ServerStreamForClient[v1.ChangedResponse] {
	t.Helper()
	type opened struct {
		stream *connect.ServerStreamForClient[v1.ChangedResponse]
		err    error
	}
	got := within(t, 5*time.Second, "the stream opening through the relay", func() opened {
		stream, err := backend.Changed(t.Context(), connect.NewRequest(&v1.ChangedRequest{}))
		return opened{stream, err}
	})
	if got.err != nil {
		t.Fatal(got.err)
	}
	t.Cleanup(func() { _ = got.stream.Close() })
	return got.stream
}

func kithdSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "kithd.sock")
}

// within fails the test unless f returns inside d: a stream frame held, or a stream left
// hanging, shows as a timeout and not as a hung test.
func within[T any](t *testing.T, d time.Duration, what string, f func() T) T {
	t.Helper()
	got := make(chan T, 1)
	go func() { got <- f() }()
	select {
	case v := <-got:
		return v
	case <-time.After(d):
		t.Fatalf("%s did not happen within %s", what, d)
		var zero T
		return zero
	}
}

// What reaches kithd through the relay is BackendService, by POST, and nothing else:
// every one of its methods is forwarded with the window's profile, DeviceService is
// answered by the relay itself, and every other path or method never reaches kithd.
func TestOnlyBackendServiceReachesKithdThroughTheRelay(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var reached []string
	socket := kithdSocket(t)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	kithdSrv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reached = append(reached, r.Method+" "+r.URL.Path+" "+r.Header.Get("Kith-Profile"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = kithdSrv.Serve(ln) }()
	t.Cleanup(func() { _ = kithdSrv.Close() })
	client := daemon.Dial(relayOn(t, socket))
	// What one request reaches, without a redirect the relay answered with followed.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	send := func(method, path string) int {
		t.Helper()
		req, rerr := http.NewRequestWithContext(t.Context(), method, relayBase+path, strings.NewReader("{}"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Kith-Profile", "Work")
		resp, derr := client.Do(req)
		if derr != nil {
			t.Fatalf("%s %s: %v", method, path, derr)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	taken := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := reached
		reached = nil
		return out
	}

	methods := v1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods()
	for i := range methods.Len() {
		path := "/" + backendv1connect.BackendServiceName + "/" + string(methods.Get(i).Name())
		if code := send(http.MethodPost, path); code != http.StatusOK {
			t.Errorf("POST %s = %d, want kithd's 200", path, code)
		}
		if got := taken(); len(got) != 1 || got[0] != "POST "+path+" Work" {
			t.Errorf("POST %s reached kithd as %q, want it once with its profile", path, got)
		}
	}

	refused := []struct{ method, path string }{
		{http.MethodGet, "/" + backendv1connect.BackendServiceName + "/Status"},
		{http.MethodPut, "/" + backendv1connect.BackendServiceName + "/Status"},
		{http.MethodPost, "/"},
		{http.MethodPost, "/" + backendv1connect.BackendServiceName},
		{http.MethodPost, "/" + backendv1connect.BackendServiceName + "X/Status"},
		{http.MethodPost, "/backend.v1.BackendServic/Status"},
		{http.MethodPost, "/signal.v1.SignalService/Start"},
		{http.MethodPost, "/" + backendv1connect.BackendServiceName + "/../signal.v1.SignalService/Start"},
		{http.MethodPost, "/" + backendv1connect.BackendServiceName + "/%2e%2e/signal.v1.SignalService/Start"},
		{http.MethodPost, "//" + backendv1connect.BackendServiceName + "/Status"},
		{http.MethodPost, "/x/" + backendv1connect.BackendServiceName + "/Status"},
		{http.MethodPost, "/" + devicev1connect.DeviceServiceName + "/Nothing"},
	}
	for _, r := range refused {
		code := send(r.method, r.path)
		if code == http.StatusOK {
			t.Errorf("%s %s = 200, want it refused", r.method, r.path)
		}
		if got := taken(); len(got) != 0 {
			t.Errorf("%s %s reached kithd as %q", r.method, r.path, got)
		}
	}

	resp, err := devicev1connect.NewDeviceServiceClient(client, relayBase).Device(t.Context(), connect.NewRequest(&devicev1.DeviceRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetSocket() != socket || strings.Join(resp.Msg.GetProfiles(), ",") != "Home,Work" {
		t.Errorf("Device = %v, want socket %s and profiles Home,Work", resp.Msg, socket)
	}
	if _, err := devicev1connect.NewDeviceServiceClient(client, relayBase).ChooseProfile(t.Context(), connect.NewRequest(&devicev1.ChooseProfileRequest{Name: "Work"})); err != nil {
		t.Fatal(err)
	}
	if got := taken(); len(got) != 0 {
		t.Errorf("DeviceService reached kithd as %q, want the relay to answer it", got)
	}
}

// A stream frame crosses the relay as kithd sends it, while the stream stays open: a
// frame held in a buffer until the next, or until the end, would never show.
func TestAStreamFrameCrossesTheRelayAsItIsSent(t *testing.T) {
	t.Parallel()
	socket := kithdSocket(t)
	k := newKithd()
	k.start(t, socket)
	stream := changed(t, backendVia(relayOn(t, socket)))
	for _, c := range []v1.Change{v1.Change_CHANGE_ROOMS, v1.Change_CHANGE_ACCOUNTS, v1.Change_CHANGE_ROOMS} {
		k.frames <- c
		if !within(t, 5*time.Second, "the frame crossing the relay", stream.Receive) {
			t.Fatalf("the stream ended: %v", stream.Err())
		}
		if got := stream.Msg().GetChange(); got != c {
			t.Errorf("frame %v, want %v", got, c)
		}
	}
}

// kithd restarting mid-stream ends the window's stream through the relay at once, with
// an error, and the next stream and call through the same relay reach the new kithd.
func TestKithdRestartingMidStreamEndsTheStreamAndTheNextReachesTheNewOne(t *testing.T) {
	t.Parallel()
	socket := kithdSocket(t)
	k := newKithd()
	k.start(t, socket)
	backend := backendVia(relayOn(t, socket))
	stream := changed(t, backend)
	k.frames <- v1.Change_CHANGE_ROOMS
	if !within(t, 5*time.Second, "the first frame", stream.Receive) {
		t.Fatalf("the stream ended: %v", stream.Err())
	}

	k.stop()
	if within(t, 5*time.Second, "the stream ending with kithd", stream.Receive) {
		t.Fatalf("a frame %v after kithd stopped", stream.Msg())
	}
	if stream.Err() == nil {
		t.Error("the stream ended cleanly when kithd went, want an error the window reattaches on")
	}
	if _, err := backend.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("Status with kithd gone = %v, want unavailable", err)
	}

	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	again := newKithd()
	again.start(t, socket)
	next := changed(t, backend)
	again.frames <- v1.Change_CHANGE_ACCOUNTS
	if !within(t, 5*time.Second, "a frame from the new kithd", next.Receive) {
		t.Fatalf("the new stream ended: %v", next.Err())
	}
	if _, err := backend.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); err != nil {
		t.Errorf("Status from the new kithd = %v", err)
	}
}

// kithd answers a stream with its headers as it opens, which can be before the relay has
// read all of the window's request: the headers cross at once, the rest of the request
// still reaches kithd, and the stream stays open. (An HTTP/1.1 server is not full duplex
// by default: it reads a request's body to its end before it writes the response.)
func TestAStreamAnsweredBeforeItsRequestEndsStaysOpen(t *testing.T) {
	t.Parallel()
	socket := kithdSocket(t)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	frame := []byte{0, 0, 0, 0, 2, 0x08, byte(v1.Change_CHANGE_ROOMS)} // one ChangedResponse, enveloped
	kithdSrv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if derr := http.NewResponseController(w).EnableFullDuplex(); derr != nil {
			t.Error(derr)
			return
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		if _, cerr := io.Copy(io.Discard, r.Body); cerr != nil {
			return // the request was cut: no frame
		}
		_, _ = w.Write(frame)
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	})}
	go func() { _ = kithdSrv.Serve(ln) }()
	t.Cleanup(func() { _ = kithdSrv.Close() })

	body, ends := io.Pipe()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, relayBase+"/"+backendv1connect.BackendServiceName+"/Changed", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/connect+proto")
	go func() { _, _ = ends.Write([]byte{0, 0, 0, 0, 0}) }() // the request's one message, empty
	resp := within(t, 5*time.Second, "the stream's headers", func() *http.Response {
		r, derr := daemon.Dial(relayOn(t, socket)).Do(req)
		if derr != nil {
			t.Error(derr)
		}
		return r
	})
	if resp == nil {
		t.FailNow()
	}
	defer func() { _ = resp.Body.Close() }()
	_ = ends.Close() // the request ends only now, after kithd has answered
	got := within(t, 5*time.Second, "the frame after the request ended", func() []byte {
		b := make([]byte, len(frame))
		_, _ = io.ReadFull(resp.Body, b)
		return b
	})
	if !bytes.Equal(got, frame) {
		t.Errorf("read %v, want the frame %v", got, frame)
	}
}

// relayConfig writes a config whose runtime dir (kithd's socket and the relay's) is in
// dir, and returns its path and kithd's socket.
func relayConfig(t *testing.T, dir string) (path, socket string) {
	t.Helper()
	cfg := config.Config{
		Profiles: []config.Profile{{Name: "Home", Include: []string{"*"}}, {Name: "Work", Include: []string{"*"}}},
		Matrix:   config.Matrix{Accounts: []config.MatrixAccount{{Name: "me", Homeserver: "https://x", User: "@me:x"}}},
		Storage: config.Storage{
			Instance: "t", DataDir: filepath.Join(dir, "data"), StateDir: filepath.Join(dir, "state"),
			CacheDir: filepath.Join(dir, "cache"), RuntimeDir: filepath.Join(dir, "run"),
		},
	}
	path = filepath.Join(dir, "config.toml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path, filepath.Join(cfg.Storage.RuntimeDir, "t.sock")
}

// window is a relay run as the desktop starts it: stdin a pipe the window holds, the
// socket's path read from stdout.
type window struct {
	socket string
	stdin  *io.PipeWriter
	done   chan error
}

func openWindow(t *testing.T, configPath string) window {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	w := window{stdin: inW, done: make(chan error, 1)}
	go func() {
		w.done <- run(context.Background(), slog.New(slog.DiscardHandler), configPath, inR, outW)
		_ = outW.Close()
	}()
	line := within(t, 5*time.Second, "the relay printing its socket", func() string {
		s, _ := bufio.NewReader(outR).ReadString('\n')
		return s
	})
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	w.socket = strings.TrimSpace(line)
	if w.socket == "" {
		t.Fatalf("the relay printed no socket: %v", <-w.done)
	}
	t.Cleanup(func() { _ = inW.Close() })
	return w
}

// closed is the window going, crashed or not: its end of the pipe closes.
func (w window) closed(t *testing.T) {
	t.Helper()
	_ = w.stdin.Close()
	if err := within(t, 5*time.Second, "the relay leaving with its window", func() error { return <-w.done }); err != nil {
		t.Errorf("the relay left with %v, want nil", err)
	}
}

func deviceOf(t *testing.T, socket string) (*devicev1.DeviceResponse, error) {
	t.Helper()
	resp, err := devicev1connect.NewDeviceServiceClient(daemon.Dial(socket), relayBase).Device(t.Context(), connect.NewRequest(&devicev1.DeviceRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// The relay never outlives its window: when the window's end of stdin closes, crashed
// or not, it stops serving and takes its socket with it. Until then only this user can
// open the socket, and it forwards to the config's kithd.
func TestTheRelayLeavesWithItsWindow(t *testing.T) {
	t.Parallel()
	path, kithdAt := relayConfig(t, t.TempDir())
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
	if got.GetSocket() != kithdAt {
		t.Errorf("Device = %v, want socket %s", got, kithdAt)
	}

	k := newKithd()
	k.start(t, kithdAt)
	backend := backendVia(w.socket)
	stream := changed(t, backend)
	k.frames <- v1.Change_CHANGE_ROOMS
	if !within(t, 5*time.Second, "a frame before the window goes", stream.Receive) {
		t.Fatalf("the stream ended: %v", stream.Err())
	}

	w.closed(t)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the relay's directory after it left: %v, want it gone", err)
	}
	if within(t, 5*time.Second, "the stream ending with the relay", stream.Receive) {
		t.Errorf("a frame %v after the relay left", stream.Msg())
	}
}

// A second window's relay on the same config gets a socket of its own: neither meets
// the other's, and one leaving leaves the other serving.
func TestASecondWindowsRelayKeepsItsOwnSocket(t *testing.T) {
	t.Parallel()
	path, kithdAt := relayConfig(t, t.TempDir())
	first, second := openWindow(t, path), openWindow(t, path)
	k := newKithd()
	k.start(t, kithdAt)
	if first.socket == second.socket {
		t.Fatalf("both relays listen on %s", first.socket)
	}

	first.closed(t)
	if _, err := deviceOf(t, second.socket); err != nil {
		t.Errorf("the second relay after the first left: %v", err)
	}
	if _, err := backendVia(second.socket).Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); err != nil {
		t.Errorf("Status through the second relay after the first left: %v", err)
	}
	if _, err := deviceOf(t, first.socket); err == nil {
		t.Error("the first relay still answers after its window left")
	}
	second.closed(t)
}

// The profile a request names reaches kithd as it was sent: the relay neither drops it
// (kithd would answer for the first profile) nor puts its own in its place.
func TestTheWindowsProfileReachesKithd(t *testing.T) {
	t.Parallel()
	socket := kithdSocket(t)
	k := newKithd()
	k.start(t, socket)
	backend := backendVia(relayOn(t, socket))
	for _, profile := range []string{"Work", "", "Home"} {
		req := connect.NewRequest(&v1.StatusRequest{})
		if profile != "" {
			req.Header().Set("Kith-Profile", profile)
		}
		if _, err := backend.Status(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(k.seen(), ","); got != "Work,,Home" {
		t.Errorf("kithd saw profiles %q, want Work,,Home", got)
	}
}
