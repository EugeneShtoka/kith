// Command kith-mailbox is a mailbox for a kith mesh (planning/design-mailbox.md): a store
// the mesh's bridges dial, leave their changes with, sealed, and pick each other's up
// from, so bridges that are never on at the same time still converge. It holds no key
// that opens what it keeps.
//
//	kith-mailbox serve [--dir dir] [--listen addr]...
//	kith-mailbox pair --addr host:port [--addr host:port]... [--dir dir]
package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/adrg/xdg"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/api/mesh/v1/meshv1connect"
	"github.com/EugeneShtoka/kith/internal/buildinfo"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/human"
	"github.com/EugeneShtoka/kith/internal/mailbox"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

const usage = `usage:
  kith-mailbox serve [--dir dir] [--listen addr]...   run the mailbox
  kith-mailbox pair --addr host:port... [--dir dir]   admit a bridge: run ` + "`kith mailbox add`" + ` there with the line it shows
  kith-mailbox --version`

// The pace of a pairing: how long it stays open, and how often each side looks at what
// the other wrote of it.
const (
	pairingFor  = 10 * time.Minute
	pairingPoll = 250 * time.Millisecond
	// handshakeTimeout bounds a TLS handshake, and each request's headers.
	handshakeTimeout = 10 * time.Second
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String("kith-mailbox"))
		return
	}
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "serve":
		err = runServe(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "pair":
		err = runPair(os.Args[2:])
	default:
		err = errors.New(usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kith-mailbox:", err)
		os.Exit(1)
	}
}

// mailboxDir is where the mailbox keeps its key and store: dir, or
// $XDG_DATA_HOME/kith-mailbox.
func mailboxDir(dir string) (string, error) {
	if dir == "" {
		dir = filepath.Join(xdg.DataHome, "kith-mailbox")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("the mailbox's folder: %w", err)
	}
	return dir, nil
}

// opened is the mailbox's key, its certificate and ID, and its store.
type opened struct {
	cert  tls.Certificate
	self  devicekey.ID
	store *mailbox.Store
}

// open reads (making it the first time) the mailbox's device key in dir, and opens its
// store, sealed under a key derived from it.
func open(ctx context.Context, dir string) (opened, error) {
	path := filepath.Join(dir, "key.pem")
	text, err := os.ReadFile(path) // #nosec G304 -- the mailbox's own folder
	var key *ecdsa.PrivateKey
	switch {
	case errors.Is(err, os.ErrNotExist):
		if key, err = devicekey.Generate(); err == nil {
			if text, err = devicekey.Marshal(key); err == nil {
				err = os.WriteFile(path, text, 0o600)
			}
		}
	case err == nil:
		key, err = devicekey.Parse(text)
	}
	if err != nil {
		return opened{}, fmt.Errorf("the mailbox's key %s: %w", path, err)
	}
	cert, err := devicekey.Certificate(key)
	if err != nil {
		return opened{}, fmt.Errorf("the mailbox's key: %w", err)
	}
	self, err := devicekey.IDOf(key.Public())
	if err != nil {
		return opened{}, fmt.Errorf("the mailbox's key: %w", err)
	}
	storeKey := sha256.Sum256(append([]byte("kith-mailbox store\n"), text...))
	store, err := mailbox.Open(ctx, filepath.Join(dir, "mailbox.db"), storeKey[:])
	if err != nil {
		return opened{}, fmt.Errorf("the mailbox's store: %w", err)
	}
	return opened{cert: cert, self: self, store: store}, nil
}

// addrs is a flag given once per address.
type addrs []string

func (a *addrs) String() string     { return strings.Join(*a, ",") }
func (a *addrs) Set(v string) error { *a = append(*a, v); return nil }

// runServe is `kith-mailbox serve`: the mailbox, over mutual TLS on each --listen
// address, to the bridges it admits, until it is stopped.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dirFlag := fs.String("dir", "", "where the mailbox keeps its key and store (default $XDG_DATA_HOME/kith-mailbox)")
	var listen addrs
	fs.Var(&listen, "listen", "an address to listen on, host:port (default :7444); give it again for more")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New(usage)
	}
	if len(listen) == 0 {
		listen = addrs{":7444"}
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := mailboxDir(*dirFlag)
	if err != nil {
		return err
	}
	o, err := open(ctx, dir)
	if err != nil {
		return err
	}
	defer func() { _ = o.store.Close() }()
	var lns []net.Listener
	for _, addr := range listen {
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", addr, err)
		}
		log.Info("the mailbox listens", "addr", ln.Addr().String(), "id", o.self.String())
		lns = append(lns, ln)
	}
	return serve(ctx, o, lns, log)
}

