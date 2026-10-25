package signalhelper_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	// sinks are the sinks Connect was handed, by ACI.
	sinks chan signalhelper.Sink
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

func (f *fakeNetwork) Send(context.Context, *signalv1.SendRequest) error { return nil }

func (f *fakeNetwork) Typing(context.Context, *signalv1.TypingRequest) error { return nil }

func (f *fakeNetwork) MarkRead(context.Context, *signalv1.MarkReadRequest) error { return nil }

// Download has the attachment "kept" and nothing else: any other is gone.
func (f *fakeNetwork) Download(_ context.Context, req *signalv1.DownloadRequest) ([]byte, error) {
	if string(req.GetPointer()) != "kept" {
		return nil, fmt.Errorf("fake: %w", signalhelper.ErrGone)
	}
	return []byte("the photo"), nil
}

func (f *fakeNetwork) Unlink(context.Context, string) error { return nil }

func (f *fakeNetwork) Leave(context.Context, string, string) error { return nil }

func (f *fakeNetwork) CreateGroup(context.Context, *signalv1.CreateGroupRequest) (*signalv1.CreateGroupResponse, error) {
	return &signalv1.CreateGroupResponse{}, nil
}

func (f *fakeNetwork) FindPerson(context.Context, *signalv1.FindPersonRequest) (*signalv1.FindPersonResponse, error) {
	return &signalv1.FindPersonResponse{}, nil
}

func (f *fakeNetwork) GroupByInvite(context.Context, *signalv1.GroupByInviteRequest) (*signalv1.GroupByInviteResponse, error) {
	return &signalv1.GroupByInviteResponse{}, nil
}

func (f *fakeNetwork) JoinGroup(context.Context, *signalv1.JoinGroupRequest) (*signalv1.JoinGroupResponse, error) {
	return &signalv1.JoinGroupResponse{}, nil
}

func (f *fakeNetwork) Upload(_ context.Context, req *signalv1.UploadRequest) (*signalv1.Attachment, error) {
	return &signalv1.Attachment{FileName: req.GetFileName(), Pointer: []byte("kept")}, nil
}

