package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/api/mesh/v1/meshv1connect"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh"
	"github.com/EugeneShtoka/kith/internal/pairing"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// A bridge pairs with a running mailbox by its token and the digits the person there
// confirms; then two bridges of one mesh that never sync with each other again hand a
// read across through it, over mutual TLS with the mailbox's key pinned. The second is
// admitted by the first's word, never paired with the mailbox itself.
func TestTwoBridgesMeetThroughARunningMailbox(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	log := slog.New(slog.DiscardHandler)
	o, err := open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.store.Close() })
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() { _ = serve(ctx, o, []net.Listener{ln}, log) }()

	a, b := newBridge(t, "a"), newBridge(t, "b")
	a.join(t, b)

	// The person at the mailbox pairs a: they say yes to the digits.
	out, written := io.Pipe()
	paired := make(chan error, 1)
	go func() { paired <- pair(ctx, o, []string{addr}, strings.NewReader("y\n"), written) }()
	inv := readInvite(t, out)
	go func() { _, _ = io.Copy(io.Discard, out) }()
	digits, outcome := a.pair(t, inv)
	if want := pairing.Digits(o.self, a.id, inv.Token); digits != want || outcome != v1.PairingOutcome_PAIRING_OUTCOME_CONFIRMED {
		t.Fatalf("the pairing: digits %s (want %s), %v", digits, want, outcome)
	}
	if err := <-paired; err != nil {
		t.Fatal(err)
	}
	for _, br := range []*bridge{a, b} {
		if err := br.store.AddMailbox(ctx, br.key, domain.Device{ID: o.self.String(), Name: "mailbox"}, []string{addr}); err != nil {
			t.Fatal(err)
		}
	}
	read := domain.ReadPosition{Room: "telegram:200/300", Event: "e1", At: time.Now().UnixMilli()}
	if err := a.store.Read(ctx, a.key, []domain.ReadPosition{read}); err != nil {
		t.Fatal(err)
	}
	stopA := a.post(t, o.self, addr)
	defer stopA()
	waitFor(t, "b admitted by a's word", func() bool { return o.store.Admits(b.id) })
	stopB := b.post(t, o.self, addr)
	defer stopB()
	waitFor(t, "a's read at b", func() bool { return slices.Contains(b.store.Reads(), read) })
}

// The mailbox keeps its key: opened again in the same folder, it has the same ID, and
// its store opens under the key derived from it.
func TestTheMailboxKeepsItsKeyAcrossARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := first.store.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	again, err := open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.store.Close() })
	if again.self != first.self {
		t.Errorf("the mailbox is %s after a restart, was %s", again.self, first.self)
	}
}

// kith-mailbox pair asks for where bridges dial it, and refuses an address they cannot.
func TestPairRefusesAnAddressNoBridgeCanDial(t *testing.T) {
	t.Parallel()
	if err := runPair(nil); err == nil || err.Error() != usage {
		t.Errorf("pair with no --addr: %v, want the usage", err)
	}
	if err := runPair([]string{"--addr", "mailbox.example"}); err == nil || !strings.HasPrefix(err.Error(), "--addr: ") {
		t.Errorf("pair --addr with no port: %v, want it refused", err)
	}
}

// bridge is a kithd's mesh store and key.
type bridge struct {
	key   *ecdsa.PrivateKey
	id    devicekey.ID
	cert  tls.Certificate
	store *mesh.Store
}

