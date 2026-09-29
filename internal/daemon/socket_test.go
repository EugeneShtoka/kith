package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/daemon"
)

// The socket is per-account and named by hash: two accounts on one machine get two daemons,
// the same account gets the same path every run, and the MXID does not appear in a
// directory listing.
func TestSocketPath(t *testing.T) {
	t.Parallel()

	const user = "@ada:example.org"
	path, err := daemon.SocketPath(user)
	if err != nil {
		t.Fatalf("SocketPath(%q) = %v", user, err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("SocketPath() = %q, want an absolute path", path)
	}
	if filepath.Ext(path) != ".sock" {
		t.Errorf("SocketPath() = %q, want a .sock name", path)
	}
	if strings.Contains(path, "ada") || strings.Contains(path, "example.org") {
		t.Errorf("SocketPath() = %q, want the MXID hashed out of the name", path)
	}

	again, err := daemon.SocketPath(user)
	if err != nil {
		t.Fatalf("SocketPath(%q) second call = %v", user, err)
	}
	if again != path {
		t.Errorf("SocketPath() = %q then %q, want a stable path", path, again)
	}

	other, err := daemon.SocketPath("@grace:example.org")
	if err != nil {
		t.Fatalf("SocketPath() for a second user = %v", err)
	}
	if other == path {
		t.Errorf("SocketPath() = %q for both users, want one socket per account", path)
	}
}

// The socket and its directory are owner-only. Everything the account can do is
// reachable through it, and on this transport the file mode is the only check.
func TestListenPermissions(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "runtime", "kith")
	path := filepath.Join(dir, "kithd.sock")
	ln, err := daemon.Listen(context.Background(), path)
	if err != nil {
		t.Fatalf("Listen(%q) = %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	sock, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := sock.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %04o, want 0600", perm)
	}
	parent, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat socket directory: %v", err)
	}
	if perm := parent.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket directory mode = %04o, want 0700", perm)
	}
}

// A socket already in use is an error, not a takeover: only the lock holder may unlink a stale one.
func TestListenRefusesAnAddressInUse(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kithd.sock")
	ln, err := daemon.Listen(context.Background(), path)
	if err != nil {
		t.Fatalf("Listen(%q) = %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	second, err := daemon.Listen(context.Background(), path)
	if err == nil {
		_ = second.Close()
		t.Fatal("Listen() on a socket in use = nil, want an error")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("the live socket was disturbed: %v", statErr)
	}
}
