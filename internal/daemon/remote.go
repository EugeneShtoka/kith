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
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// streamBuffer bounds each daemon→UI channel. This side blocks when full; the
// daemon's hub decides what to drop.
const streamBuffer = 64

// Remote is the api.Backend the TUI runs against: methods are RPCs over the daemon's
// unix socket and streams are subscriptions exposed as channels. Methods are
// documented on api.Backend.
type Remote struct {
	c backendv1connect.BackendServiceClient

	msgs *stream[domain.Message]
	// read is where the message stream stands: the last change delivered, from which a
	// reattached stream resumes. Nil until the stream first opens.
	read          atomic.Pointer[readAt]
	unread        *stream[domain.Unread]
	reactions     *stream[domain.ReactionUpdate]
	invites       *stream[[]domain.Room]
	verifications *stream[domain.Verification]
	activity      *stream[domain.Activity]
	// follows carries links handed over by a second kith process; not in api.Backend.
	follows *stream[string]
	// changes says what changed in the daemon (rooms, accounts); not in api.Backend.
	changes *stream[domain.Change]
	// configs is the configuration after each change; not in api.Backend.
	configs  *stream[config.Snapshot]
	attached *stream[bool]
	// client names this Remote to the daemon. seated is set once it takes the seat and
	// names its draft writes from then on (kith-mcp takes none and writes unnamed). lost
	// closes when another window takes the seat.
	client   string
	seated   atomic.Bool
	lost     chan struct{}
	loseOnce sync.Once
	// profile is the [[profile]] named on every request; "" is the first.
	profile atomic.Pointer[string]
	// over closes once, when the streams stop for good.
	over     chan struct{}
	overOnce sync.Once

	mu     sync.Mutex
	cancel context.CancelFunc
	// leaveSeat ends the seat stream; Stop calls it. where is this window's whereabouts,
	// asked for again when a switch moves the seat.
	leaveSeat context.CancelFunc
	where     domain.SeatHolder
	started   bool
	stopped   bool
	// attempt is the streams' current attachment, opened in one profile; begun closes
	// when the next begins. switchedOff is the last one a profile switch ended.
	attempt     streamAttempt
	begun       chan struct{}
	switchedOff chan struct{}
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
	r := &Remote{
		msgs:          newStream[domain.Message](),
		unread:        newStream[domain.Unread](),
		reactions:     newStream[domain.ReactionUpdate](),
		invites:       newStream[[]domain.Room](),
		verifications: newStream[domain.Verification](),
		activity:      newStream[domain.Activity](),
		follows:       newStream[string](),
		changes:       newStream[domain.Change](),
		configs:       newStream[config.Snapshot](),
		attached:      newStream[bool](),
		client:        rand.Text(),
		lost:          make(chan struct{}),
		over:          make(chan struct{}),
	}
	r.c = backendv1connect.NewBackendServiceClient(hc, socketBaseURL,
		connect.WithInterceptors(profileName{name: r.profileName}))
	return r
}

// UseProfile names the [[profile]] every request from now on runs in; "" is the first.
func (r *Remote) UseProfile(name string) { r.profile.Store(&name) }

func (r *Remote) profileName() string {
	if p := r.profile.Load(); p != nil {
		return *p
	}
	return ""
}

// stream is one daemon→UI channel, closed exactly once; listeners treat closed as
// the end.
type stream[T any] struct {
	ch   chan T
	once sync.Once
}

func newStream[T any]() *stream[T] {
	return &stream[T]{ch: make(chan T, streamBuffer)}
}

func (s *stream[T]) done() { s.once.Do(func() { close(s.ch) }) }

// call runs one unary RPC, wrapping a failure with op and restoring sentinels. On
// error the message is nil; proto getters on it are safe.
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
