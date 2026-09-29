package daemon_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// draftStore is a backend that keeps drafts and takes every write.
type draftStore struct {
	apitest.Nop
	mu     sync.Mutex
	drafts map[domain.RoomID]domain.StoredDraft
}

func (s *draftStore) ReplaceDraft(_ context.Context, draft, _ domain.StoredDraft) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drafts == nil {
		s.drafts = map[domain.RoomID]domain.StoredDraft{}
	}
	s.drafts[draft.RoomID] = draft
	return true, nil
}

func (s *draftStore) Drafts(context.Context) ([]domain.StoredDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Collect(maps.Values(s.drafts)), nil
}

// window is a client of h that asked for the seat, and what it was told; it lets go
// when the test ends.
func window(t *testing.T, h *harness, force bool, pid int) (*daemon.Remote, error) {
	t.Helper()
	r := h.client()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { r.Stop(); cancel() })
	err := r.TakeSeat(ctx, force, domain.SeatHolder{PID: pid, TTY: "/dev/pts/7"})
	return r, err
}

// A second window without --force is refused, and told where the first one is.
func TestASecondWindowIsRefusedAndToldWhereTheFirstIs(t *testing.T) {
	t.Parallel()
	h := serve(t, &draftStore{})
	if _, err := window(t, h, false, 101); err != nil {
		t.Fatalf("the first window: %v", err)
	}
	_, err := window(t, h, false, 202)
	if !errors.Is(err, api.ErrSeatTaken) {
		t.Fatalf("the second window: %v, want ErrSeatTaken", err)
	}
	if !strings.Contains(err.Error(), "pid 101 on /dev/pts/7") {
		t.Errorf("refusal %q does not say where the first window is", err)
	}
}

// --force tells the window in the seat to step aside, and gets the seat once it has
// closed, not before.
func TestForceTakesTheSeatOnceTheOtherWindowHasClosed(t *testing.T) {
	t.Parallel()
	h := serve(t, &draftStore{})
	first, err := window(t, h, false, 101)
	if err != nil {
		t.Fatalf("the first window: %v", err)
	}
	taken := make(chan error, 1)
	go func() { _, err := window(t, h, true, 202); taken <- err }()

	select {
	case <-first.SeatLost():
	case <-time.After(settle):
		t.Fatal("the first window was never told to step aside")
	}
	select {
	case err := <-taken:
		t.Fatalf("the seat was taken (%v) before the first window closed", err)
	case <-time.After(100 * time.Millisecond):
	}
	// It saves its drafts, then closes.
	if saved, err := first.ReplaceDraft(t.Context(), domain.StoredDraft{RoomID: "!a:x", Body: "last words"}, domain.StoredDraft{}); !saved || err != nil {
		t.Fatalf("the first window's last save = %v, %v", saved, err)
	}
	first.Stop()
	if err := take(t, taken, "the forced TakeSeat"); err != nil {
		t.Fatalf("--force: %v", err)
	}
}

