package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Sender is what the scheduler needs from the backend. Scheduling lives in the
// daemon because nothing else may be running when a message comes due; it also
// makes the queue single-writer.
type Sender interface {
	Send(ctx context.Context, roomID domain.RoomID, draft domain.Draft) error
}

// Queue is the durable half, declared here because its store (internal/schedule)
// reads and writes files, and the wire package every client links must not.
type Queue interface {
	Load() ([]domain.ScheduledMessage, error)
	Save(queue []domain.ScheduledMessage) error
}

// Scheduler owns the pending-send queue: the file, the timer and the sending.
type Scheduler struct {
	store  Queue
	sender Sender
	// cutoff is read per pass so a config reload takes effect.
	cutoff func() time.Duration
	// report is the only account of a held or failed send.
	report func(level slog.Level, line string)
	now    func() time.Time

	mu sync.Mutex
	// base is the daemon's context, set by Start and used by every pass and timer.
	// Never a request context: net/http cancels those when the handler returns,
	// which once silently disarmed the whole queue.
	base context.Context //nolint:containedctx // the queue outlives every request that touches it
	// stopped is set by Stop under mu, which keeps wg.Add off the wrong side of wg.Wait.
	stopped bool
	// wg counts passes in flight; the caller closes the client and cache after Stop.
	wg    sync.WaitGroup
	queue []domain.ScheduledMessage
	timer *time.Timer
	// held: messages already reported as too late, so each is reported once.
	held map[string]bool
	// failed is the last error reported per message: one line per outage, but a
	// changed error is news.
	failed map[string]string
	// backoff is the wait before retrying after a pass left something unsent (zero
	// when none). Needed because restored messages are already due, and PlanSchedule's
	// Next only covers future ones.
	backoff time.Duration
}

// retryFloor and retryCeiling bound the doubling retry cadence.
const (
	retryFloor   = 15 * time.Second
	retryCeiling = 5 * time.Minute
)

// NewScheduler builds a scheduler over the queue at store.
func NewScheduler(store Queue, sender Sender, cutoff func() time.Duration, report func(level slog.Level, line string)) *Scheduler {
	if report == nil {
		report = func(slog.Level, string) {}
	}
	return &Scheduler{
		store: store, sender: sender, cutoff: cutoff, report: report,
		now: time.Now, held: map[string]bool{},
		// Replaced by Start; until then a pass has no lifetime to end with.
		base: context.Background(),
	}
}

// Start loads the queue and starts the first pass, which sends what came due while
// the daemon was down. An unreadable queue is reported and treated as empty.
//
// The load is synchronous, so no Add can save over a queue not yet read. The pass is
// not: an overdue send can take the homeserver's full timeout, and the socket must be
// serving meanwhile. It needs no join of its own: a pass registers under the lock Stop
// takes, so Stop either waits for it or it sees stopped and does nothing.
func (s *Scheduler) Start(ctx context.Context) {
	s.load(ctx)
	go s.pass(ctx)
}

// load reads the queue from the store.
func (s *Scheduler) load(ctx context.Context) {
	queue, err := s.store.Load()
	if err != nil {
		s.report(slog.LevelWarn, "scheduled messages: "+err.Error())
	}
	s.mu.Lock()
	s.queue = queue
	s.base = ctx
	s.mu.Unlock()
}

// Stop cancels the pending wake-up and waits for any pass still running, so none
// reaches a closed SQLite handle. Nil-safe.
func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Unlock()
	// Outside the lock: a pass in flight takes it.
	s.wg.Wait()
}

// List is the queue as it stands, soonest first.
func (s *Scheduler) List() []domain.ScheduledMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Clone(s.queue)
	domain.SortScheduled(out)
	return out
}

// Add queues a message; a past time means "send now". It takes no context on purpose:
// its caller's request context dies before the message is due.
func (s *Scheduler) Add(msg domain.ScheduledMessage) error {
	if msg.RoomID == "" {
		return fmt.Errorf("daemon: a scheduled message needs a room")
	}
	if msg.ID == "" {
		return fmt.Errorf("daemon: a scheduled message needs an id")
	}
	s.mu.Lock()
	s.queue = append(s.queue, msg)
	err := s.saveLocked()
	if err != nil {
		// Rolled back: the caller is about to be told this failed.
		s.queue = withoutID(s.queue, msg.ID)
	}
	base := s.base
	s.mu.Unlock()

	if err != nil {
		return fmt.Errorf("daemon: save schedule: %w", err)
	}
	s.pass(base)
	return nil
}

