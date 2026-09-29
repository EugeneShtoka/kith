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

// The line reaches cmd.exe as written: quotes inside it survive, and stdin is the
// payload, which is how the clipboard command is fed.
func TestShellCommandRunsTheLineAsWritten(t *testing.T) {
	t.Parallel()

	cmd := shellCommand(t.Context(), `echo "two words" & more`)
	cmd.Stdin = strings.NewReader("from stdin\r\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"two words"`) || !strings.Contains(got, "from stdin") {
		t.Errorf("output = %q, want the quoted echo and the stdin payload", got)
	}
}

// TestMain lets the test binary stand in for kithd.exe: started with the marker
// variable set, it records its pid there and stays alive instead of testing.
func TestMain(m *testing.M) {
	if marker := os.Getenv("KITH_FAKE_DAEMON_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// detach finds kithd.exe on PATH and starts it with the flags that keep it off this
// console.
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

	if err := detach(""); err != nil {
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
