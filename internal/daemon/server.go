package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
)

const (
	readHeaderTimeout = 30 * time.Second
	// shutdownTimeout is how long a graceful shutdown waits for handlers. A stream
	// ends with the base context, unless its client stopped reading: then it is blocked
	// in a write, and only closing the connection frees it.
	shutdownTimeout = 10 * time.Second
)

// Daemon is everything the socket serves: the Matrix backend plus facts about
// this process that api.Backend cannot answer.
type Daemon struct {
	Backend api.Backend
	// Streams fans each backend channel out to every subscriber.
	Streams *Streams
	// State is readiness: whether a sync has landed, and what broke if not.
	State *State
	// Notifications may be nil in a test harness; its handlers then refuse.
	Notifications *Notifications
	// Reload re-reads the config file; nil when the daemon was given none.
	Reload Reload
	// Scheduler is nil when the queue path could not be resolved; its handlers
	// then refuse.
	Scheduler *Scheduler
	// Logins logs network accounts in; nil refuses every login with ErrNetworkOff.
	Logins api.Logins
	// Log receives every failed call, with the procedure and the reason, so a
	// failure a client only counted still reaches the journal. nil logs nothing.
	Log *slog.Logger
}

// Serve hosts d on ln until ctx is canceled, then shuts down gracefully. Feeding
// Streams is the caller's job (Streams.Run).
//
// There is no authentication: the socket is 0600, so the kernel already decided
// who may connect.
func Serve(ctx context.Context, ln net.Listener, d *Daemon) error {
	// Handlers that outlive their request (MarkRoomsRead) run on this, so a broken
	// listener can end them too: the caller's ctx is still live then.
	ctx, cancelHandlers := context.WithCancel(ctx)
	defer cancelHandlers()
	mux := http.NewServeMux()
	mux.Handle(backendv1connect.NewBackendServiceHandler(&server{Daemon: d, lifetime: ctx, seat: &seat{}},
		connect.WithInterceptors(failureLog{log: d.Log})))
	// Every handler is counted, so none is left running once Serve has returned and
	// the caller closes the stores under it; and once Serve is ending, none starts.
	var inflight handlerGate
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !inflight.enter() {
				http.Error(w, "daemon: shutting down", http.StatusServiceUnavailable)
				return
			}
			defer inflight.leave()
			mux.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		// The listener broke while handlers may still be running: cut them off, end
		// the ones on the daemon's lifetime, and wait, as a shutdown does, before the
		// caller closes the stores.
		_ = srv.Close() // ignored: the serve error is the one to report
		inflight.shut()
		cancelHandlers()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			err = fmt.Errorf("daemon: serve: %w", err)
		} else {
			err = nil
		}
		if !drained(&inflight, shutdownTimeout) {
			return errors.Join(err, ErrHandlersRunning)
		}
		return err
	case <-ctx.Done():
		timeout := shutdownTimeout
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		if srv.Shutdown(shutCtx) == nil {
			return nil
		}
		// A client that stopped reading (a suspended TUI) holds its stream in a write.
		// That is the client's doing, not a daemon failure: cut every connection and wait
		// for the handlers, which now fail their writes or see their canceled context.
		_ = srv.Close() // ignored: Shutdown already closed the listener, and conns close regardless
		inflight.shut()
		if !drained(&inflight, timeout) {
			return fmt.Errorf("daemon: shutdown: still running %s after closing every connection: %w", timeout, ErrHandlersRunning)
		}
		if d.Log != nil {
			d.Log.Warn("shutdown closed connections a client had stopped reading", "timeout", timeout)
		}
		return nil
	}
}

// ErrHandlersRunning is Serve giving up on handlers that did not finish. They may be
// mid-write, so the caller must leave the stores open and let the process exit
// release them: closing the crypto store under a write can corrupt it.
var ErrHandlersRunning = errors.New("handlers still running")

// drained waits for n to reach zero, giving up after timeout.
func drained(g *handlerGate, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for g.running() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// handlerGate counts running handlers and, once shut, admits no more. A counter, not
// a WaitGroup: a handler may still arrive while shutdown waits. A request already
// read when the server closed would otherwise start after the drain saw none running,
// as the caller closes the stores.
type handlerGate struct {
	mu     sync.Mutex
	n      int
	closed bool
}

// enter admits a handler, or refuses one once the gate is shut.
func (g *handlerGate) enter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.n++
	return true
}

func (g *handlerGate) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n--
}

// shut admits no more handlers.
func (g *handlerGate) shut() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
}

func (g *handlerGate) running() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

// failureLog logs each call that fails: a client shows a short line (or nothing), and
// the journal is where its reason must survive.
type failureLog struct{ log *slog.Logger }

func (f failureLog) report(ctx context.Context, procedure string, err error) {
	if err == nil || f.log == nil {
		return
	}
	// The client leaving, or our own shutdown, is not a failure of the call.
	level := slog.LevelWarn
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || connect.CodeOf(err) == connect.CodeCanceled {
		level = slog.LevelDebug
	}
	f.log.Log(ctx, level, "call failed", "op", path.Base(procedure), "err", err)
}

func (f failureLog) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		f.report(ctx, req.Spec().Procedure, err)
		return res, err
	}
}

// WrapStreamingClient is unused: the interceptor is installed on the server only.
func (f failureLog) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (f failureLog) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		err := next(ctx, conn)
		f.report(ctx, conn.Spec().Procedure, err)
		return err
	}
}

// rpcErr maps a backend error onto a Connect error, tagging contract sentinels so
// the client can restore their identity (see sentinel.go).
func rpcErr(err error) error {
	if err == nil {
		return nil
	}
	code := connect.CodeInternal
	switch {
	case errors.Is(err, context.Canceled):
		code = connect.CodeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		code = connect.CodeDeadlineExceeded
	}
	cerr := connect.NewError(code, err)
	if name, ok := sentinelName(err); ok {
		cerr.Meta().Set(sentinelHeader, name)
	}
	return cerr
}

// reply turns a backend result into a handler result. msg may be built from a
// zero value when err is set; it is discarded.
func reply[T any](msg *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(msg), nil
}

// serveStream forwards one hub to one server stream until the client leaves or
// the hub closes, always releasing the subscription.
func serveStream[T, R any](ctx context.Context, h *hub[T], stream *connect.ServerStream[R], frame func(T) *R) error {
	id, ch := h.subscribe()
	defer h.release(id)
	// Headers now, not with the first event: the client bounds its wait for them
	// (answerTimeout), and a stream can have nothing to say for hours.
	if err := stream.Send(nil); err != nil {
		return rpcErr(fmt.Errorf("open stream: %w", err))
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case v, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(frame(v)); err != nil {
				return rpcErr(fmt.Errorf("send stream frame: %w", err))
			}
		}
	}
}
