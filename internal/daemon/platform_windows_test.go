//go:build windows

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for kithd.exe: with the marker variable set,
// it records its pid there and stays alive.
func TestMain(m *testing.M) {
	if marker := os.Getenv("KITH_FAKE_DAEMON_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// detach finds kithd.exe on PATH and starts it off this console.
func TestDetachStartsTheDaemon(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, daemonBinary+".exe"), bin, 0o700); err != nil {
		t.Fatalf("write fake daemon: %v", err)
	}
	marker := filepath.Join(dir, "started")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KITH_FAKE_DAEMON_MARKER", marker)

	if err := detach(daemonBinary, nil); err != nil {
		t.Fatalf("detach() = %v", err)
	}
	waitFor(t, func() bool { return exists(marker) }, "the fake daemon to start")
	t.Cleanup(func() {
		raw, _ := os.ReadFile(marker)
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil {
			if p, findErr := os.FindProcess(pid); findErr == nil {
				_ = p.Kill()
			}
		}
	})
}
