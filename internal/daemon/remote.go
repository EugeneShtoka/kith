package daemon

import (
	"context"
	"crypto/rand"
	"net/http"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// streamBuffer bounds each daemon→UI channel. This side blocks rather than
// drops when it fills; the daemon's hub decides what to drop per subscriber.
const streamBuffer = 64

// Remote is the api.Backend the TUI runs against: every method is a request over
// the daemon's unix socket, and every stream is a subscription re-exposed as a Go
// channel. Its methods are documented where they are declared, on api.Backend.
type Remote struct {
	c backendv1connect.BackendServiceClient

	msgs          *stream[domain.Message]
	unread        *stream[domain.Unread]
	reactions     *stream[domain.ReactionUpdate]
	invites       *stream[[]domain.Room]
	verifications *stream[domain.Verification]
	activity      *stream[domain.Activity]
	// follows carries links handed over by a second kith process; not part of
	// api.Backend.
	follows *stream[string]
	// rooms says a network rewrote its rooms (RoomsChanged); not part of api.Backend.
	rooms *stream[struct{}]
	// attached reports losing and regaining the daemon (see Attached).
	attached *stream[bool]
	// client names this Remote to the daemon for its whole life. seated is set once
	// it has the seat (TakeSeat), and names its draft writes from then on: a client
	// that takes none (kith-mcp) writes unnamed. lost is closed when another window
	// takes the seat.
	client   string
	seated   atomic.Bool
	lost     chan struct{}
	loseOnce sync.Once

	mu     sync.Mutex
	cancel context.CancelFunc
	// leaveSeat ends the seat stream (TakeSeat); Stop calls it.
	leaveSeat context.CancelFunc
	started   bool
	stopped   bool
}

var (
	_ api.Backend = (*Remote)(nil)
	_ api.Seat    = (*Remote)(nil)
)

// NewRemote returns a Remote for the daemon socket at path. It performs no I/O.
func NewRemote(path string) *Remote {
	return newRemote(Dial(path))
}

func newRemote(hc *http.Client) *Remote {
	return &Remote{
		c:             backendv1connect.NewBackendServiceClient(hc, socketBaseURL),
		msgs:          newStream[domain.Message](),
		unread:        newStream[domain.Unread](),
		reactions:     newStream[domain.ReactionUpdate](),
		invites:       newStream[[]domain.Room](),
		verifications: newStream[domain.Verification](),
		activity:      newStream[domain.Activity](),
		follows:       newStream[string](),
		rooms:         newStream[struct{}](),
		attached:      newStream[bool](),
		client:        rand.Text(),
		lost:          make(chan struct{}),
	}
}

// stream is one daemon→UI channel, closed exactly once: listeners treat a closed
// channel as the end.
type stream[T any] struct {
	ch   chan T
	once sync.Once
}

func newStream[T any]() *stream[T] {
	return &stream[T]{ch: make(chan T, streamBuffer)}
}

func (s *stream[T]) done() { s.once.Do(func() { close(s.ch) }) }

// call runs one unary RPC, wrapping a failure with op (restoring sentinels, see
// callErr). On error the returned message is nil; proto getters on it are safe.
func call[Req, Res any](
	ctx context.Context, op string,
	rpc func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error),
	msg *Req,
) (*Res, error) {
	resp, err := rpc(ctx, connect.NewRequest(msg))
	if err != nil {
		return nil, callErr(op, err)
	}
	return resp.Msg, nil
}
