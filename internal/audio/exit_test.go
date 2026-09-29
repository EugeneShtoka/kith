package audio

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A player that dies at once is reported as exited, and noticed before socketWait.
func TestPlayReportsAPlayerThatExitsImmediately(t *testing.T) {
	t.Parallel()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}

	began := time.Now()
	session, err := Play(context.Background(), []string{sh, "-c", "exit 3"}, "voice.ogg", 1)
	took := time.Since(began)

	if err == nil {
		session.Close()
		t.Fatal("Play() succeeded with a program that exits immediately")
	}
	if !strings.Contains(err.Error(), "exited before it could be controlled") {
		t.Errorf("Play() error = %q, want it to say the player exited", err)
	}
	if took >= socketWait {
		t.Errorf("Play() took %s, the whole socket wait", took)
	}
}

// waitForSocket gives up when its context does, rather than polling to the deadline.
func TestWaitForSocketHonorsTheContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	began := time.Now()
	_, err := waitForSocket(ctx, "/nonexistent/kith-audio/ipc", make(chan struct{}))
	if err == nil {
		t.Fatal("waitForSocket() succeeded against a socket that does not exist")
	}
	if took := time.Since(began); took >= socketWait {
		t.Errorf("waitForSocket() took %s despite a canceled context", took)
	}
}
