package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
)

// backupCounter is a backend whose answer the test chooses, counting how often it
// was asked.
type backupCounter struct {
	mu       sync.Mutex
	passes   int
	uploaded int
	err      error
}

func (c *backupCounter) BackupRoomKeys(context.Context) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.passes++
	return c.uploaded, c.err
}

func (c *backupCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.passes
}

func (c *backupCounter) answer(uploaded int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.uploaded, c.err = uploaded, err
}

// recorder collects the lines the sweeper reported.
type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) log(_ slog.Level, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// The startup pass is not redundant with the timed ones: keys that arrived while the daemon
// was stopped are in the store and in no backup, so waiting a full interval to notice them
// would make every restart a window.
func TestKeyBackupSweepsAtStartupAndOnTheTimer(t *testing.T) {
	t.Parallel()

	backend := &backupCounter{}
	k := NewKeyBackup(backend, func(slog.Level, string) {})
	k.every = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go k.Run(ctx)

	deadline := time.After(2 * time.Second)
	for backend.count() < 3 {
		select {
		case <-deadline:
			t.Fatalf("passes = %d after two seconds, want at least 3", backend.count())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
}

// An account with no backup is a state, not a fault, and it does not change by itself.
func TestKeyBackupSaysAStandingConditionOnce(t *testing.T) {
	t.Parallel()

	backend := &backupCounter{err: api.ErrNoKeyBackup}
	lines := &recorder{}
	k := NewKeyBackup(backend, lines.log)

	for range 3 {
		k.pass(t.Context())
	}
	got := lines.all()
	if len(got) != 1 {
		t.Fatalf("reported %d lines for one standing condition, want 1: %q", len(got), got)
	}
	if !strings.Contains(got[0], "--bootstrap-keys") {
		t.Errorf("line = %q, want it to name the command that fixes it", got[0])
	}
}

// Said once is not said never: when the condition goes away and comes back, it is
// worth hearing again — the second occurrence is new information.
func TestKeyBackupSaysItAgainAfterItChanges(t *testing.T) {
	t.Parallel()

	backend := &backupCounter{err: api.ErrNoKeyBackup}
	lines := &recorder{}
	k := NewKeyBackup(backend, lines.log)

	k.pass(t.Context())
	backend.answer(0, nil) // a backup appeared; nothing to upload
	k.pass(t.Context())
	backend.answer(0, api.ErrNoKeyBackup) // and it is gone again
	k.pass(t.Context())

	if got := lines.all(); len(got) != 2 {
		t.Errorf("reported %d lines, want 2 — the condition returned and that is news: %q", len(got), got)
	}
}

// A pass that uploaded something says how much. A pass with nothing to do says
// nothing at all, which is what a working daemon looks like almost all the time.
func TestKeyBackupReportsOnlyWhatItDid(t *testing.T) {
	t.Parallel()

	backend := &backupCounter{}
	lines := &recorder{}
	k := NewKeyBackup(backend, lines.log)

	k.pass(t.Context())
	if got := lines.all(); len(got) != 0 {
		t.Errorf("reported %q for a pass with nothing to do, want silence", got)
	}

	backend.answer(3, nil)
	k.pass(t.Context())
	got := lines.all()
	if len(got) != 1 || !strings.Contains(got[0], "3") {
		t.Errorf("lines = %q, want one naming 3 keys", got)
	}
}

// Shutting the daemon down cancels the context mid-pass.
func TestKeyBackupIsSilentOnShutdown(t *testing.T) {
	t.Parallel()

	backend := &backupCounter{err: context.Canceled}
	lines := &recorder{}
	k := NewKeyBackup(backend, lines.log)
	k.pass(t.Context())

	if got := lines.all(); len(got) != 0 {
		t.Errorf("reported %q on shutdown, want silence", got)
	}
}

// Anything else is a real failure and is reported — once, for as long as it keeps
// saying the same thing.
func TestKeyBackupReportsAFailure(t *testing.T) {
	t.Parallel()

	backend := &backupCounter{err: errors.New("homeserver unreachable")}
	lines := &recorder{}
	k := NewKeyBackup(backend, lines.log)
	k.pass(t.Context())

	got := lines.all()
	if len(got) != 1 || !strings.Contains(got[0], "homeserver unreachable") {
		t.Errorf("lines = %q, want one carrying the failure", got)
	}
}
