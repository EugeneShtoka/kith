package daemon

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The warning after auto-spawning says what it cost.
func TestStartedNoteSaysWhatItCost(t *testing.T) {
	t.Parallel()

	note := startedNote(t.Context(), defaultUnit)
	if !strings.Contains(note, "was not running") {
		t.Errorf("note = %q, want it to say the daemon was not running", note)
	}
	if !strings.Contains(note, "not notified") {
		t.Errorf("note = %q, want it to say what was missed while it was down", note)
	}
}

// The enable hint appears whenever the unit is not enabled, as in tests.
func TestStartedNoteOffersTheUnitWhenNotEnabled(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("no login autostart exists on Windows to offer; see detach_windows.go")
	}
	if unitEnabled(t.Context(), defaultUnit) {
		t.Skip("kithd.service is enabled on this machine; the hint is correctly absent")
	}
	note := startedNote(t.Context(), defaultUnit)
	if !strings.Contains(note, "systemctl --user enable") {
		t.Errorf("note = %q, want it to say how to make this stop happening", note)
	}
}

// trimOutput keeps a multi-line systemd complaint out of an error message; unitEnabled
// compares against it.
func TestTrimOutput(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ in, want string }{
		"is-enabled output": {"enabled\n", "enabled"},
		"multi-line":        {"Failed to start.\nSee journalctl.\n", "Failed to start."},
		"padded":            {"  disabled  \n", "disabled"},
		"empty":             {"", ""},
		"newline only":      {"\n", ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := trimOutput([]byte(tc.in)); got != tc.want {
				t.Errorf("trimOutput(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A spawned daemon must outlive the process that spawned it.
func TestDetachedDaemonOutlivesItsCaller(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid unavailable")
	}

	dir, marker := fakeDaemon(t)
	if err := detach(daemonBinary, nil); err != nil {
		t.Fatalf("detach() = %v", err)
	}
	t.Cleanup(func() { kill(t, dir) })
	waitFor(t, func() bool { return exists(marker) }, "the fake daemon to start")
	time.Sleep(300 * time.Millisecond)
	if !processAlive(t, dir) {
		t.Error("the detached daemon died")
	}
}

// fakeDaemon puts a kithd stand-in first on PATH that marks the disk, then stays alive.
func fakeDaemon(t *testing.T) (dir, marker string) {
	t.Helper()

	dir = t.TempDir()
	marker = filepath.Join(dir, "started")
	// The stub releases its inherited stdio before sleeping.
	script := "#!/bin/sh\ntouch " + marker + "\nexec >/dev/null 2>&1 </dev/null\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, daemonBinary), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake daemon: %v", err)
	}
	// Prepended: detach needs setsid too.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, marker
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// processAlive reports whether the fake daemon from dir runs, matched on its unique
// temp path.
func processAlive(t *testing.T, dir string) bool {
	t.Helper()
	return exec.CommandContext(t.Context(), "pgrep", "-f", filepath.Join(dir, daemonBinary)).Run() == nil
}

// kill runs from t.Cleanup, where t.Context() is already canceled.
func kill(t *testing.T, dir string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "pkill", "-f", filepath.Join(dir, daemonBinary)).Run()
}

// waitFor polls until cond holds, failing rather than hanging.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A daemon holding the socket open but never answering does not hang the client.
func TestStatusProbeIsBoundedAgainstASilentDaemon(t *testing.T) {
	t.Parallel()

	socket := filepath.Join(t.TempDir(), "s.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan struct{}, 1)
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			select {
			case accepted <- struct{}{}:
			default:
			}
			// Hold it open, never reply; closing would error at once, which already worked.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	start := time.Now()
	_, perr := statusProbe(context.Background(), NewRemote(socket))
	elapsed := time.Since(start)

	if perr == nil {
		t.Fatal("expected an error from a daemon that never answers")
	}
	// The bound is reattachProbeTimeout; generous slack still catches an unbounded call.
	if limit := 10 * time.Second; elapsed > limit {
		t.Errorf("statusProbe took %s, want it bounded near %s", elapsed, reattachProbeTimeout)
	}
	select {
	case <-accepted:
	default:
		t.Error("the probe never reached the listener; the test proved nothing")
	}
}
