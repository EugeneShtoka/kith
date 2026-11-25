package daemon

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"

	backendv1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/config"
)

// Upstream is where a forwarder (kith-relay, kith-proxy) reaches kithd: the local one's
// socket, kith-proxy's, or the bridge this device joined, over pinned mutual TLS with
// this device's key (DialBridge).
type Upstream struct {
	Client *http.Client
	// Scheme is what Client is asked for: TLS to a bridge, plain over a socket.
	Scheme string
	// Bridge is the joined bridge's name, reached directly or through kith-proxy; "" for
	// the local kithd.
	Bridge string
	// refused is the bridge having refused this device's key, until it answers again. In
	// TLS 1.3 a server judges the client's key once the client's handshake is done, so
	// its refusal is an alert where the first answer would come; a request can meet the
	// connection reset first instead, and a bridge bounds the handshakes each address may
	// start, dropping the rest before it reads the key. Neither says anything of the key,
	// so the client is told the last verdict. Nil for the local kithd.
	refused *atomic.Bool
}

// LocalUpstream is the kithd whose socket is socket.
func LocalUpstream(socket string) Upstream {
	return Upstream{Client: Dial(socket), Scheme: "http"}
}

// ProxyUpstream is kith-proxy's socket, which reaches the bridge named bridge: a refusal
// of this device's key comes from it as an answer, kept as it is.
func ProxyUpstream(socket, bridge string) Upstream {
	return Upstream{Client: Dial(socket), Scheme: "http", Bridge: bridge, refused: new(atomic.Bool)}
}

// BridgeUpstream is the bridge b, reached as the device whose certificate is cert.
func BridgeUpstream(cert tls.Certificate, b config.Bridge) (Upstream, error) {
	client, err := DialBridge(cert, b)
	if err != nil {
		return Upstream{}, err
	}
	refused := new(atomic.Bool)
	client.Transport = heard{RoundTripper: client.Transport, refused: refused}
	return Upstream{Client: client, Scheme: "https", Bridge: b.Name, refused: refused}, nil
}

// heard forgets a bridge's refusal once it answers: an answer comes only through a
// handshake that took this device's key.
type heard struct {
	http.RoundTripper
	refused *atomic.Bool
}

func (h heard) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := h.RoundTripper.RoundTrip(r)
	if err != nil {
		return nil, fmt.Errorf("reaching the bridge: %w", err)
	}
	h.refused.Store(false)
	return res, nil
}

// Failure is err, met reaching kithd, as a client reads it: kithd's own answer keeps
// its code; a bridge refusing this device's key (a TLS alert from it), now or last time
// and not answering since, is permission denied, saying so; anything else is kithd not
// reachable, unavailable, which the client tries again.
func (u Upstream) Failure(err error) *connect.Error {
	var alert *net.OpError
	if u.Bridge != "" && (u.refused.Load() || errors.As(err, &alert) && alert.Op == "remote error") {
		u.refused.Store(true)
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"bridge %s refused this device's key; it is not paired there (kith pair there, kith join here): %w", u.Bridge, err))
	}
	if ce := new(connect.Error); errors.As(err, &ce) {
		return ce
	}
	return connect.NewError(connect.CodeUnavailable, err)
}

// HandleBackend routes each of BackendService's methods on mux to h. Each is a path of
// its own, so anything else (another service, a path that cleans to one) is left to
// mux, and a forwarder lends its reach to nothing else of kithd's.
func HandleBackend(mux *http.ServeMux, h http.Handler) {
	methods := backendv1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods()
	for i := range methods.Len() {
		mux.Handle("POST /"+backendv1connect.BackendServiceName+"/"+string(methods.Get(i).Name()), h)
	}
}

// Forward proxies to kithd at u, flushing each stream frame as it comes: a client's
// streams are live. kithd unreachable is answered as Failure reads it, in the protocol
// the client asked in; kithd gone mid-stream, or a bridge's connection found dead by
// its PING, cuts the client's stream, which it reopens.
func (u Upstream) Forward(log *slog.Logger) http.Handler {
	errs := connect.NewErrorWriter()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = u.Scheme
			r.Out.URL.Host = "kithd"
		},
		Transport:     u.Client.Transport,
		FlushInterval: -1,
		ErrorLog:      slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("kithd did not answer", "method", r.URL.Path, "err", err)
			if werr := errs.Write(w, r, u.Failure(err)); werr != nil {
				w.WriteHeader(http.StatusBadGateway)
			}
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// kithd answers a stream with its headers as it opens, which can be before the
		// client's request is all read. An HTTP/1.1 server otherwise consumes the rest of
		// a request's body before it writes the response: the headers would wait on the
		// request's end, and that read races the copy forwarding the body to kithd, which
		// then cuts the stream.
		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
			log.Warn("cannot read a request while answering it; a stream may be cut", "err", err)
		}
		body := &heldBody{body: r.Body}
		r.Body = body
		defer body.end()
		proxy.ServeHTTP(w, r)
	})
}

// errHandled is a request's body read after its handler returned.
var errHandled = errors.New("daemon: the request was already answered")

// heldBody is a request's body read only while its handler runs, and to its end. A
// full-duplex server whose handler left the body unread reads it to its end itself, as
// it closes it once the handler returned, and reaching the end starts its own read of
// the connection while it already reads the next request, which it does not survive. A
// round trip that failed before sending the body (a bridge refusing the connection)
// leaves it unread, and a transport may go on reading it after its round trip ended
// (net/http allows it).
type heldBody struct {
	mu    sync.Mutex
	body  io.ReadCloser
	ended bool
}

func (b *heldBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ended {
		return 0, errHandled
	}
	n, err := b.body.Read(p)
	switch {
	case err == nil:
		return n, nil
	case errors.Is(err, io.EOF):
		return n, io.EOF
	default:
		return n, fmt.Errorf("reading the request: %w", err)
	}
}

func (b *heldBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ended {
		return nil // the server closes the body itself
	}
	if err := b.body.Close(); err != nil {
		return fmt.Errorf("closing the request: %w", err)
	}
	return nil
}

// unreadBound is how much of a request end reads that the transport left unread, as the
// server itself would after its handler: a client's requests are one message each.
const unreadBound = 256 << 10

// end waits out a read in progress and reads what is left of the request; no read
// after it reaches the request.
func (b *heldBody) end() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ended = true
	_, _ = io.CopyN(io.Discard, b.body, unreadBound) // its error is the request's end, or a cut the server sees itself
}
