//go:build !windows

package llamacpp

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStartReportsAnImmediateExit(t *testing.T) {
	t.Parallel()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}

	// Exits at once; the limit is far above the test's patience, so passing means the
	// exit was noticed, not waited out.
	const limit = 30 * time.Second
	started := time.Now()
	_, err = start(context.Background(), Settings{
		Command: sh,
		Model:   "-c:exit 1", // args() puts --model first; the shell just exits
		Startup: limit,
	})
	took := time.Since(started)

	if err == nil {
		t.Fatal("start() succeeded against a command that exits immediately")
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("start() error = %q, want it to name the exit rather than a timeout", err)
	}
	if took > 5*time.Second {
		t.Errorf("start() took %s to notice an immediate exit (limit was %s) — that is the "+
			"poll waiting out the deadline rather than watching the process", took, limit)
	}
}

func TestStopReturnsWhenTheProcessDies(t *testing.T) {
	t.Parallel()

	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep on PATH")
	}

	// Wired like start(): own process group, a reaper that closes exited.
	cmd := exec.CommandContext(context.Background(), sleep, "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("spawn: %v", startErr)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	srv := &server{cmd: cmd, exited: exited}

	began := time.Now()
	if stopErr := srv.stop(); stopErr != nil {
		t.Fatalf("stop() error = %v", stopErr)
	}
	took := time.Since(began)

	// sleep dies on SIGTERM; nearing the 2s deadline means the exit went unobserved.
	if took > time.Second {
		t.Errorf("stop() took %s for a process that dies on SIGTERM — that is the deadline "+
			"expiring, which means SIGKILL for a process that was already gone", took)
	}

	select {
	case <-srv.died():
	default:
		t.Error("stop() returned before the process was reaped")
	}
}

// Stopping a never-started server does not block on a nil channel.
func TestStopOnAServerThatNeverSpawned(t *testing.T) {
	t.Parallel()

	done := make(chan error, 1)
	go func() { done <- (&server{}).stop() }()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("stop() on an unstarted server = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop() blocked on a server that was never started")
	}
}