// serve serves the mailbox o on lns until ctx ends or a listener fails.
func serve(ctx context.Context, o opened, lns []net.Listener, log *slog.Logger) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	mux := http.NewServeMux()
	mux.Handle(meshv1connect.NewMeshServiceHandler(meshService{o: o}))
	mux.Handle(backendv1connect.NewBackendServiceHandler(pairService{o: o}))
	srv := &http.Server{
		Handler:           withPeer(mux),
		TLSConfig:         tlsConfig(ctx, o),
		ReadHeaderTimeout: handshakeTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	var serving sync.WaitGroup
	failed := make(chan error, len(lns))
	var err error
	for _, ln := range lns {
		serving.Go(func() {
			if serr := srv.ServeTLS(ln, "", ""); !errors.Is(serr, http.ErrServerClosed) {
				failed <- serr
			}
		})
	}
	serving.Go(func() { o.store.Compacting(ctx, log) })
	select {
	case <-ctx.Done():
	case err = <-failed:
	}
	stop()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx) // the listeners close either way; a stream still open ends with the process
	serving.Wait()
	return err
}

// tlsConfig is mutual TLS with the peer's key pinned: any certificate is asked for and
// none checked against a CA. A key the mailbox admits is let in; while a pairing is
// open, any key is, to answer it (pairService), and only an admitted one may sync
// (meshService).
func tlsConfig(ctx context.Context, o opened) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		Certificates:           []tls.Certificate{o.cert},
		NextProtos:             []string{"h2", "http/1.1"},
		ClientAuth:             tls.RequireAnyClientCert,
		SessionTicketsDisabled: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			id, err := devicekey.PeerID(state)
			if err != nil {
				return fmt.Errorf("kith-mailbox: %w", err)
			}
			if !o.store.Admits(id) && !o.store.PairingOpen(ctx, time.Now()) {
				return errors.New("kith-mailbox: a key this mailbox does not admit")
			}
			return nil
		},
	}
}

// peerKey keys the device ID a request's TLS handshake showed.
type peerKey struct{}

// withPeer puts the device ID of the request's peer in its context.
func withPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			if id, err := devicekey.PeerID(*r.TLS); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), peerKey{}, id))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// peerOf is the device ID of the peer a request came from.
func peerOf(ctx context.Context) (devicekey.ID, error) {
	id, ok := ctx.Value(peerKey{}).(devicekey.ID)
	if !ok {
		return id, connect.NewError(connect.CodeUnauthenticated, errors.New("kith-mailbox: no device key shown"))
	}
	return id, nil
}

// meshService is MeshService.Sync for the bridges the mailbox admits.
type meshService struct {
	o opened
}

var _ meshv1connect.MeshServiceHandler = meshService{}

func (m meshService) Sync(ctx context.Context, st *connect.BidiStream[meshv1.SyncRequest, meshv1.SyncResponse]) error {
	peer, err := peerOf(ctx)
	if err != nil {
		return err
	}
	if !m.o.store.Admits(peer) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("kith-mailbox: a key this mailbox does not admit"))
	}
	if err := m.o.store.Serve(ctx, m.o.self, peer, frames{st}, time.Now); err != nil {
		return connect.NewError(connect.CodeAborted, err)
	}
	return nil
}

// frames is a sync stream as the mailbox's frames.
type frames struct {
	st *connect.BidiStream[meshv1.SyncRequest, meshv1.SyncResponse]
}

func (f frames) Send(frame *meshv1.SyncFrame) error {
	if err := f.st.Send(&meshv1.SyncResponse{Frame: frame}); err != nil {
		return fmt.Errorf("kith-mailbox: send a sync frame: %w", err)
	}
	return nil
}

// Receive reads the next frame; io.EOF (wrapped) once the bridge closed.
func (f frames) Receive() (*meshv1.SyncFrame, error) {
	r, err := f.st.Receive()
	if err != nil {
		return nil, fmt.Errorf("kith-mailbox: read a sync frame: %w", err)
	}
	return r.GetFrame(), nil
}

// pairService answers a bridge's kithd pairing with the mailbox (kith mailbox add) as a
// bridge answers a device's: BackendService.Pair, the one call of it a mailbox serves.
type pairService struct {
	backendv1connect.UnimplementedBackendServiceHandler
	o opened
}

// Pair takes the pairing open under the request's token for the key that sent it: the
// six digits both sides work out, then, once the person at the mailbox answers (kith-
// mailbox pair), the outcome; a pairing accepted admits the key.
func (p pairService) Pair(ctx context.Context, r *connect.Request[v1.PairRequest], st *connect.ServerStream[v1.PairResponse]) error {
	peer, err := peerOf(ctx)
	if err != nil {
		return err
	}
	tok, err := pairing.ParseToken(r.Msg.GetToken())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err = p.o.store.AnswerPairing(ctx, tok.String(), peer, time.Now()); err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}
	defer func() { _ = p.o.store.ClosePairing(context.WithoutCancel(ctx), tok.String()) }()
	if err = st.Send(&v1.PairResponse{Event: &v1.PairResponse_Digits{Digits: pairing.Digits(p.o.self, peer, tok)}}); err != nil {
		return fmt.Errorf("kith-mailbox: send the digits: %w", err)
	}
	outcome, err := p.await(ctx, tok.String(), peer)
	if err != nil {
		return err
	}
	if err := st.Send(&v1.PairResponse{Event: &v1.PairResponse_Outcome{Outcome: outcome}}); err != nil {
		return fmt.Errorf("kith-mailbox: send the outcome: %w", err)
	}
	return nil
}

