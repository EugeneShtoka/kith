package signalhelper_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/api/signal/v1/signalv1connect"
	"github.com/EugeneShtoka/kith/internal/signalhelper"
)

// fakeNetwork links after drawing links, as a phone that scans the last one.
type fakeNetwork struct {
	devices []signalhelper.Device
	links   []string
	linked  signalhelper.Device
	opened  atomic.Int32
	key     []byte
}

func (f *fakeNetwork) Open(_ context.Context, key []byte) ([]signalhelper.Device, error) {
	f.opened.Add(1)
	f.key = key
	return f.devices, nil
}

func (f *fakeNetwork) Link(_ context.Context, _ string, show func(string) error) (signalhelper.Device, error) {
	for _, l := range f.links {
		if err := show(l); err != nil {
			return signalhelper.Device{}, err
		}
	}
	return f.linked, nil
}

// serve runs the helper on a socket of its own and returns a client for it and a
// channel that closes when the heartbeat lapses.
func serve(t *testing.T, network signalhelper.Network, lapse time.Duration) (signalv1connect.SignalServiceClient, <-chan struct{}) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "signal.sock")
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	lapsed := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- signalhelper.Serve(ctx, ln, network, lapse, func() { close(lapsed) }, slog.New(slog.DiscardHandler))
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve = %v", err)
		}
	})
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
	return signalv1connect.NewSignalServiceClient(hc, "http://kith-signal"), lapsed
}

// With no heartbeat, the helper gives up: from the start too, so a kithd that dies
// before its first beat is noticed.
func TestTheHelperLapsesWithoutHeartbeats(t *testing.T) {
	t.Parallel()
	_, lapsed := serve(t, &fakeNetwork{}, 50*time.Millisecond)
	select {
	case <-lapsed:
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat ever came, and the helper did not lapse")
	}
}

// Heartbeats keep it alive well past the lapse; once they stop, it lapses.
func TestHeartbeatsKeepTheHelperAlive(t *testing.T) {
	t.Parallel()
	const lapse = 300 * time.Millisecond
	client, lapsed := serve(t, &fakeNetwork{}, lapse)
	ctx := context.Background()
	stop := time.Now().Add(4 * lapse)
	for time.Now().Before(stop) {
		if _, err := client.Heartbeat(ctx, connect.NewRequest(&signalv1.HeartbeatRequest{})); err != nil {
			t.Fatal(err)
		}
		select {
		case <-lapsed:
			t.Fatal("lapsed while kithd was beating")
		case <-time.After(lapse / 4):
		}
	}
	select {
	case <-lapsed:
	case <-time.After(5 * time.Second):
		t.Fatal("the heartbeats stopped, and the helper did not lapse")
	}
}

// Start opens the store with kithd's key once, listing its devices; Link before it,
// or a second Start, is refused without touching the store.
func TestStartOpensTheStoreOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	network := &fakeNetwork{devices: []signalhelper.Device{{ACI: "aci-1", Number: "+15550001"}}}
	client, _ := serve(t, network, time.Minute)

	stream, err := client.Link(ctx, connect.NewRequest(&signalv1.LinkRequest{DeviceName: "kith"}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
		t.Errorf("Link before Start sent %v", stream.Msg())
	}
	if connect.CodeOf(stream.Err()) != connect.CodeFailedPrecondition {
		t.Errorf("Link before Start = %v", stream.Err())
	}

	key := []byte("0123456789abcdef0123456789abcdef")
	resp, err := client.Start(ctx, connect.NewRequest(&signalv1.StartRequest{Key: key}))
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Msg.GetDevices(); len(got) != 1 || got[0].GetAci() != "aci-1" || got[0].GetNumber() != "+15550001" {
		t.Errorf("devices = %v", got)
	}
	if !bytes.Equal(network.key, key) {
		t.Errorf("the store was opened with %q", network.key)
	}
	if _, err := client.Start(ctx, connect.NewRequest(&signalv1.StartRequest{Key: key})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a second Start = %v", err)
	}
	if n := network.opened.Load(); n != 1 {
		t.Errorf("the store was opened %d times", n)
	}
}

// Link streams every link drawn, in order, then the account linked.
func TestLinkStreamsLinksThenTheAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	network := &fakeNetwork{
		links:  []string{"sgnl://linkdevice?uuid=1", "sgnl://linkdevice?uuid=2"},
		linked: signalhelper.Device{ACI: "aci-2", Number: "+15550002"},
	}
	client, _ := serve(t, network, time.Minute)
	if _, err := client.Start(ctx, connect.NewRequest(&signalv1.StartRequest{})); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Link(ctx, connect.NewRequest(&signalv1.LinkRequest{DeviceName: "kith"}))
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	var linked *signalv1.Device
	for stream.Receive() {
		switch step := stream.Msg().GetStep().(type) {
		case *signalv1.LinkResponse_Url:
			if linked != nil {
				t.Errorf("a link after the account: %s", step.Url)
			}
			urls = append(urls, step.Url)
		case *signalv1.LinkResponse_Linked:
			linked = step.Linked
		}
	}
	if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if len(urls) != 2 || urls[0] != network.links[0] || urls[1] != network.links[1] {
		t.Errorf("links = %v", urls)
	}
	if linked.GetAci() != "aci-2" || linked.GetNumber() != "+15550002" {
		t.Errorf("linked = %v", linked)
	}
}
