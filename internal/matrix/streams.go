package matrix

import (
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix/event"
)

// Messages streams incoming room messages to the UI.
func (b *InProc) Messages() <-chan domain.Message { return b.out.msgs }

// streams is what the backend publishes to its client, and the lock that keeps a
// send off a closed channel. Sends are not all on the sync goroutine (recount from
// RPCs; mautrix's waitForSession re-dispatch). Holding the read lock across a send is
// safe only because every send is non-blocking.
type streams struct {
	mu        sync.RWMutex
	closed    bool
	msgs      chan domain.Message
	unread    chan domain.Unread
	reactions chan domain.ReactionUpdate
	activity  chan domain.Activity
	invites   chan []domain.Room
}

// open makes the channels, each buffered so sync never waits on the client. Before
// it, every channel is nil and emit drops what it is given.
func (s *streams) open() {
	s.msgs = make(chan domain.Message, messageBuffer)
	s.unread = make(chan domain.Unread, messageBuffer)
	s.reactions = make(chan domain.ReactionUpdate, messageBuffer)
	s.activity = make(chan domain.Activity, messageBuffer)
	s.invites = make(chan []domain.Room, inviteBuffer)
}

// emit delivers v unless the streams are closed (a send on a closed channel panics).
// Lossy on a full buffer: every stream has a cache read behind it.
func emit[T any](s *streams, ch chan T, v T) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case ch <- v:
	default:
	}
}

// close closes every channel once, under the write lock so concurrent sends see
// closed. The verification channel is not here: its one send selects on the verification context.
func (s *streams) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.msgs == nil {
		return
	}
	s.closed = true
	close(s.msgs)
	close(s.unread)
	close(s.reactions)
	close(s.activity)
	close(s.invites)
}

// Reactions streams reaction adds/removals to the UI as sync delivers them.
func (b *InProc) Reactions() <-chan domain.ReactionUpdate { return b.out.reactions }

// Activity streams typing and other people's read receipts as sync delivers them.
func (b *InProc) Activity() <-chan domain.Activity { return b.out.activity }

// Attached never fires in-process: a closed channel means "this will not happen".
func (b *InProc) Attached() <-chan bool { return neverAttaches }

var neverAttaches = func() chan bool {
	ch := make(chan bool)
	close(ch)
	return ch
}()

// emitActivity publishes a room's typing state, only when the batch has a typing event.
func (b *InProc) emitActivity(roomID domain.RoomID, events []*event.Event) {
	typing, present := typingIn(events, b.client.UserID)
	if !present {
		return
	}
	emit(&b.out, b.out.activity, domain.Activity{RoomID: roomID, Typing: typing})
}