func newBridge(t *testing.T, name string) *bridge {
	t.Helper()
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := devicekey.Certificate(key)
	if err != nil {
		t.Fatal(err)
	}
	dbKey := make([]byte, sealeddb.KeyLen)
	_, _ = rand.Read(dbKey)
	store, err := mesh.Open(t.Context(), filepath.Join(t.TempDir(), name+".db"), dbKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.ChangeRoster(t.Context(), key, []domain.Device{{ID: id.String(), Name: name, Role: domain.RoleBridge}}, nil); err != nil {
		t.Fatal(err)
	}
	return &bridge{key: key, id: id, cert: cert, store: store}
}

// join has other join the mesh of br, as kith join --bridge does once paired.
func (br *bridge) join(t *testing.T, other *bridge) {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(other.key.Public())
	if err != nil {
		t.Fatal(err)
	}
	if err := br.store.Bridge(t.Context(), br.key, domain.Device{ID: other.id.String(), Name: "b"}, der, other.store.Sealing(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ab, ba := make(chan *meshv1.SyncFrame, 64), make(chan *meshv1.SyncFrame, 64)
	go func() {
		_ = br.store.Sync(ctx, br.key, other.id, false, chans{in: ba, out: ab, ctx: ctx}, slog.New(slog.DiscardHandler))
	}()
	if _, err := other.store.Join(ctx, other.id, br.id, other.key, chans{in: ab, out: ba, ctx: ctx}); err != nil {
		t.Fatal(err)
	}
}

// client is an HTTP client presenting br's key to the mailbox whose key is pin.
func (br *bridge) client(pin devicekey.ID) *http.Client {
	return &http.Client{Transport: &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{br.cert}, NextProtos: []string{"h2"},
			InsecureSkipVerify: true, // #nosec G402 -- pinned below
			VerifyConnection: func(state tls.ConnectionState) error {
				if id, err := devicekey.PeerID(state); err != nil || id != pin {
					return errors.New("not the mailbox")
				}
				return nil
			},
		},
	}}
}

// pair answers inv as kithd does for kith mailbox add: the digits, then the outcome.
func (br *bridge) pair(t *testing.T, inv pairing.Invite) (string, v1.PairingOutcome) {
	t.Helper()
	c := backendv1connect.NewBackendServiceClient(br.client(inv.ID), "https://"+inv.Addresses[0])
	st, err := c.Pair(t.Context(), connect.NewRequest(&v1.PairRequest{Token: inv.Token.String(), Name: "a"}))
	if err != nil {
		t.Fatal(err)
	}
	var digits string
	for st.Receive() {
		switch ev := st.Msg().GetEvent().(type) {
		case *v1.PairResponse_Digits:
			digits = ev.Digits
		case *v1.PairResponse_Outcome:
			return digits, ev.Outcome
		}
	}
	t.Fatalf("the pairing ended: %v", st.Err())
	return "", 0
}

// post keeps br syncing with the mailbox at addr, dialing again while it is refused,
// until the returned stop.
func (br *bridge) post(t *testing.T, mailbox devicekey.ID, addr string) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			st := meshv1connect.NewMeshServiceClient(br.client(mailbox), "https://"+addr).Sync(ctx)
			_ = br.store.SyncMailbox(ctx, br.key, br.id, mailbox, streamFrames{st}, slog.New(slog.DiscardHandler))
			_ = st.CloseRequest()
			_ = st.CloseResponse()
			select {
			case <-ctx.Done():
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	return func() { cancel(); <-done }
}

// readInvite reads the line kith-mailbox pair shows for kith mailbox add.
func readInvite(t *testing.T, out io.Reader) pairing.Invite {
	t.Helper()
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		if uri, ok := strings.CutPrefix(strings.TrimSpace(lines.Text()), "kith mailbox add '"); ok {
			inv, err := pairing.ParseURI(strings.TrimSuffix(uri, "'"))
			if err != nil {
				t.Fatal(err)
			}
			return inv
		}
	}
	t.Fatal("kith-mailbox pair showed no line to run")
	return pairing.Invite{}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("never: %s", what)
}

// chans is one side of an in-memory sync stream.
type chans struct {
	in, out chan *meshv1.SyncFrame
	ctx     context.Context
}

func (c chans) Send(f *meshv1.SyncFrame) error {
	select {
	case c.out <- f:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

func (c chans) Receive() (*meshv1.SyncFrame, error) {
	select {
	case f := <-c.in:
		return f, nil
	case <-c.ctx.Done():
		return nil, io.EOF
	}
}

// streamFrames is a bridge's sync stream with the mailbox as mesh frames.
type streamFrames struct {
	st *connect.BidiStreamForClient[meshv1.SyncRequest, meshv1.SyncResponse]
}

func (f streamFrames) Send(frame *meshv1.SyncFrame) error {
	return f.st.Send(&meshv1.SyncRequest{Frame: frame})
}

func (f streamFrames) Receive() (*meshv1.SyncFrame, error) {
	r, err := f.st.Receive()
	if err != nil {
		return nil, err
	}
	return r.GetFrame(), nil
}
