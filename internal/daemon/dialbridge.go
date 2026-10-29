package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// bridgeBaseURL addresses Connect requests to a bridge; the host is ignored, the
// addresses dialed are the bridge's.
const bridgeBaseURL = "https://kithd"

// bridgeDialTimeout bounds one address's dial and handshake, so an address that does
// not answer leaves time for the next.
const bridgeDialTimeout = 5 * time.Second

// errNotTheBridge is a peer whose key is not the one pinned.
var errNotTheBridge = errors.New("daemon: the address answered with another key than the bridge's")

// pinnedTLS is a device's half of the listener's mutual TLS: it presents cert and
// takes the peer only if its key's ID is pin. No CA: the key is what is pinned, and
// VerifyConnection runs on a resumed session too.
func pinnedTLS(cert tls.Certificate, pin devicekey.ID) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
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
		ResponseHeaderTimeout: answerTimeout,
	}}
}

// NewBridgeRemote is a Remote for the bridge whose key's ID is pin, at the first of
// addrs that answers with it, as the device whose certificate is cert. It performs no
// I/O.
func NewBridgeRemote(cert tls.Certificate, pin devicekey.ID, addrs []string) *Remote {
	return newRemoteAt(dialBridge(cert, pin, addrs), bridgeBaseURL)
}
