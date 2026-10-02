package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The socket is per-account and named by hash: two accounts on one machine get two daemons,
// the same account gets the same path every run, and the MXID does not appear in a
// directory listing.
func TestSocketPath(t *testing.T) {
	t.Parallel()

	storage := domain.Storage{Instance: "a1b2c3", RuntimeDir: "/run/user/1000/kith"}
	path, err := daemon.SocketPath(storage)
	if err != nil || path != "/run/user/1000/kith/a1b2c3.sock" {
		t.Errorf("SocketPath() = (%q, %v), want the instance's socket in the runtime directory", path, err)
	}
	other := storage
	other.Instance = "d4e5f6"
	if p, _ := daemon.SocketPath(other); p == path {
		t.Errorf("two instances share the socket %q", path)
	}
	// No runtime directory: refused, never a shared temp directory.
	if _, err := daemon.SocketPath(domain.Storage{Instance: "a1b2c3"}); err == nil {
		t.Error("SocketPath() with no runtime directory succeeded")
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
