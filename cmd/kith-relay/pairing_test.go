package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/api/device/v1/devicev1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

// trusted is a pairing started on a device the bridge trusts (its own socket, as `kith
// pair` there): the ticket, the device that answers it, and how it ends.
type trusted struct {
	ticket  *v1.PairingTicket
	pending chan *v1.PendingDevice
	outcome chan v1.PairingOutcome
	backend backendv1connect.BackendServiceClient
	// leave is the trusted side going away: its stream ends, and the pairing with it.
	leave  context.CancelFunc
	bridge *bridgeKithd
}

func startPairing(t *testing.T, b *bridgeKithd) *trusted {
	t.Helper()
	backend := backendv1connect.NewBackendServiceClient(daemon.Dial(b.socket), "http://kithd")
	ctx, leave := context.WithCancel(t.Context())
	t.Cleanup(leave)
	st, err := backend.StartPairing(ctx, connect.NewRequest(&v1.StartPairingRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Receive() || st.Msg().GetTicket() == nil {
		t.Fatalf("no ticket: %v", st.Err())
	}
	p := &trusted{
		ticket: st.Msg().GetTicket(), pending: make(chan *v1.PendingDevice, 1), outcome: make(chan v1.PairingOutcome, 1),
		backend: backend, leave: leave, bridge: b,
	}
	go func() {
		for st.Receive() {
			switch ev := st.Msg().GetEvent().(type) {
			case *v1.StartPairingResponse_Pending:
				p.pending <- ev.Pending
			case *v1.StartPairingResponse_Outcome:
				p.outcome <- ev.Outcome
			}
		}
	}()
	return p
}

// answer is the trusted side's answer to the device pending.
func (p *trusted) answer(t *testing.T, accept bool) {
	t.Helper()
	if _, err := p.backend.ConfirmPairing(t.Context(), connect.NewRequest(&v1.ConfirmPairingRequest{
		Token: p.ticket.GetToken(), Accept: accept,
	})); err != nil {
		t.Fatal(err)
	}
}

// uri is the ticket as the window shows it: written by the relay (PairingURI).
func (p *trusted) uri(t *testing.T, relay devicev1connect.DeviceServiceClient) string {
	t.Helper()
	res, err := relay.PairingURI(t.Context(), connect.NewRequest(&devicev1.PairingURIRequest{
		Token: p.ticket.GetToken(), Addresses: p.ticket.GetAddresses(), Id: p.ticket.GetId(), Expires: p.ticket.GetExpires(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg.GetUri()
}

// joining is a JoinBridge under way: the digits it shows, and how it ends.
type joining struct {
	digits chan string
	done   chan joined
	cut    context.CancelFunc
}

type joined struct {
	bridge *devicev1.JoinedBridge
	err    error
}

func join(t *testing.T, relay devicev1connect.DeviceServiceClient, uri string) *joining {
	t.Helper()
	ctx, cut := context.WithCancel(t.Context())
	t.Cleanup(cut)
	j := &joining{digits: make(chan string, 4), done: make(chan joined, 1), cut: cut}
	go func() {
		st, err := relay.JoinBridge(ctx, connect.NewRequest(&devicev1.JoinBridgeRequest{Uri: uri, Name: "laptop"}))
		if err != nil {
			j.done <- joined{err: err}
			return
		}
		var got joined
		for st.Receive() {
			switch ev := st.Msg().GetEvent().(type) {
			case *devicev1.JoinBridgeResponse_Digits:
				j.digits <- ev.Digits
			case *devicev1.JoinBridgeResponse_Joined:
				got.bridge = ev.Joined
			}
		}
		got.err = st.Err()
		j.done <- got
	}()
	return j
}

// How a join can go: what the joining device holds, what it is given, and what the
// trusted side and the joining window do once the device has answered.
type joinCase struct {
	name string
	// paired: this device is paired with the bridge already, and device.toml names it.
	paired bool
	// uri is what the window pastes, from the trusted side's pairing.
	uri func(t *testing.T, p *trusted, relay devicev1connect.DeviceServiceClient) string
	// then is what happens once the digits show (nil: the join ends before they do).
	then func(t *testing.T, p *trusted, j *joining)
	// want is the code the join ends with (0: joined).
	want connect.Code
}

func asShown(t *testing.T, p *trusted, relay devicev1connect.DeviceServiceClient) string {
	return p.uri(t, relay)
}

var joinCases = []joinCase{
	{name: "confirmed", uri: asShown, then: func(t *testing.T, p *trusted, _ *joining) { p.answer(t, true) }},
	{
		name: "refused", uri: asShown, want: connect.CodeFailedPrecondition,
		then: func(t *testing.T, p *trusted, _ *joining) { p.answer(t, false) },
	},
	{
		name: "the trusted side going away", uri: asShown, want: connect.CodeFailedPrecondition,
		then: func(_ *testing.T, p *trusted, _ *joining) { p.leave() },
	},
	{
		name: "the window going away midway", uri: asShown, want: connect.CodeCanceled,
		then: func(_ *testing.T, _ *trusted, j *joining) { j.cut() },
	},
	{name: "joined there already", paired: true, uri: asShown, want: connect.CodeAlreadyExists},
	{
		name: "a URI that does not read", want: connect.CodeInvalidArgument,
		uri: func(t *testing.T, p *trusted, relay devicev1connect.DeviceServiceClient) string {
			return p.uri(t, relay)[:40]
		},
	},
	{
		name: "another bridge's ID", want: connect.CodeUnavailable,
		uri: func(t *testing.T, p *trusted, _ devicev1connect.DeviceServiceClient) string {
			token, err := pairing.ParseToken(p.ticket.GetToken())
			if err != nil {
				t.Fatal(err)
			}
			return pairing.Invite{Addresses: p.ticket.GetAddresses(), ID: idOf(t, newKey(t)), Token: token, Expires: time.Now().Add(time.Hour)}.URI()
		},
	},
	{
		// No pairing open: the bridge refuses an unpaired key in the handshake.
		name: "a pairing ended before", want: connect.CodeUnavailable,
		uri: func(t *testing.T, p *trusted, relay devicev1connect.DeviceServiceClient) string {
			uri := p.uri(t, relay)
			p.answer(t, false) // refused before anyone answered: it ends at once
			within(t, 5*time.Second, "the pairing ending", func() v1.PairingOutcome { return <-p.outcome })
			return uri
		},
	},
	{
		// Another pairing open: the key is let in to answer, and the token is refused.
		name: "a pairing ended while another is open", want: connect.CodeNotFound,
		uri: func(t *testing.T, p *trusted, relay devicev1connect.DeviceServiceClient) string {
			uri := p.uri(t, relay)
			_ = startPairing(t, p.bridge)
			p.answer(t, false)
			within(t, 5*time.Second, "the pairing ending", func() v1.PairingOutcome { return <-p.outcome })
			return uri
		},
	},
}

// A desktop joining a bridge does what `kith join` does, and nothing else: the device
// file names the bridge only once the trusted device confirmed the digits both sides
// show; however else the join ends (refused, either side going away, a URI that does not
// read or names another key, a pairing ended, this device paired already), the device
// file is as it was, byte for byte, and the bridge has paired nothing.
func TestAJoinWritesTheBridgeOnlyOnceConfirmed(t *testing.T) {
	t.Parallel()
	for _, c := range joinCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path, laptop := clientDevice(t)
			var b *bridgeKithd
			before := "[display]\nfps = 30\n"
			if c.paired {
				b = serveBridge(t, laptop)
				before = "[[bridge]]\nname = \"home\"\nid = \"" + b.id.String() + "\"\naddresses = [\"" + b.addr + "\"]\n"
			} else {
				b = serveBridge(t)
			}
			handWrite(t, path, before)
			relay := deviceClient(relayTo(t, local(filepath.Join(t.TempDir(), "none.sock")), path))
			p := startPairing(t, b)
			j := join(t, relay, c.uri(t, p, relay))
			if c.then != nil {
				shown := within(t, 5*time.Second, "the joining device's digits", func() string { return <-j.digits })
				pending := within(t, 5*time.Second, "the device pending", func() *v1.PendingDevice { return <-p.pending })
				if pending.GetDigits() != shown || pending.GetId() != idOf(t, laptop).String() || pending.GetName() != "laptop" {
					t.Fatalf("the trusted side shows %v, the joining one %s", pending, shown)
				}
				c.then(t, p, j)
			}
			got := within(t, 10*time.Second, "the join ending", func() joined { return <-j.done })
			if code := connect.CodeOf(got.err); got.err != nil && code != c.want || got.err == nil && c.want != 0 {
				t.Fatalf("the join ended with %v, want code %v", got.err, c.want)
			}
			after, err := os.ReadFile(config.DevicePath(path)) // #nosec G304 -- the test's own file
			if err != nil {
				t.Fatal(err)
			}
			_, listed, err := b.paired.List()
			if err != nil {
				t.Fatal(err)
			}
			isPaired := slices.ContainsFunc(listed, func(d domain.Device) bool { return d.ID == idOf(t, laptop).String() })
			if c.want != 0 {
				if string(after) != before {
					t.Errorf("an unconfirmed join wrote the device file:\n%s", after)
				}
				if isPaired != c.paired {
					t.Errorf("paired at the bridge: %v, want %v", isPaired, c.paired)
				}
				return
			}
			dev, err := config.LoadDevice(config.DevicePath(path))
			if err != nil {
				t.Fatal(err)
			}
			host, _, _ := net.SplitHostPort(b.addr)
			want := config.Bridge{Name: host, Addresses: []string{b.addr}, ID: b.id.String()}
			if len(dev.Bridges) != 1 || !slices.Equal(dev.Bridges[0].Addresses, want.Addresses) ||
				dev.Bridges[0].ID != want.ID || dev.Bridges[0].Name != want.Name || dev.Display.FPS != 30 {
				t.Errorf("device file %+v, want the bridge %+v beside fps 30", dev, want)
			}
			if got.bridge.GetName() != want.Name || got.bridge.GetId() != want.ID {
				t.Errorf("the window was told %v joined, want %+v", got.bridge, want)
			}
			if !isPaired {
				t.Error("joined, but the bridge has not paired this device")
			}
		})
	}
}

// A bridge with the pinned key that shows other digits than this device works out (one
// answering for whoever used the QR code first) is refused, as `kith join` refuses it:
// the window is shown no digits, the pairing ends, and the device file is untouched.
func TestABridgeShowingOtherDigitsIsRefused(t *testing.T) {
	t.Parallel()
	path, _ := clientDevice(t)
	before := "[display]\nfps = 30\n"
	handWrite(t, path, before)
	key := newKey(t)
	ended := make(chan struct{})
	addr := serveOtherDigits(t, key, ended)
	relay := deviceClient(relayTo(t, local(filepath.Join(t.TempDir(), "none.sock")), path))
	uri := pairing.Invite{Addresses: []string{addr}, ID: idOf(t, key), Token: pairing.NewToken(), Expires: time.Now().Add(time.Hour)}.URI()
	j := join(t, relay, uri)
	got := within(t, 10*time.Second, "the join ending", func() joined { return <-j.done })
	if connect.CodeOf(got.err) != connect.CodeFailedPrecondition {
		t.Fatalf("the join ended with %v, want a failed precondition", got.err)
	}
	select {
	case d := <-j.digits:
		t.Errorf("the window was shown %s", d)
	default:
	}
	within(t, 5*time.Second, "the bridge's pairing ending", func() struct{} { return <-ended })
	if after, err := os.ReadFile(config.DevicePath(path)); err != nil || string(after) != before { // #nosec G304 -- the test's own file
		t.Errorf("the device file is %q, %v; want it untouched", after, err)
	}
}

// otherDigits answers a pairing with digits no device works out, and says when the
// joining device has gone.
type otherDigits struct {
	backendv1connect.UnimplementedBackendServiceHandler
	ended chan struct{}
}

func (o otherDigits) Pair(ctx context.Context, _ *connect.Request[v1.PairRequest], st *connect.ServerStream[v1.PairResponse]) error {
	if err := st.Send(&v1.PairResponse{Event: &v1.PairResponse_Digits{Digits: "000000"}}); err != nil {
		return err
	}
	<-ctx.Done()
	close(o.ended)
	return nil
}

// serveOtherDigits serves otherDigits over TLS with key, as a bridge does: HTTP/2, any
// client certificate asked for.
func serveOtherDigits(t *testing.T, key *ecdsa.PrivateKey, ended chan struct{}) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{certOf(t, key)}, ClientAuth: tls.RequireAnyClientCert,
		MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(backendv1connect.NewBackendServiceHandler(otherDigits{ended: ended}))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second, Protocols: new(http.Protocols)}
	srv.Protocols.SetHTTP2(true)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// Main is told the key this device dials its bridge with, and that there is none for a
// kithd of this machine; the URI a window pastes reads as kith join reads it.
func TestTheRelayNamesThisDeviceAndReadsAnInvite(t *testing.T) {
	t.Parallel()
	laptop := newKey(t)
	b := serveBridge(t, laptop)
	relay := deviceClient(viaBridge(t, laptop, b.id, b.addr))
	res, err := relay.ThisDevice(t.Context(), connect.NewRequest(&devicev1.ThisDeviceRequest{}))
	if err != nil || res.Msg.GetId() != idOf(t, laptop).String() {
		t.Errorf("ThisDevice = %v, %v; want %s", res, err, idOf(t, laptop))
	}
	here := deviceClient(relayTo(t, local(filepath.Join(t.TempDir(), "none.sock")), filepath.Join(t.TempDir(), "config.toml")))
	if mine, herr := here.ThisDevice(t.Context(), connect.NewRequest(&devicev1.ThisDeviceRequest{})); herr != nil || mine.Msg.GetId() != "" {
		t.Errorf("ThisDevice on kithd's machine = %v, %v; want none", mine, herr)
	}

	p := startPairing(t, b)
	invite, err := relay.Invite(t.Context(), connect.NewRequest(&devicev1.InviteRequest{Uri: p.uri(t, relay)}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(invite.Msg.GetAddresses(), p.ticket.GetAddresses()) || invite.Msg.GetId() != b.id.String() ||
		!invite.Msg.GetExpires().AsTime().Equal(p.ticket.GetExpires().AsTime().Truncate(time.Second)) {
		t.Errorf("the invite reads %v, want the ticket %v", invite.Msg, p.ticket)
	}
	for _, bad := range []string{"", "kith://pair?addr=x", "https://example.org/", "kith://pair?addr=192.168.1.5%3A7443&id=x&token=y&exp=1"} {
		if _, err := relay.Invite(t.Context(), connect.NewRequest(&devicev1.InviteRequest{Uri: bad})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("Invite(%q) = %v, want invalid", bad, err)
		}
	}
	if _, err := relay.PairingURI(t.Context(), connect.NewRequest(&devicev1.PairingURIRequest{Token: "x", Id: b.id.String()})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("PairingURI of a bad token = %v, want invalid", err)
	}
}