// await waits for the verdict on the pairing under token, answered by peer: accepted
// admits peer.
func (p pairService) await(ctx context.Context, token string, peer devicekey.ID) (v1.PairingOutcome, error) {
	ticker := time.NewTicker(pairingPoll)
	defer ticker.Stop()
	for {
		got, err := p.o.store.PairingAt(ctx, token)
		switch {
		case err != nil:
			return 0, connect.NewError(connect.CodeNotFound, err)
		case got.Verdict == mailbox.Accepted:
			if err := p.o.store.Admit(ctx, peer); err != nil {
				return 0, connect.NewError(connect.CodeInternal, err)
			}
			return v1.PairingOutcome_PAIRING_OUTCOME_CONFIRMED, nil
		case got.Verdict == mailbox.Refused:
			return v1.PairingOutcome_PAIRING_OUTCOME_REFUSED, nil
		case time.Now().After(got.Expires):
			return v1.PairingOutcome_PAIRING_OUTCOME_EXPIRED, nil
		}
		select {
		case <-ctx.Done():
			return 0, connect.NewError(connect.CodeCanceled, ctx.Err())
		case <-ticker.C:
		}
	}
}

// runPair is `kith-mailbox pair`: it opens a pairing for --addr (where bridges dial
// the mailbox), shows the line to give `kith mailbox add` on the bridge, then the
// digits once the bridge answers, and asks whether the bridge shows the same.
func runPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	dirFlag := fs.String("dir", "", "where the mailbox keeps its key and store (default $XDG_DATA_HOME/kith-mailbox)")
	var at addrs
	fs.Var(&at, "addr", "where bridges dial this mailbox, host:port; give it again for more")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || len(at) == 0 {
		return errors.New(usage)
	}
	for _, a := range at {
		if err := pairing.CheckAddress(a); err != nil {
			return fmt.Errorf("--addr: %w", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := mailboxDir(*dirFlag)
	if err != nil {
		return err
	}
	o, err := open(ctx, dir)
	if err != nil {
		return err
	}
	defer func() { _ = o.store.Close() }()
	return pair(ctx, o, at, os.Stdin, os.Stdout)
}

// pair opens a pairing on o for bridges dialing at, and asks on in, telling out.
func pair(ctx context.Context, o opened, at []string, in io.Reader, out io.Writer) error {
	inv := pairing.Invite{Addresses: at, ID: o.self, Token: pairing.NewToken(), Expires: time.Now().Add(pairingFor)}
	token := inv.Token.String()
	if err := o.store.OpenPairing(ctx, token, inv.Expires); err != nil {
		return fmt.Errorf("open a pairing: %w", err)
	}
	defer func() { _ = o.store.ClosePairing(context.WithoutCancel(ctx), token) }()
	_, _ = fmt.Fprintf(out, "On the bridge to admit, run (within %s, with kith-mailbox serve running here):\n\n  kith mailbox add '%s'\n\n", human.Duration(pairingFor), inv.URI())
	ticker := time.NewTicker(pairingPoll)
	defer ticker.Stop()
	for {
		got, err := o.store.PairingAt(ctx, token)
		switch {
		case err != nil:
			return fmt.Errorf("the pairing: %w", err)
		case got.Answered:
			return decide(ctx, o, token, got.Device, pairing.Digits(o.self, got.Device, inv.Token), in, out)
		case time.Now().After(got.Expires):
			return errors.New("the pairing expired: no bridge answered it")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the pairing: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// decide asks whether the bridge device, which answered the pairing under token, shows
// digits too, and records the answer.
func decide(ctx context.Context, o opened, token string, device devicekey.ID, digits string, in io.Reader, out io.Writer) error {
	_, _ = fmt.Fprintf(out, "Bridge %s answered. This mailbox shows %s.\nDoes the bridge show the same digits? [y/N] ", device, digits)
	line, _ := bufio.NewReader(in).ReadString('\n') // no answer is no
	verdict := mailbox.Refused
	if answer := strings.ToLower(strings.TrimSpace(line)); answer == "y" || answer == "yes" {
		verdict = mailbox.Accepted
	}
	if err := o.store.DecidePairing(ctx, token, verdict); err != nil {
		return fmt.Errorf("the pairing: %w", err)
	}
	if verdict == mailbox.Refused {
		return errors.New("refused: the bridge was not admitted")
	}
	// The listener admits it once it reads the verdict, and drops the pairing.
	deadline := time.Now().Add(10 * pairingPoll)
	for time.Now().Before(deadline) {
		if _, err := o.store.PairingAt(ctx, token); errors.Is(err, mailbox.ErrNoPairing) {
			break
		}
		time.Sleep(pairingPoll)
	}
	_, _ = fmt.Fprintf(out, "Admitted %s. The other bridges of its mesh are admitted as it tells the mailbox of them.\n", device)
	return nil
}
