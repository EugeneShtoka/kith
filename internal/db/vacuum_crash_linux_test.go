package db

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// vacuumChildEnv makes the test binary a child that vacuums vacuumChildCache in its
// working directory, under a file-size limit in bytes (0: none).
const vacuumChildEnv = "KITH_TEST_VACUUM_CHILD"

// vacuumChildCache is the cache a child vacuums, in its working directory.
const vacuumChildCache = "cache.db"

// vacuumChild is the child's side: it opens the cache, says so, and vacuums it. Under
// a file-size limit it exits 0 only if the vacuum failed.
func vacuumChild() bool {
	limitArg, child := os.LookupEnv(vacuumChildEnv)
	if !child {
		return false
	}
	path := vacuumChildCache
	limit, _ := strconv.ParseUint(limitArg, 10, 64)
	if limit > 0 {
		signal.Ignore(syscall.SIGXFSZ)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: limit, Max: limit}); err != nil {
			println("setrlimit:", err.Error())
			os.Exit(2)
		}
	}
	ctx := context.Background()
	c, err := Open(ctx, path, testKey)
	if err != nil {
		println("open:", err.Error())
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString("vacuuming\n")
	v, err := c.Vacuum(ctx, filepath.Dir(path))
	switch {
	case limit > 0 && err != nil:
		println(err.Error())
		os.Exit(0)
	case err != nil || !v.Ran:
		println("vacuum:", v.Ran, err)
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString("done\n")
	if limit > 0 { // under a limit, success is the failure
		os.Exit(3)
	}
	os.Exit(0)
	return true
}

// A vacuum killed partway, whether building its copy or writing it back, or one
// whose files cannot grow (a disk filling under it), leaves every row there and the
// cache whole; the next one runs.
func TestAVacuumCutShortLeavesTheCacheWhole(t *testing.T) {
	if vacuumChild() {
		return
	}
	t.Parallel()
	const room = domain.RoomID(mx + "!room:example.org")
	const kept = 60
	fixture := filepath.Join(t.TempDir(), "cache.db")
	c, err := Open(context.Background(), fixture, testKey)
	if err != nil {
		t.Fatal(err)
	}
	saveSome(t, c, room, 0, 300)
	pad(t, c, "kept", kept, false)
	pad(t, c, "gone", 45, true)
	if err := c.Close(); err != nil { // checkpoints: the fixture is one file
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		// cut waits for the moment to kill the child; nil runs it under limit.
		cut   func(t *testing.T, path string)
		limit uint64
	}{
		{name: "killed building its copy", cut: func(*testing.T, string) { time.Sleep(30 * time.Millisecond) }},
		{name: "killed writing it back", cut: func(t *testing.T, path string) {
			t.Helper()
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
				if info, err := os.Stat(path + "-wal"); err == nil && info.Size() > 8<<20 {
					return
				}
			}
			t.Fatal("the log never grew")
		}},
		{name: "out of room", limit: 20 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, vacuumChildCache)
			copyFile(t, fixture, path)
			cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^"+t.Name()+"$") // #nosec G204 -- this test binary
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), vacuumChildEnv+"="+strconv.FormatUint(tc.limit, 10))
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			lines := bufio.NewReader(out)
			if line, _ := lines.ReadString('\n'); line != "vacuuming\n" {
				_ = cmd.Process.Kill()
				t.Fatalf("the child said %q", line)
			}
			if tc.cut != nil {
				tc.cut(t, path)
				if err = cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			}
			rest, _ := io.ReadAll(lines)
			err = cmd.Wait()
			var exit *exec.ExitError
			switch {
			case tc.cut != nil && strings.Contains(string(rest), "done"):
				t.Fatal("the vacuum finished before it was cut short")
			case tc.cut != nil && (!errors.As(err, &exit) || exit.ExitCode() != -1):
				t.Fatalf("the child was not killed: %v", err)
			case tc.cut == nil && err != nil:
				t.Fatalf("the vacuum out of room: %v", err)
			}

			ctx := context.Background()
			after, err := Open(ctx, path, testKey)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = after.Close() }()
			var check string
			if err := after.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
				t.Errorf("integrity_check = %q, %v", check, err)
			}
			var pads int
			if err := after.db.QueryRowContext(ctx, "SELECT count(*) FROM kept").Scan(&pads); err != nil || pads != kept*256 {
				t.Errorf("%d padding rows, %v; want %d", pads, err, kept*256)
			}
			if n := countMessages(t, after, room); n != 300 {
				t.Errorf("%d messages, want 300", n)
			}
			if v, err := after.Vacuum(ctx, t.TempDir()); err != nil || !v.Ran {
				t.Errorf("the next vacuum: %+v, %v", v, err)
			}
		})
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from) // #nosec G304 -- the test's own fixture
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
