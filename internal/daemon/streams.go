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
	// rooms tells clients a network rewrote its rooms so they reread the list. It has no
	// backend channel (RoomsChanged feeds it), so Run does not pump it and Attached does
	// not count it.
	rooms *hub[struct{}]
	// configs carries the configuration after each change (Configured), like rooms.
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
		rooms:         newHub[struct{}](),
		configs:       newHub[config.Snapshot](),
	}
}

// Run pumps every backend channel into its hub until ctx ends or the backend closes
// them, then ends every stream. It blocks.
func (s *Streams) Run(ctx context.Context, b api.Backend) {
	var wg sync.WaitGroup
	wg.Go(func() { s.messages.pump(ctx, b.Messages()) })
	wg.Go(func() { s.unread.pump(ctx, b.Unread()) })
	wg.Go(func() { s.reactions.pump(ctx, b.Reactions()) })
	wg.Go(func() { s.invites.pump(ctx, b.Invites()) })
	wg.Go(func() { s.verifications.pump(ctx, b.Verifications()) })
	wg.Go(func() { s.activity.pump(ctx, b.Activity()) })
	wg.Wait()
	s.rooms.end() // no backend channel ends these
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
