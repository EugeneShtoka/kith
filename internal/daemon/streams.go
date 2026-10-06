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
	messages      *hub[domain.Message]
	unread        *hub[domain.Unread]
	reactions     *hub[domain.ReactionUpdate]
	invites       *hub[[]domain.Room]
	verifications *hub[domain.Verification]
	// activity (typing, receipts) has no paired cache read.
	activity *hub[domain.Activity]
	// follows carries URIs from `kith --open` to attached TUIs. It has no backend
	// channel (an RPC feeds it; see Follow), so Run does not pump it and Attached
	// does not count it.
	follows *hub[string]
	// rooms tells attached clients a network rewrote its rooms (a chat begun, renamed,
	// archived on the phone), so they read the list again. Fed by RoomsChanged, not a
	// backend channel, like follows.
	rooms *hub[struct{}]
	// configs carries the configuration after each change (Configured), like follows.
	configs *hub[config.Snapshot]
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
		follows:       newHub[string](),
		rooms:         newHub[struct{}](),
		configs:       newHub[config.Snapshot](),
	}
}

// Run pumps every backend channel into its hub until ctx is canceled or the backend
// closes them, then ends every stream. It blocks.
func (s *Streams) Run(ctx context.Context, b api.Backend) {
	var wg sync.WaitGroup
	wg.Go(func() { s.messages.pump(ctx, b.Messages()) })
	wg.Go(func() { s.unread.pump(ctx, b.Unread()) })
	wg.Go(func() { s.reactions.pump(ctx, b.Reactions()) })
	wg.Go(func() { s.invites.pump(ctx, b.Invites()) })
	wg.Go(func() { s.verifications.pump(ctx, b.Verifications()) })
	wg.Go(func() { s.activity.pump(ctx, b.Activity()) })
	wg.Wait()
	s.follows.end() // no backend channel ends these otherwise
	s.rooms.end()
	s.configs.end()
}

// notifierBuffer is the in-process consumer's queue. Its delivery can be slow (a desktop
// notification over D-Bus, a spam verdict over the network), and a burst after a
// suspend is hundreds of messages at once: a message it misses is a mention nobody
// is told of, and nothing reads it again.
const notifierBuffer = 4096

// SubscribeMessages attaches an in-process consumer (the notifier) to the message
// stream, returning its id and channel. It does not affect Attached.
func (s *Streams) SubscribeMessages() (uint64, <-chan domain.Message) {
	return s.messages.subscribeWith(notifierBuffer)
}

// ReleaseMessages detaches a subscriber taken out by SubscribeMessages.
func (s *Streams) ReleaseMessages(id uint64) { s.messages.release(id) }

// Attached reports how many clients are fully attached: the minimum subscriber
// count across the five streams, since a client subscribes to them one by one.
func (s *Streams) Attached() int {
	return min(
		s.messages.subscribers(),
		s.unread.subscribers(),
		s.reactions.subscribers(),
		s.invites.subscribers(),
		s.verifications.subscribers(),
	)
}

// Configured tells every attached client the configuration after a change.
func (s *Streams) Configured(snap config.Snapshot) { s.configs.broadcast(snap) }

// RoomsChanged tells every attached client that a network rewrote its rooms.
func (s *Streams) RoomsChanged() { s.rooms.broadcast(struct{}{}) }

// Follow hands a URI to every attached client and reports whether there was one;
// `kith --open` starts a terminal when there was not.
func (s *Streams) Follow(uri string) bool {
	if s.follows.subscribers() == 0 {
		return false
	}
	s.follows.broadcast(uri)
	return true
}