// A window that is gone does not block the next one.
func TestAClosedWindowLeavesTheSeatFree(t *testing.T) {
	t.Parallel()
	h := serve(t, &draftStore{})
	first, err := window(t, h, false, 101)
	if err != nil {
		t.Fatalf("the first window: %v", err)
	}
	first.Stop()
	deadline := time.Now().Add(settle)
	for {
		_, err := window(t, h, false, 202)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the seat stayed taken after its window closed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A window that does not answer loses the seat after a few seconds, and its draft
// writes are refused from then on. A writer that is no window (kith-mcp) is not refused.
func TestAWindowThatDoesNotAnswerLosesTheSeat(t *testing.T) {
	t.Parallel()
	h := serve(t, &draftStore{})
	hung, err := window(t, h, false, 101)
	if err != nil {
		t.Fatalf("the first window: %v", err)
	}
	began := time.Now()
	if _, err := window(t, h, true, 202); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if waited := time.Since(began); waited < 2*time.Second || waited > 5*time.Second {
		t.Errorf("the seat was given over after %v, want the few seconds a window has to answer", waited)
	}
	if _, err := hung.ReplaceDraft(t.Context(), domain.StoredDraft{RoomID: "!a:x", Body: "late"}, domain.StoredDraft{}); !errors.Is(err, api.ErrSeatTaken) {
		t.Errorf("the old window's write = %v, want ErrSeatTaken", err)
	}
	if saved, err := h.client().ReplaceDraft(t.Context(), domain.StoredDraft{RoomID: "!a:x", Body: "added"}, domain.StoredDraft{}); !saved || err != nil {
		t.Errorf("an appender's write = %v, %v; want it taken", saved, err)
	}
}

// Two --force at once past a window that does not answer: each grant tells the window it
// replaces, so no window sits on unaware while its writes are refused.
func TestEveryWindowReplacedIsToldItLostTheSeat(t *testing.T) {
	t.Parallel()
	h := serve(t, &draftStore{})
	if _, err := window(t, h, false, 101); err != nil { // does not answer: never stops
		t.Fatalf("the first window: %v", err)
	}
	type took struct {
		r   *daemon.Remote
		err error
	}
	res := make(chan took, 2)
	for _, pid := range []int{202, 303} {
		go func() { r, err := window(t, h, true, pid); res <- took{r, err} }()
	}
	for range 2 {
		got := <-res
		if got.err != nil {
			t.Fatalf("--force: %v", got.err)
		}
		_, err := got.r.ReplaceDraft(t.Context(), domain.StoredDraft{RoomID: "!a:x", Body: "x"}, domain.StoredDraft{})
		if !errors.Is(err, api.ErrSeatTaken) {
			continue // the one sitting now
		}
		select {
		case <-got.r.SeatLost():
		case <-time.After(settle):
			t.Error("a window whose writes are refused was never told it lost the seat")
		}
	}
}

// A signal (the terminal closing) ends the window's context before its last draft
// write: the seat stays with it until Stop, which comes after that write.
func TestASignalKeepsTheSeatUntilStop(t *testing.T) {
	t.Parallel()
	h := serve(t, &draftStore{})
	a := h.client()
	sig, hup := context.WithCancel(context.Background())
	t.Cleanup(func() { a.Stop(); hup() })
	if err := a.TakeSeat(sig, false, domain.SeatHolder{PID: 101}); err != nil {
		t.Fatalf("the first window: %v", err)
	}
	hup()
	time.Sleep(100 * time.Millisecond)
	if _, err := window(t, h, false, 202); !errors.Is(err, api.ErrSeatTaken) {
		t.Fatalf("a second window after the signal = %v, want it refused while the first still writes", err)
	}
	if _, err := a.ReplaceDraft(context.Background(), domain.StoredDraft{RoomID: "!a:x", Body: "last words"}, domain.StoredDraft{}); err != nil {
		t.Fatalf("the closing window's last write = %v", err)
	}
	a.Stop()
	deadline := time.Now().Add(settle)
	for {
		if _, err := window(t, h, false, 303); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("the seat stayed taken after Stop: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A link handed over while the window sits but does not listen for links yet (it is
// at a startup prompt, or reconnecting) is delivered, not taken for "no window": that
// would start a second terminal, which the seat refuses.
func TestALinkWaitsForTheWindowThatSits(t *testing.T) {
	t.Parallel()
	h := serve(t, &draftStore{})
	seated, err := window(t, h, false, 101) // sits; its streams are not open yet
	if err != nil {
		t.Fatalf("the window: %v", err)
	}
	delivered, err := h.client().Follow(t.Context(), "matrix:roomid/abc:x")
	if err != nil || !delivered {
		t.Fatalf("Follow = %v, %v; want it delivered to the window that sits", delivered, err)
	}
	go func() { _ = seated.Start(context.Background()) }()
	if got := take(t, seated.Follows(), "Follows()"); got != "matrix:roomid/abc:x" {
		t.Errorf("the window was handed %q, want the link", got)
	}

	empty := serve(t, &draftStore{})
	if delivered, err := empty.client().Follow(t.Context(), "matrix:roomid/abc:x"); err != nil || delivered {
		t.Errorf("Follow with no window = %v, %v; want not delivered, so a terminal is started", delivered, err)
	}
}
