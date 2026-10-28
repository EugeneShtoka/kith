package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/schedule"
)

// writeFile is os.WriteFile with the queue's mode.
func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}

type recordingSender struct {
	mu    sync.Mutex
	sent  []domain.Draft
	rooms []domain.RoomID
	fail  error
}

func (r *recordingSender) Send(_ context.Context, roomID domain.RoomID, draft domain.Draft) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.rooms = append(r.rooms, roomID)
	r.sent = append(r.sent, draft)
	return nil
}

func (r *recordingSender) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func newScheduler(t *testing.T, now time.Time, cutoff time.Duration) (*Scheduler, *recordingSender, *schedule.Store) {
	t.Helper()
	store := schedule.New(filepath.Join(t.TempDir(), "scheduled.toml"))
	sender := &recordingSender{}
	var reported []string
	s := NewScheduler(store, sender, func() time.Duration { return cutoff },
		func(_ slog.Level, line string) { reported = append(reported, line) })
	s.now = func() time.Time { return now }
	t.Cleanup(s.Stop)
	return s, sender, store
}

// A message that came due while nothing ran goes out when the daemon starts.
func TestStartSendsWhatCameDueWhileTheDaemonWasDown(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s, sender, store := newScheduler(t, now, 24*time.Hour)

	if err := store.Save([]domain.ScheduledMessage{
		{ID: "due", RoomID: mx + "!a:x", Body: "good morning", At: now.Add(-2 * time.Hour), Written: now.Add(-3 * time.Hour)},
		{ID: "later", RoomID: mx + "!a:x", Body: "not yet", At: now.Add(time.Hour), Written: now},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s.startNow(context.Background())

	if sender.count() != 1 {
		t.Fatalf("sent %d messages, want just the due one", sender.count())
	}
	if sender.sent[0].Body != "good morning" {
		t.Errorf("sent %q, want the overdue message", sender.sent[0].Body)
	}
	// Sent messages leave the queue; the future one stays.
	left := s.List()
	if len(left) != 1 || left[0].ID != "later" {
		t.Errorf("queue = %+v, want only the future message", left)
	}
	// The file agrees, so a restart does not send it twice.
	onDisk, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(onDisk) != 1 || onDisk[0].ID != "later" {
		t.Errorf("on disk = %+v, want only the future message", onDisk)
	}
}

// Past the cutoff a message is kept, not sent, and reported once.
func TestAMessagePastTheCutoffIsHeldNotSent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s, sender, store := newScheduler(t, now, 24*time.Hour)
	var lines []string
	s.report = func(_ slog.Level, line string) { lines = append(lines, line) }

	if err := store.Save([]domain.ScheduledMessage{
		{ID: "ancient", RoomID: mx + "!a:x", Body: "last tuesday", At: now.Add(-72 * time.Hour), Written: now.Add(-73 * time.Hour)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s.startNow(context.Background())

	if sender.count() != 0 {
		t.Errorf("sent %d messages, want none — it is past the cutoff", sender.count())
	}
	if len(s.List()) != 1 {
		t.Error("the held message was dropped; only its author may decide that")
	}
	if len(lines) == 0 {
		t.Error("a held message was not reported, so nobody would ever know")
	}
	// The report names the message, never its text.
	for _, line := range lines {
		if strings.Contains(line, "last tuesday") || !strings.Contains(line, "ancient") {
			t.Errorf("held line = %q, want the ID and not the body", line)
		}
	}

	// Reported once, not on every pass.
	before := len(lines)
	s.pass(context.Background())
	if len(lines) != before {
		t.Errorf("the same held message was reported again (%d then %d lines)", before, len(lines))
	}
}

// A cutoff of zero sends however late.
func TestACutoffOfZeroSendsAnythingOverdue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s, sender, store := newScheduler(t, now, 0)
	if err := store.Save([]domain.ScheduledMessage{
		{ID: "ancient", RoomID: mx + "!a:x", Body: "whenever", At: now.Add(-1000 * time.Hour)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s.startNow(context.Background())
	if sender.count() != 1 {
		t.Errorf("sent %d, want it sent however late", sender.count())
	}
}

// A scheduled thread reply lands in its thread, not the main timeline.
func TestAScheduledThreadReplyKeepsItsRoot(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s, sender, _ := newScheduler(t, now, 24*time.Hour)

	if err := s.Add(domain.ScheduledMessage{
		ID: "t1", RoomID: mx + "!a:x", Body: "in the thread", ThreadRoot: mx + "$root",
		Emote: true, At: now.Add(-time.Second),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("sent %d, want the due message", sender.count())
	}
	if got := sender.sent[0].ThreadRoot; got != mx+"$root" {
		t.Errorf("ThreadRoot = %q, want it kept", got)
	}
	if !sender.sent[0].Emote {
		t.Error("Emote was lost between scheduling and sending")
	}
}

// Canceling stops a send and survives a restart.
func TestCancelRemovesFromTheQueueAndTheFile(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s, sender, store := newScheduler(t, now, 24*time.Hour)
	if err := s.Add(domain.ScheduledMessage{
		ID: "x1", RoomID: mx + "!a:x", Body: "never mind", At: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Cancel("x1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if len(s.List()) != 0 {
		t.Error("the message is still queued")
	}
	onDisk, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(onDisk) != 0 {
		t.Error("the message is still on disk, so a restart would send it")
	}
	// Canceling what is already gone is not an error.
	if err := s.Cancel("x1"); err != nil {
		t.Errorf("canceling an unknown id: %v", err)
	}
	if sender.count() != 0 {
		t.Error("a canceled message was sent")
	}
}

// An unreadable queue is reported and does not stop the daemon.
func TestAnUnreadableQueueIsReportedNotFatal(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "scheduled.toml")
	if err := writeFile(path, "this is not toml = = ="); err != nil {
		t.Fatalf("write: %v", err)
	}
	var lines []string
	s := NewScheduler(schedule.New(path), &recordingSender{},
		func() time.Duration { return 24 * time.Hour },
		func(_ slog.Level, line string) { lines = append(lines, line) })
	s.now = func() time.Time { return now }
	t.Cleanup(s.Stop)

	s.startNow(context.Background()) // must not panic
	if len(lines) == 0 {
		t.Error("an unreadable queue was not reported")
	}
	if len(s.List()) != 0 {
		t.Error("entries appeared out of an unreadable file")
	}
}

// A message that could not be saved is not listed.
func TestAMessageThatCouldNotBeSavedIsNotQueued(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	// A path inside a file is the cheapest unwritable location.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := writeFile(blocker, "not a directory"); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := NewScheduler(schedule.New(filepath.Join(blocker, "scheduled.toml")),
		&recordingSender{}, func() time.Duration { return 24 * time.Hour }, nil)
	s.now = func() time.Time { return now }
	t.Cleanup(s.Stop)

	err := s.Add(domain.ScheduledMessage{
		ID: "doomed", RoomID: mx + "!a:x", Body: "never lands", At: now.Add(time.Hour),
	})
	if err == nil {
		t.Fatal("an unwritable queue accepted a message")
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("queue = %+v after a failed save, want nothing — the caller was told it failed", got)
	}
}

// flakyQueue is a Queue whose Save can be made to fail, to reach the ordering claim()
// enforces.
type flakyQueue struct {
	mu       sync.Mutex
	onDisk   []domain.ScheduledMessage
	failSave error
	saves    int
}

func (q *flakyQueue) Load() ([]domain.ScheduledMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]domain.ScheduledMessage, len(q.onDisk))
	copy(out, q.onDisk)
	return out, nil
}

func (q *flakyQueue) Save(queue []domain.ScheduledMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.saves++
	if q.failSave != nil {
		return q.failSave
	}
	q.onDisk = make([]domain.ScheduledMessage, len(queue))
	copy(q.onDisk, queue)
	return nil
}

func (q *flakyQueue) fail(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failSave = err
}

func (q *flakyQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.onDisk)
}

// Sending before saving would leave memory ahead of the file.
func TestAQueueThatCannotBeWrittenSendsNothingRatherThanTwice(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	store := &flakyQueue{onDisk: []domain.ScheduledMessage{
		{ID: "due", RoomID: mx + "!a:x", Body: "hello", At: now.Add(-time.Minute)},
	}}
	sender := &recordingSender{}
	start := func() *Scheduler {
		s := NewScheduler(store, sender, func() time.Duration { return 24 * time.Hour }, nil)
		s.now = func() time.Time { return now }
		t.Cleanup(s.Stop)
		return s
	}

	store.fail(os.ErrPermission)
	first := start()
	first.startNow(context.Background())

	if sender.count() != 0 {
		t.Fatalf("sent %d messages the queue could not record; a restart sends them again",
			sender.count())
	}
	if len(first.List()) != 1 {
		t.Error("the message left the queue even though the removal was never written")
	}

	// Nothing goes out until the file can be advanced, however many restarts.
	for range 3 {
		start().startNow(context.Background())
	}
	if sender.count() != 0 {
		t.Fatalf("after three restarts the message went out %d times", sender.count())
	}
	if store.len() != 1 {
		t.Errorf("the file holds %d entries, want the message still pending", store.len())
	}

	store.fail(nil)
	start().startNow(context.Background())
	if sender.count() != 1 {
		t.Errorf("sent %d, want exactly one once the queue could be written", sender.count())
	}
	if store.len() != 0 {
		t.Errorf("the file still holds %d entries after a successful send", store.len())
	}

	// The next restart sends nothing: the removal is durable.
	start().startNow(context.Background())
	if sender.count() != 1 {
		t.Errorf("a restart after a successful send re-sent it: %d total", sender.count())
	}
}

// A failed send goes back to the file, so an outage outlasting the daemon keeps it.
func TestAFailedSendGoesBackOnDisk(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	store := &flakyQueue{onDisk: []domain.ScheduledMessage{
		{ID: "due", RoomID: mx + "!a:x", Body: "hello", At: now.Add(-time.Minute)},
	}}
	sender := &recordingSender{fail: context.DeadlineExceeded}
	s := NewScheduler(store, sender, func() time.Duration { return 24 * time.Hour }, nil)
	s.now = func() time.Time { return now }
	t.Cleanup(s.Stop)

	s.startNow(context.Background())
	if store.len() != 1 {
		t.Fatalf("the file holds %d entries after a failed send, want the message back", store.len())
	}

	sender.fail = nil
	next := NewScheduler(store, sender, func() time.Duration { return 24 * time.Hour }, nil)
	next.now = func() time.Time { return now }
	t.Cleanup(next.Stop)
	next.startNow(context.Background())
	if sender.count() != 1 {
		t.Errorf("sent %d after the retry, want 1", sender.count())
	}
}

func TestShutdownMidPassReturnsUnsentMessages(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	store := &flakyQueue{onDisk: []domain.ScheduledMessage{
		{ID: "a", RoomID: mx + "!a:x", Body: "first", At: now.Add(-2 * time.Minute)},
		{ID: "b", RoomID: mx + "!a:x", Body: "second", At: now.Add(-time.Minute)},
	}}
	sender := &recordingSender{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := NewScheduler(store, sender, func() time.Duration { return 24 * time.Hour }, nil)
	s.now = func() time.Time { return now }
	t.Cleanup(s.Stop)
	s.Start(ctx)

	if sender.count() != 0 {
		t.Errorf("a canceled context still sent %d", sender.count())
	}
	if store.len() != 2 {
		t.Errorf("the file holds %d entries, want both back", store.len())
	}
}

// The timer a Schedule RPC arms survives the RPC's canceled context.
func TestAMessageAddedByAnRPCStillSendsAfterThatRPCReturns(t *testing.T) {
	t.Parallel()

	s, sender, _ := newScheduler(t, time.Now(), time.Hour)
	daemonCtx := t.Context()
	s.now = time.Now
	s.Start(daemonCtx)

	// As the handler does: its own request context, canceled on return.
	_, done := context.WithCancel(daemonCtx)
	if err := s.Add(domain.ScheduledMessage{
		ID: "soon", RoomID: mx + "!a:x", Body: "after the handler is gone",
		At: time.Now().Add(40 * time.Millisecond), Written: time.Now(),
	}); err != nil {
		t.Fatalf("Add() = %v", err)
	}
	done()

	deadline := time.Now().Add(2 * time.Second)
	for sender.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sender.count() != 1 {
		t.Fatalf("the message never went out: sent %d, queue %+v", sender.count(), s.List())
	}
}

func TestStopWaitsForAPassThatIsAlreadySending(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	entered := make(chan struct{})
	blocking := &blockingSender{entered: entered, release: release}

	store := schedule.New(filepath.Join(t.TempDir(), "scheduled.toml"))
	s := NewScheduler(store, blocking, func() time.Duration { return time.Hour }, func(slog.Level, string) {})
	s.now = time.Now

	// Add runs the pass inline and it parks inside Send, so Add blocks until release.
	added := make(chan error, 1)
	go func() {
		added <- s.Add(domain.ScheduledMessage{
			ID: "due", RoomID: mx + "!a:x", Body: "going out now",
			At: time.Now().Add(-time.Minute), Written: time.Now().Add(-2 * time.Minute),
		})
	}()

	// Stop from another goroutine so the test can tell whether it waits.
	<-entered
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()

	select {
	case <-stopped:
		t.Fatal("Stop returned while a pass was still inside Send")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop never returned after the pass finished")
	}
	if err := <-added; err != nil {
		t.Errorf("Add() = %v", err)
	}
}

// blockingSender parks inside Send until released.
type blockingSender struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingSender) Send(context.Context, domain.RoomID, domain.Draft) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return nil
}

// gatedQueue's first Save parks until released, to order two writers.
type gatedQueue struct {
	mu    sync.Mutex
	queue []domain.ScheduledMessage

	first   sync.Once
	entered chan struct{}
	release chan struct{}
}

func (g *gatedQueue) Load() ([]domain.ScheduledMessage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]domain.ScheduledMessage, len(g.queue))
	copy(out, g.queue)
	return out, nil
}

func (g *gatedQueue) Save(queue []domain.ScheduledMessage) error {
	held := false
	g.first.Do(func() {
		held = true
		close(g.entered)
	})
	if held {
		<-g.release
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queue = make([]domain.ScheduledMessage, len(queue))
	copy(g.queue, queue)
	return nil
}

// A sent message does not come back from a write still in flight.
func TestASentMessageDoesNotComeBackFromAWriteStillInFlight(t *testing.T) {
	t.Parallel()

	due := domain.ScheduledMessage{
		ID: "due", RoomID: mx + "!a:x", Body: "going out now",
		At: time.Now().Add(-time.Minute), Written: time.Now().Add(-2 * time.Minute),
	}
	store := &gatedQueue{
		queue:   []domain.ScheduledMessage{due},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	sender := &recordingSender{}
	s := NewScheduler(store, sender, func() time.Duration { return time.Hour }, func(slog.Level, string) {})
	s.now = time.Now
	s.mu.Lock()
	s.queue = []domain.ScheduledMessage{due}
	s.mu.Unlock()
	t.Cleanup(s.Stop)

	// Writer one adds a future message; its Save is gated.
	addDone := make(chan error, 1)
	go func() {
		addDone <- s.Add(domain.ScheduledMessage{
			ID: "later", RoomID: mx + "!a:x", Body: "not yet",
			At: time.Now().Add(time.Hour), Written: time.Now(),
		})
	}()
	<-store.entered

	// Writer two is the pass that claims and sends "due".
	passDone := make(chan struct{})
	go func() { s.pass(context.Background()); close(passDone) }()

	time.Sleep(50 * time.Millisecond) // let writer two get as far as it can
	close(store.release)

	<-passDone
	if err := <-addDone; err != nil {
		t.Fatalf("Add() = %v", err)
	}

	onDisk, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range onDisk {
		if msg.ID == "due" && sender.count() > 0 {
			t.Fatalf("%q was sent and is still in the file — the next start sends it again:\n%+v",
				msg.ID, onDisk)
		}
	}
	if sender.count() != 1 {
		t.Errorf("sent %d messages, want 1", sender.count())
	}
}

// A failed send stays queued and is retried on a backoff that grows to a ceiling and
// resets on success.
func TestAFailedSendIsRetriedOnACadence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s, sender, store := newScheduler(t, now, 24*time.Hour)
	sender.fail = errors.New("homeserver unreachable")

	if err := store.Save([]domain.ScheduledMessage{
		{ID: "stuck", RoomID: mx + "!a:x", Body: "hello", At: now.Add(-time.Minute), Written: now.Add(-time.Minute)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.startNow(context.Background())

	if got := len(s.List()); got != 1 {
		t.Fatalf("queue holds %d entries, want the failed message kept", got)
	}
	// A wake-up is armed for it.
	s.mu.Lock()
	armed, wait := s.timer != nil, s.backoff
	s.mu.Unlock()
	if !armed {
		t.Error("no retry armed after a failed send")
	}
	if wait != retryFloor {
		t.Errorf("backoff = %v after one failure, want %v", wait, retryFloor)
	}

	// Each failing pass waits longer, up to a ceiling.
	for range 20 {
		s.pass(context.Background())
	}
	s.mu.Lock()
	wait = s.backoff
	s.mu.Unlock()
	if wait != retryCeiling {
		t.Errorf("backoff = %v after a run of failures, want the ceiling %v", wait, retryCeiling)
	}

	// The server comes back: the message goes and the cadence resets.
	sender.fail = nil
	s.pass(context.Background())
	if sender.count() != 1 {
		t.Fatalf("sent %d messages after the server came back, want 1", sender.count())
	}
	if len(s.List()) != 0 {
		t.Errorf("queue = %v, want the sent message gone", s.List())
	}
	s.mu.Lock()
	wait, armed = s.backoff, s.timer != nil
	s.mu.Unlock()
	if wait != 0 {
		t.Errorf("backoff = %v after a successful pass, want it reset", wait)
	}
	if armed {
		t.Error("a timer is still armed with an empty queue")
	}
}

// A held message does not arm the retry: only a person releases it.
func TestAHeldMessageDoesNotArmARetry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	s, sender, store := newScheduler(t, now, time.Hour)
	sender.fail = errors.New("unreachable")

	if err := store.Save([]domain.ScheduledMessage{
		{ID: "stale", RoomID: mx + "!a:x", Body: "yesterday", At: now.Add(-25 * time.Hour), Written: now.Add(-25 * time.Hour)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.startNow(context.Background())

	s.mu.Lock()
	armed, wait := s.timer != nil, s.backoff
	s.mu.Unlock()
	if armed {
		t.Error("a held message armed a retry timer")
	}
	if wait != 0 {
		t.Errorf("backoff = %v, want none: nothing was attempted", wait)
	}
	if sender.count() != 0 {
		t.Error("a held message was sent")
	}
}

// A repeated failure is reported once per distinct error, and the success once.
func TestAFailingMessageIsReportedOncePerState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	store := schedule.New(filepath.Join(t.TempDir(), "scheduled.toml"))
	sender := &recordingSender{fail: errors.New("connection refused")}
	var reported []string
	s := NewScheduler(store, sender, func() time.Duration { return 24 * time.Hour },
		func(_ slog.Level, line string) { reported = append(reported, line) })
	s.now = func() time.Time { return now }
	t.Cleanup(s.Stop)

	if err := store.Save([]domain.ScheduledMessage{
		{ID: "stuck", RoomID: mx + "!a:x", Body: "hello", At: now.Add(-time.Minute), Written: now.Add(-time.Minute)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s.startNow(context.Background())
	for range 5 {
		s.pass(context.Background())
	}
	if len(reported) != 1 {
		t.Fatalf("reported %d lines for one repeated failure, want 1:\n%v", len(reported), reported)
	}

	// A different error is news.
	sender.mu.Lock()
	sender.fail = errors.New("M_FORBIDDEN")
	sender.mu.Unlock()
	s.pass(context.Background())
	if len(reported) != 2 {
		t.Fatalf("a changed error reported %d lines in total, want 2:\n%v", len(reported), reported)
	}

	// The eventual success is reported.
	sender.mu.Lock()
	sender.fail = nil
	sender.mu.Unlock()
	s.pass(context.Background())
	if len(reported) != 3 {
		t.Fatalf("reported %v, want a line saying it went out", reported)
	}
	if sender.count() != 1 {
		t.Errorf("sent %d, want the message delivered once", sender.count())
	}
}

// A pass panicking while planning is recovered: the lock is released and nothing is
// registered for Stop to wait on.
func TestAPanickingPassLeavesTheSchedulerUsable(t *testing.T) {
	t.Parallel()
	store := schedule.New(filepath.Join(t.TempDir(), "scheduled.toml"))
	var reported []string
	s := NewScheduler(store, &recordingSender{},
		func() time.Duration { panic("cutoff bug") },
		func(_ slog.Level, line string) { reported = append(reported, line) })

	s.startNow(context.Background())
	if len(reported) != 1 {
		t.Fatalf("reported %q, want the recovered panic", reported)
	}

	stopped := make(chan struct{})
	go func() {
		_ = s.List()
		s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("List or Stop hung after a recovered panic")
	}
}

// startNow is Start with the first pass run inline.
func (s *Scheduler) startNow(ctx context.Context) {
	s.load(ctx)
	s.pass(ctx)
}

// heldSender says when a send begins and holds it until released.
type heldSender struct {
	recordingSender
	began   chan struct{}
	release chan struct{}
}

func (h *heldSender) Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error {
	close(h.began)
	<-h.release
	return h.recordingSender.Send(ctx, roomID, draft)
}

// Start returns while an overdue send is in flight, so one slow homeserver does not
// keep clients from attaching.
func TestStartDoesNotWaitForTheFirstPass(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	store := schedule.New(filepath.Join(t.TempDir(), "scheduled.toml"))
	if err := store.Save([]domain.ScheduledMessage{
		{ID: "due", RoomID: mx + "!a:x", Body: "good morning", At: now.Add(-time.Hour), Written: now.Add(-2 * time.Hour)},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sender := &heldSender{began: make(chan struct{}), release: make(chan struct{})}
	s := NewScheduler(store, sender, func() time.Duration { return 24 * time.Hour }, nil)
	s.now = func() time.Time { return now }

	returned := make(chan struct{})
	go func() { s.Start(context.Background()); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		close(sender.release)
		t.Fatal("Start waited for a send")
	}
	<-sender.began // the pass is registered, so Stop waits for it
	close(sender.release)
	s.Stop()
	if sender.count() != 1 {
		t.Errorf("sent %d, want the overdue message sent once released", sender.count())
	}
}
