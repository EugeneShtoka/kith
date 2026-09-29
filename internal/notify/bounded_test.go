//go:build !windows

package notify

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// failures collects what a sink reported.
type failures struct {
	mu   sync.Mutex
	errs []string
}

func (f *failures) report(_ string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs = append(f.errs, err.Error())
}

func (f *failures) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, e := range f.errs {
			if strings.Contains(e, want) {
				f.mu.Unlock()
				return
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no failure mentioning %q was reported", want)
}

func sleeper(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "sleep", "30") }

// A hook that hangs is killed at the timeout and said to have failed.
func TestAHangingHookIsKilledAtTheTimeout(t *testing.T) {
	t.Parallel()
	var got failures
	b := &bounded{slots: make(chan struct{}, 1), timeout: 100 * time.Millisecond}

	b.start("command", "notification command", got.report, sleeper)
	got.waitFor(t, "notification command: signal: killed")
	select {
	case b.slots <- struct{}{}: // the killed hook gave its slot back
	case <-time.After(5 * time.Second):
		t.Fatal("the killed hook still holds its slot")
	}
}

// With every slot taken, the next notification's hook is skipped and said to be,
// rather than piling up another process.
func TestHooksBeyondTheCapAreSkipped(t *testing.T) {
	t.Parallel()
	var got failures
	b := &bounded{slots: make(chan struct{}, 1), timeout: 500 * time.Millisecond} // reaps the sleeper
	started := 0
	build := func(ctx context.Context) *exec.Cmd { started++; return sleeper(ctx) }

	b.start("command", "notification command", got.report, build)
	b.start("command", "notification command", got.report, build)
	got.waitFor(t, "skipped this one")
	if started != 1 {
		t.Errorf("started %d processes, want the second one skipped", started)
	}
}
