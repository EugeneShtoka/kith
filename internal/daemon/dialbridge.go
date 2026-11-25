package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// bridgeBaseURL addresses Connect requests to a bridge; the host is ignored, the
// addresses dialed are the bridge's.
const bridgeBaseURL = "https://kithd"

// bridgeDialTimeout bounds one address's dial and handshake, so an address that does
// not answer leaves time for the next.
const bridgeDialTimeout = 5 * time.Second

// bridgeIdle is how long a connection to a bridge may go without a frame before a PING
// asks whether it is still there, and bridgePingWait how long the answer may take: a
// laptop that changed networks finds its old connection dead within their sum, and its
// streams reopen on a new one (the message stream resumes where it stood).
const (
	bridgeIdle     = 15 * time.Second
	bridgePingWait = 10 * time.Second
)

// errNotTheBridge is a peer whose key is not the one pinned. A bridge that rotated its
// key answers on the old one for rotationGrace; past it, a device still pinning that
// key joins again.
var errNotTheBridge = errors.New("daemon: the address answered with another key than the bridge's " +
	"(if the bridge rotated its key over 30 days ago, `kith join` it again)")

// pinnedTLS is a device's half of the listener's mutual TLS: it presents cert and
// takes the peer only if its key's ID is pin, which it names as the server (so a
// bridge that rotated from it answers as it a while). No CA: the key is what is
// pinned, and VerifyConnection runs on a resumed session too.
func pinnedTLS(cert tls.Certificate, pin devicekey.ID) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		ServerName:         pin.ServerName(),
		Certificates:       []tls.Certificate{cert},
		NextProtos:         []string{"h2", "http/1.1"},
		InsecureSkipVerify: true, // #nosec G402 -- the key is pinned in VerifyConnection, not a CA's word
		VerifyConnection: func(state tls.ConnectionState) error {
			id, err := devicekey.PeerID(state)
			if err != nil {
				return fmt.Errorf("daemon: %w", err)
			}
			if id != pin {
				return fmt.Errorf("%w (%s, not %s)", errNotTheBridge, id, pin)
			}
			return nil
		},
	}
}

// dialBridge is an HTTP client reaching the bridge whose key is pin at the first of
// addrs, in order, that answers with that key, presenting cert.
func dialBridge(cert tls.Certificate, pin devicekey.ID, addrs []string) *http.Client {
	conf := pinnedTLS(cert, pin)
	return &http.Client{Transport: &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var errs []error
			for _, addr := range addrs {
				d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: bridgeDialTimeout}, Config: conf}
				c, err := d.DialContext(ctx, "tcp", addr)
				if err == nil {
					return c, nil
				}
				errs = append(errs, fmt.Errorf("%s: %w", addr, err))
				if ctx.Err() != nil {
					break
				}
			}
			return nil, fmt.Errorf("daemon: no address of the bridge answered: %w", errors.Join(errs...))
		},
		ForceAttemptHTTP2:     true,
		HTTP2:                 &http.HTTP2Config{SendPingTimeout: bridgeIdle, PingTimeout: bridgePingWait},
		ResponseHeaderTimeout: answerTimeout,
	}}
}

// NewBridgeRemote is a Remote for the bridge whose key's ID is pin, at the first of
// addrs that answers with it, as the device whose certificate is cert. It performs no
// I/O.
func NewBridgeRemote(cert tls.Certificate, pin devicekey.ID, addrs []string) *Remote {
	return newRemoteAt(dialBridge(cert, pin, addrs), bridgeBaseURL)
}

// AttachBridge returns a client of the bridge b once it reports ready, as the device whose
// certificate is cert: through kith-proxy when it answers on this machine (storage's),
// else b's addresses in order, the first answering with b's key. It never starts a
// daemon; one that does not answer is waited for until timeout.
func AttachBridge(ctx context.Context, storage domain.Storage, cert tls.Certificate, b config.Bridge, timeout time.Duration) (*Remote, error) {
	dial, err := BridgeDialer(ctx, storage, cert, b)
	if err != nil {
		return nil, err
	}
	r := dial()
	if err := waitReady(ctx, r, timeout); err != nil {
		return nil, fmt.Errorf("bridge %s: %w", b.Name, err)
	}
	return r, nil
}

// BridgeDialer makes Remotes for the bridge b ([[bridge]]) as the device whose certificate
// is cert: of kith-proxy's socket when it answers now (ProxyAnswers), else each new one
// as NewBridgeRemote makes it; an error when b's ID is not one.
func BridgeDialer(ctx context.Context, storage domain.Storage, cert tls.Certificate, b config.Bridge) (func() *Remote, error) {
	pin, err := bridgePin(b)
	if err != nil {
		return nil, err
	}
	if ProxyAnswers(ctx, storage) {
		return func() *Remote { return NewRemote(storage.ProxySocketPath()) }, nil
	}
	return func() *Remote { return NewBridgeRemote(cert, pin, b.Addresses) }, nil
}

// ProxyAnswers is whether kith-proxy listens on this machine for storage
// (storage.ProxySocketPath). A client of the bridge asks once, as it starts: a machine
// with no proxy running dials the bridge itself, as before there was one.
func ProxyAnswers(ctx context.Context, storage domain.Storage) bool {
	if storage.RuntimeDir == "" {
		return false
	}
	c, err := (&net.Dialer{Timeout: proxyDialTimeout}).DialContext(ctx, "unix", storage.ProxySocketPath())
	if err != nil {
		return false
	}
	_ = c.Close() // only its answering was asked
	return true
}

// proxyDialTimeout bounds asking whether kith-proxy answers: a local socket answers at
// once or not at all.
const proxyDialTimeout = time.Second

// DialBridge returns an HTTP client that reaches the bridge b ([[bridge]]) as the device
// whose certificate is cert, as a Remote for it does: b's addresses in order, the first
// answering with b's key, over HTTP/2 with an idle connection pinged. Requests go over
// TLS whatever host they name; an error when b's ID is not one. It performs no I/O.
func DialBridge(cert tls.Certificate, b config.Bridge) (*http.Client, error) {
	pin, err := bridgePin(b)
	if err != nil {
		return nil, err
	}
	return dialBridge(cert, pin, b.Addresses), nil
}

// bridgePin is the ID of b's key, which every connection to b is pinned to.
func bridgePin(b config.Bridge) (devicekey.ID, error) {
	pin, err := devicekey.ParseID(b.ID)
	if err != nil {
		return devicekey.ID{}, fmt.Errorf("daemon: bridge %s: %w", b.Name, err)
	}
	return pin, nil
}