// Cancel drops one pending message; canceling one already gone is not an error.
func (s *Scheduler) Cancel(id string) error {
	s.mu.Lock()
	s.queue = withoutID(s.queue, id)
	delete(s.held, id)
	delete(s.failed, id)
	err := s.saveLocked()
	s.mu.Unlock()

	if err != nil {
		return fmt.Errorf("daemon: save schedule: %w", err)
	}
	s.rearm()
	return nil
}

// withoutID is the queue with one entry removed.
func withoutID(queue []domain.ScheduledMessage, id string) []domain.ScheduledMessage {
	return withoutIDs(queue, map[string]bool{id: true})
}

// withoutIDs is the queue with a set of entries removed.
func withoutIDs(queue []domain.ScheduledMessage, ids map[string]bool) []domain.ScheduledMessage {
	kept := make([]domain.ScheduledMessage, 0, len(queue))
	for i := range queue {
		if !ids[queue[i].ID] {
			kept = append(kept, queue[i])
		}
	}
	return kept
}

// saveLocked writes the queue to disk. s.mu must be held, and stays held across the
// write: snapshotting then writing unlocked lets an older snapshot win and resurrect
// a message a pass already claimed and sent — a duplicate send.
func (s *Scheduler) saveLocked() error {
	if err := s.store.Save(slices.Clone(s.queue)); err != nil {
		return fmt.Errorf("daemon: write the schedule queue: %w", err)
	}
	return nil
}

// pass sends everything due, reports everything too late, and arms the next wake-up.
// Startup and the timer both land here.
func (s *Scheduler) pass(ctx context.Context) {
	// The only goroutine where a panic in the send path would take the whole daemon
	// down (net/http already recovers handlers), so cap its cost.
	defer func() {
		if r := recover(); r != nil {
			s.report(slog.LevelError, fmt.Sprintf("the send queue hit a bug and skipped a pass: %v", r))
		}
	}()
	plan, announce, ok := s.plan()
	if !ok {
		return
	}
	defer s.wg.Done()

	for _, line := range announce {
		s.report(slog.LevelWarn, line)
	}

	// Overlapping passes plan from the same queue: the plan proposes, claim decides.
	sending, ok := s.claim(plan.Send)
	if !ok {
		s.rearm()
		return
	}

	unsent := s.sendAll(ctx, sending)
	if len(unsent) > 0 {
		s.restore(unsent)
	}
	s.setBackoff(len(unsent) > 0)
	s.rearm()
}

// plan registers a pass and plans it, under the lock Stop sets stopped under; false
// once stopped. The unlock is deferred so a recovered panic cannot leave s.mu held, and
// the pass is registered last so a panic cannot leave Stop waiting on it.
func (s *Scheduler) plan() (domain.SchedulePlan, []string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return domain.SchedulePlan{}, nil, false
	}
	plan := domain.PlanSchedule(s.queue, s.now(), s.cutoff())
	// Held messages stay queued for their author; each is reported once.
	var announce []string
	for i := range plan.Held {
		msg := &plan.Held[i]
		if !s.held[msg.ID] {
			s.held[msg.ID] = true
			// IDs and the time only: this line goes to the journal, the text never does.
			announce = append(announce, fmt.Sprintf(
				"scheduled message held: %s in %s, due %s (too late to send unasked)",
				msg.ID, msg.RoomID, msg.At.Format(time.RFC3339)))
		}
	}
	s.wg.Add(1)
	return plan, announce, true
}

// noteFailure records a failed attempt and returns the line to report, or "" when
// this message already failed the same way and the reader has been told.
func (s *Scheduler) noteFailure(msg domain.ScheduledMessage, err error) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed == nil {
		s.failed = map[string]string{}
	}
	text := err.Error()
	if s.failed[msg.ID] == text {
		return ""
	}
	s.failed[msg.ID] = text
	return fmt.Sprintf("message to %s failed, retrying: %v", msg.RoomID, err)
}

// noteSuccess clears a message's failure record and returns the line saying it
// finally went, or "" if it never failed.
func (s *Scheduler) noteSuccess(msg domain.ScheduledMessage) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed[msg.ID] == "" {
		return ""
	}
	delete(s.failed, msg.ID)
	return fmt.Sprintf("message to %s went out after all", msg.RoomID)
}

