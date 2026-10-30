package daemon

import (
	"context"
	"sync"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Streams owns the fan-out for every channel api.Backend exposes (see hub).
type Streams struct {
	// messages feeds the in-process notifier; clients read the cache's changes instead
	// (tail).
	messages      *hub[domain.Message]
	unread        *hub[domain.Unread]
	reactions     *hub[domain.ReactionUpdate]
	invites       *hub[[]domain.Room]
	verifications *hub[domain.Verification]
	// activity (typing, receipts) has no paired cache read.
	activity *hub[domain.Activity]
	// changes tells clients what changed (a network's rooms, an account's state, a
	// profile's rail) so they read it again. It has no backend channel (RoomsChanged,
	// AccountsChanged and railChanged feed it), so Run does not pump it and Attached does
	// not count it.
	changes *hub[change]
	// configs carries the configuration after each change (Configured), like rooms.
	configs *hub[config.Snapshot]
	// tail wakes the clients' message streams when the cache numbered more changes.
	tail *tail
}

// NewStreams returns Streams with nothing attached. Subscribing before Run is safe.
func NewStreams() *Streams {
	return &Streams{
		messages:      newHub[domain.Message](),
		unread:        newHub[domain.Unread](),
		reactions:     newHub[domain.ReactionUpdate](),
		invites:       newHub[[]domain.Room](),
		verifications: newHub[domain.Verification](),
		activity:      newHub[domain.Activity](),
		changes:       newHub[change](),
		configs:       newHub[config.Snapshot](),
		tail:          newTail(),
	}
}

// Run pumps every backend channel into its hub until ctx ends or the backend closes
// them, then ends every stream. It blocks. The clients' message streams end with the
// backend's messages, as they did when they were its fan-out.
func (s *Streams) Run(ctx context.Context, b api.Backend) {
	var wg sync.WaitGroup
	wg.Go(func() {
		s.messages.pump(ctx, b.Messages())
		s.tail.end()
	})
	wg.Go(func() { s.unread.pump(ctx, b.Unread()) })
	wg.Go(func() { s.reactions.pump(ctx, b.Reactions()) })
	wg.Go(func() { s.invites.pump(ctx, b.Invites()) })
	wg.Go(func() { s.verifications.pump(ctx, b.Verifications()) })
	wg.Go(func() { s.activity.pump(ctx, b.Activity()) })
	wg.Wait()
	s.changes.end() // no backend channel ends these
	s.configs.end()
}

// notifierBuffer is the in-process consumer's queue. Its delivery can be slow (D-Bus,
// a spam verdict over the network) and a post-suspend burst is hundreds of messages;
// a missed message is a mention nobody is told of.
const notifierBuffer = 4096

// SubscribeMessages attaches an in-process consumer (the notifier) to the message
// stream. It does not affect Attached.
func (s *Streams) SubscribeMessages() (uint64, <-chan domain.Message) {
	return s.messages.subscribeWith(notifierBuffer)
}

// ReleaseMessages detaches a subscriber taken out by SubscribeMessages.
func (s *Streams) ReleaseMessages(id uint64) { s.messages.release(id) }

// Attached reports how many clients are fully attached: the minimum subscriber count
// across the five streams, since a client subscribes one by one.
func (s *Streams) Attached() int {
	return min(
		s.tail.readers(),
		s.unread.subscribers(),
		s.reactions.subscribers(),
		s.invites.subscribers(),
		s.verifications.subscribers(),
	)
}

// Configured tells every attached client the configuration after a change.
func (s *Streams) Configured(snap config.Snapshot) { s.configs.broadcast(snap) }

// change is one thing that changed, and the profile whose clients it is for ("" every
// client's).
type change struct {
	what    domain.Change
	profile string
}

// RoomsChanged tells every attached client that a network rewrote its rooms.
func (s *Streams) RoomsChanged() { s.changes.broadcast(change{what: domain.ChangeRooms}) }

// AccountsChanged tells every attached client that a network account's state changed.
func (s *Streams) AccountsChanged() { s.changes.broadcast(change{what: domain.ChangeAccounts}) }

// railChanged tells the clients running in profile that its rail changed.
func (s *Streams) railChanged(profile string) {
	s.changes.broadcast(change{what: domain.ChangeRail, profile: profile})
}

// MessagesChanged wakes the clients' message streams: the cache numbered more changes,
// or started a new epoch (db.Cache.OnChanged).
func (s *Streams) MessagesChanged() { s.tail.ring() }

// tail is what a client's message stream waits on between reads of the cache. A reader
// takes the bell before it reads, so a change committed while it reads rings one it
// already holds.
type tail struct {
	mu   sync.Mutex
	bell chan struct{}
	// ended means the backend's messages ended: every stream ends with them.
	ended bool
	count int
}

func newTail() *tail { return &tail{bell: make(chan struct{})} }

// join counts a reader in; leave counts it out.
func (t *tail) join() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
}

func (t *tail) leave() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count--
}

func (t *tail) readers() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// next is the bell the next change rings, and whether the stream has ended.
func (t *tail) next() (<-chan struct{}, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bell, t.ended
}

// ring wakes every reader waiting on the current bell and hangs a new one.
func (t *tail) ring() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return
	}
	close(t.bell)
	t.bell = make(chan struct{})
}

// end wakes every reader for the last time.
func (t *tail) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ended {
		t.ended = true
		close(t.bell)
	}
}
