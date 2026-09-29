package daemon_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/daemon"
)

// workerLog is a concurrency-safe buffer a worker's logger writes to.
type workerLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *workerLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *workerLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// A refresh that fails is retried on the next change — and now also says why, where
// before it vanished.
func TestRefresherLogsAFailedRefresh(t *testing.T) {
	t.Parallel()

	backend := &refreshCounter{err: errors.New("M_LIMIT_EXCEEDED")}
	var invalidated atomic64
	r := daemon.NewRefresher(backend, invalidated.inc)
	out := &workerLog{}
	r.UseLogger(slog.New(slog.NewTextHandler(out, nil)))

	ctx := t.Context()
	go r.Run(ctx)

	waitFor(t, func() bool {
		s := out.String()
		return strings.Contains(s, "op=\"refresh rooms\"") && strings.Contains(s, "op=\"refresh spaces\"")
	})
	got := out.String()
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, "M_LIMIT_EXCEEDED") {
		t.Errorf("log = %q, want a warning carrying the reason", got)
	}
	if invalidated.get() != 0 {
		t.Error("a failed refresh still invalidated the scope")
	}
}
