// Package daemon holds both sides of the kithd wire: Serve hosts an api.Backend
// over a unix socket via Connect, and Remote is the api.Backend the TUI runs
// against. Schema: api/proto/backend/v1; mapping: protoconv.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/adrg/xdg"
)

// socketDirMode keeps the socket's parent directory owner-only; it closes the
// window before the socket's own mode is tightened after net.Listen.
const socketDirMode = 0o700

// socketBaseURL is what Connect requests are addressed to; the host is ignored.
const socketBaseURL = "http://kithd"

// SocketPath returns the path of the daemon's unix socket for user (an MXID), named
// by a hash to keep the MXID out of directory listings. It is always under the user's
// runtime directory: never the shared temp directory xdg.RuntimeFile falls back to,
// where another local user could create the directory first.
func SocketPath(user string) (string, error) {
	if xdg.RuntimeDir == "" {
		return "", errors.New("daemon: no user runtime directory; set XDG_RUNTIME_DIR to an owner-only directory")
	}
	return filepath.Join(xdg.RuntimeDir, "kith", domain.AccountKey(user)+".sock"), nil
}

// makeSocketDir creates the socket's directory owner-only, or checks an existing one.
func makeSocketDir(dir string) error {
	if err := os.MkdirAll(dir, socketDirMode); err != nil {
		return fmt.Errorf("daemon: create socket directory (is XDG_RUNTIME_DIR set to a directory you own?): %w", err)
	}
	return secureDir(dir)
}

// Listen opens the daemon's listening socket at path, creating its parent directory
// owner-only. It never removes an existing socket — only the lock holder may (see
// Lock.Listen). ctx bounds opening only.
func Listen(ctx context.Context, path string) (net.Listener, error) {
	if err := makeSocketDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("daemon: listen on %s: %w", path, err)
	}
	if err := restrictSocket(path); err != nil {
		_ = ln.Close() // cleanup on the error path; the chmod failure is the report
		return nil, fmt.Errorf("daemon: restrict socket permissions: %w", err)
	}
	return ln, nil
}

// answerTimeout bounds the wait for the first response header, so a wedged daemon
// cannot hang a TUI call (whose context has no deadline) forever. It does not bound a
// body (a stream, a media download), but it does bound a stream's headers: every
// server stream sends them as it opens (serveStream, Seat).
const answerTimeout = 2 * time.Minute

// Dial returns an HTTP client that reaches the daemon's socket at path,
// whatever host a request names.
func Dial(path string) *http.Client {
	return dialWithAnswerTimeout(path, answerTimeout)
}

func dialWithAnswerTimeout(path string, answer time.Duration) *http.Client {
	dialer := &net.Dialer{}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				// Secrets go down this socket: never to one in a directory someone else controls.
				if err := secureDir(filepath.Dir(path)); err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, "unix", path)
			},
			ResponseHeaderTimeout: answer,
		},
	}
}
