package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A daemon that accepts and never answers does not hold a caller forever.
func TestACallToASilentDaemonDoesNotHangForever(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "silent.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	// Accept and stay silent, as a wedged daemon does: dialing succeeds, no error comes.
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	hc := dialWithAnswerTimeout(path, 150*time.Millisecond)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, socketBaseURL+"/anything", nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		resp, rerr := hc.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- rerr
	}()

	select {
	case rerr := <-done:
		if rerr == nil {
			t.Fatal("a daemon that answered nothing produced no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call never returned: a silent daemon still holds its caller forever")
	}
}

// The socket carries passwords and recovery keys, so a symlinked directory is refused
// and a loose mode on our own directory is tightened.
func TestTheSocketDirectoryIsOwnerOnly(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the directory's ACL, not its mode, guards the socket on Windows")
	}

	loose := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := makeSocketDir(loose); err != nil {
		t.Fatalf("makeSocketDir(own, 0755) = %v", err)
	}
	if info, err := os.Stat(loose); err != nil || info.Mode().Perm() != socketDirMode {
		t.Fatalf("mode after makeSocketDir = %v (%v), want %o", info.Mode().Perm(), err, socketDirMode)
	}

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(loose, link); err != nil {
		t.Fatal(err)
	}
	if err := makeSocketDir(link); err == nil {
		t.Fatal("makeSocketDir(symlink) = nil, want a refusal")
	}
	hc := Dial(filepath.Join(link, "mx.sock"))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, socketBaseURL+"/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("dialing through a symlinked directory: %v, want a refusal", err)
	}
}