// sendAll sends what the pass claimed and returns what it could not, in order.
func (s *Scheduler) sendAll(ctx context.Context, sending []domain.ScheduledMessage) []domain.ScheduledMessage {
	var unsent []domain.ScheduledMessage
	for i := range sending {
		msg := sending[i]
		if ctx.Err() != nil {
			// Shutdown: claimed but never attempted, so it goes back.
			unsent = append(unsent, msg)
			continue
		}
		err := s.sender.Send(ctx, msg.RoomID, domain.Draft{
			Body:       msg.Body,
			Emote:      msg.Emote,
			ThreadRoot: msg.ThreadRoot,
			ReplyTo:    msg.ReplyTo,
			Mentions:   msg.Mentions,
			// The same txn ID on every try makes an uncertain retry safe.
			TxnID: msg.TxnID,
		})
		if err != nil {
			// Put back; the next pass retries until the cutoff catches it.
			if line := s.noteFailure(msg, err); line != "" {
				s.report(slog.LevelWarn, line)
			}
			unsent = append(unsent, msg)
			continue
		}
		if line := s.noteSuccess(msg); line != "" {
			s.report(slog.LevelInfo, line)
		}
	}
	return unsent
}

// setBackoff grows the retry wait after a pass that left something unsent, and drops
// it after one that did not.
func (s *Scheduler) setBackoff(failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !failed:
		s.backoff = 0
	case s.backoff == 0:
		s.backoff = retryFloor
	default:
		s.backoff = min(s.backoff*2, retryCeiling)
	}
}

// claim removes the messages about to be sent from the queue and saves that *before*
// the first send, reporting whether the pass may go ahead. Sending first once
// replayed the whole queue on every start when the state dir was read-only; a crash
// between write and send loses a message, which beats sending it twice.
func (s *Scheduler) claim(sending []domain.ScheduledMessage) ([]domain.ScheduledMessage, bool) {
	s.mu.Lock()
	// Only what is still queued, under the lock, so exactly one overlapping pass wins.
	present := make(map[string]bool, len(s.queue))
	for i := range s.queue {
		present[s.queue[i].ID] = true
	}
	claimed := make(map[string]bool, len(sending))
	mine := make([]domain.ScheduledMessage, 0, len(sending))
	for i := range sending {
		if present[sending[i].ID] {
			claimed[sending[i].ID] = true
			mine = append(mine, sending[i])
		}
	}
	if len(mine) == 0 {
		s.mu.Unlock()
		return nil, true // another pass took them; nothing to do and nothing wrong
	}

	s.queue = withoutIDs(s.queue, claimed)
	err := s.saveLocked()
	if err != nil {
		// Nothing sent yet; append back so a message added meanwhile survives.
		s.queue = append(s.queue, mine...)
	}
	s.mu.Unlock()

	if err != nil {
		s.report(slog.LevelWarn, "scheduled messages: queue could not be written, nothing sent: "+err.Error())
		return nil, false
	}
	return mine, true
}

// restore returns claimed messages that were not sent after all. A failed save here
// means late, not twice.
func (s *Scheduler) restore(msgs []domain.ScheduledMessage) {
	s.mu.Lock()
	s.queue = append(s.queue, msgs...)
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		s.report(slog.LevelWarn, "scheduled messages: could not return them to the queue: "+err.Error())
	}
}

// rearm schedules one wake-up for the next message due, or none.
func (s *Scheduler) rearm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.stopped {
		return
	}
	plan := domain.PlanSchedule(s.queue, s.now(), s.cutoff())
	wait, armed := time.Duration(0), false
	if !plan.Next.IsZero() {
		wait, armed = max(plan.Next.Sub(s.now()), 0), true
	}
	// A failed message is already due and absent from plan.Next; the retry wait brings
	// the pass back for it, whichever is sooner.
	if s.backoff > 0 && len(plan.Send) > 0 {
		if !armed || s.backoff < wait {
			wait, armed = s.backoff, true
		}
	}
	if !armed {
		return
	}
	base := s.base
	s.timer = time.AfterFunc(wait, func() {
		if base.Err() != nil {
			return
		}
		s.pass(base)
	})
}