func (f *fakeNetwork) Connect(_ context.Context, _ string, sink signalhelper.Sink) error {
	f.sinks <- sink
	return nil
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

// Heartbeats keep it alive well past the lapse; once they stop, it lapses. The lapse
// runs from the start, so the first beat (and its connection) must land within it,
// under a whole suite's load too: the beats come at a tenth of it.
func TestHeartbeatsKeepTheHelperAlive(t *testing.T) {
	t.Parallel()
	const lapse = time.Second
	client, lapsed := serve(t, &fakeNetwork{}, lapse)
	ctx := context.Background()
	stop := time.Now().Add(3 * lapse)
	for time.Now().Before(stop) {
		if _, err := client.Heartbeat(ctx, connect.NewRequest(&signalv1.HeartbeatRequest{})); err != nil {
			t.Fatal(err)
		}
		select {
		case <-lapsed:
			t.Fatal("lapsed while kithd was beating")
		case <-time.After(lapse / 10):
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

// connected starts the helper with a fake network and connects one account, returning
// the client and the sink the account's events go into.
func connected(t *testing.T) (signalv1connect.SignalServiceClient, signalhelper.Sink) {
	t.Helper()
	ctx := context.Background()
	network := &fakeNetwork{sinks: make(chan signalhelper.Sink, 1)}
	client, _ := serve(t, network, time.Minute)
	if _, err := client.Start(ctx, connect.NewRequest(&signalv1.StartRequest{})); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Connect(ctx, connect.NewRequest(&signalv1.ConnectRequest{Aci: "aci-1"})); err != nil {
		t.Fatal(err)
	}
	return client, <-network.sinks
}

func message(body string) *signalv1.EventsResponse {
	return &signalv1.EventsResponse{Account: "aci-1", Event: &signalv1.EventsResponse_Message{Message: &signalv1.Message{Body: body}}}
}

// deliver delivers each body in turn, as signalmeow's handler would, closing its
// channel in kept as each returns (Signal is told then).
func deliver(sink signalhelper.Sink, bodies ...string) []chan struct{} {
	kept := make([]chan struct{}, len(bodies))
	for i := range kept {
		kept[i] = make(chan struct{})
	}
	go func() {
		for i, b := range bodies {
			sink.Deliver(message(b))
			close(kept[i])
		}
	}()
	return kept
}

func receive(t *testing.T, stream *connect.ServerStreamForClient[signalv1.EventsResponse]) *signalv1.EventsResponse {
	t.Helper()
	if !stream.Receive() {
		t.Fatalf("the stream ended: %v", stream.Err())
	}
	return stream.Msg()
}

func returned(c chan struct{}) bool {
	select {
	case <-c:
		return true
	case <-time.After(200 * time.Millisecond):
		return false
	}
}

// An event waits in the helper until kithd acks it: only then does Deliver return (and
// Signal hear it arrived).
func TestAnEventWaitsForItsAck(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client, sink := connected(t)
	stream, err := client.Events(ctx, connect.NewRequest(&signalv1.EventsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	kept := deliver(sink, "one", "two")
	first := receive(t, stream)
	if first.GetMessage().GetBody() != "one" || first.GetToken() == 0 {
		t.Fatalf("first = %v", first)
	}
	if returned(kept[0]) {
		t.Fatal("Deliver returned before kithd acked")
	}
	if _, err := client.Ack(ctx, connect.NewRequest(&signalv1.AckRequest{Token: first.GetToken()})); err != nil {
		t.Fatal(err)
	}
	if !returned(kept[0]) {
		t.Fatal("Deliver did not return once acked")
	}
	second := receive(t, stream)
	if second.GetMessage().GetBody() != "two" || second.GetToken() == first.GetToken() {
		t.Errorf("second = %v", second)
	}
	// An ack again, or for no event, is nothing.
	for _, token := range []uint64{first.GetToken(), 999} {
		if _, err := client.Ack(ctx, connect.NewRequest(&signalv1.AckRequest{Token: token})); err != nil {
			t.Errorf("Ack(%d) = %v", token, err)
		}
	}
	if returned(kept[1]) {
		t.Error("an unrelated ack released the second event")
	}
}

// A new listener (kithd's stream broke, or kithd came back) is sent every event not yet
// acked first, in order, then what comes; states are not sent again. The old listener's
// stream ends.
func TestANewListenerGetsWhatWasNotAcked(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client, sink := connected(t)
	oldCtx, dropOld := context.WithCancel(ctx)
	defer dropOld()
	old, err := client.Events(oldCtx, connect.NewRequest(&signalv1.EventsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	sink.Tell(&signalv1.EventsResponse{Account: "aci-1", Event: &signalv1.EventsResponse_Connection{Connection: &signalv1.Connection{State: signalv1.Connection_STATE_CONNECTED}}})
	if got := receive(t, old); got.GetConnection() == nil || got.GetToken() != 0 {
		t.Fatalf("state = %v", got)
	}
	kept := deliver(sink, "one", "two", "three")
	one := receive(t, old)
	if _, err = client.Ack(ctx, connect.NewRequest(&signalv1.AckRequest{Token: one.GetToken()})); err != nil {
		t.Fatal(err)
	}
	two := receive(t, old) // sent, never acked: lost with the old listener
	if two.GetMessage().GetBody() != "two" {
		t.Fatalf("two = %v", two)
	}

	fresh, err := client.Events(ctx, connect.NewRequest(&signalv1.EventsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	again := receive(t, fresh)
	if again.GetMessage().GetBody() != "two" || again.GetToken() != two.GetToken() {
		t.Fatalf("the new listener's first = %v, want two again", again)
	}
	if _, err := client.Ack(ctx, connect.NewRequest(&signalv1.AckRequest{Token: again.GetToken()})); err != nil {
		t.Fatal(err)
	}
	three := receive(t, fresh)
	if three.GetMessage().GetBody() != "three" {
		t.Fatalf("then %v, want three", three)
	}
	if !returned(kept[1]) || returned(kept[2]) {
		t.Error("two was not released by its ack, or three was without one")
	}
	if old.Receive() {
		t.Errorf("the old listener was sent %v after a new one took over", old.Msg())
	}
}

// An attachment comes back as its bytes; one Signal no longer has is NotFound, which
// kithd tells apart from the helper's being unreachable.
func TestADownloadSaysWhenTheAttachmentIsGone(t *testing.T) {
	t.Parallel()
	client, _ := serve(t, &fakeNetwork{}, time.Minute)
	ctx := context.Background()
	up, err := client.Upload(ctx, connect.NewRequest(&signalv1.UploadRequest{Account: "a", FileName: "beach.jpg"}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Download(ctx, connect.NewRequest(&signalv1.DownloadRequest{Pointer: up.Msg.GetAttachment().GetPointer()}))
	if err != nil || string(got.Msg.GetData()) != "the photo" {
		t.Fatalf("downloaded %q, %v", got.Msg.GetData(), err)
	}
	_, err = client.Download(ctx, connect.NewRequest(&signalv1.DownloadRequest{Pointer: []byte("old")}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("a gone attachment: %v, want NotFound", err)
	}
}
