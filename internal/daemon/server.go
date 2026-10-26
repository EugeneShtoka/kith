package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
)

const (
	readHeaderTimeout = 30 * time.Second
	// shutdownTimeout is how long a graceful shutdown waits for handlers. A stream whose
	// client stopped reading is blocked in a write; only closing the connection frees it.
	shutdownTimeout = 10 * time.Second
)

// Daemon is everything the socket serves: the backend plus facts about this process
// that api.Backend cannot answer. Every part is required (Serve checks).
type Daemon struct {
	Backend api.Backend
	// Streams fans each backend channel out to every subscriber.
	Streams *Streams
	// State is readiness: whether a sync has landed, and what broke if not.
	State *State
	// Notifications delivers alerts and holds the do-not-disturb rules.
	Notifications *Notifications
	// Reload re-reads the config file.
	Reload Reload
	// CheckConfig judges a config a client means to write.
	CheckConfig CheckConfig
	// Config is the config file, written only by the daemon.
	Config *ConfigFile
	// Scheduler is the send-later queue.
	Scheduler *Scheduler
	// Logins logs network accounts in.
	Logins api.Logins
	// Log receives every failed call with procedure and reason, so a failure a client
	// only counted reaches the journal.
	Log *slog.Logger
}

// missing names the parts d lacks.
func (d *Daemon) missing() []string {
	var out []string
	for name, absent := range map[string]bool{
		"Backend": d.Backend == nil, "Streams": d.Streams == nil, "State": d.State == nil,
		"Notifications": d.Notifications == nil, "Reload": d.Reload == nil,
		"CheckConfig": d.CheckConfig == nil, "Config": d.Config == nil,
		"Scheduler": d.Scheduler == nil, "Logins": d.Logins == nil, "Log": d.Log == nil,
	} {
		if absent {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// Serve hosts d on ln until ctx is canceled, then shuts down gracefully; the caller
// runs Streams.Run. No authentication: the socket is 0600.
func Serve(ctx context.Context, ln net.Listener, d *Daemon) error {
	if missing := d.missing(); len(missing) > 0 {
		_ = ln.Close() // nothing will be served on it
		return fmt.Errorf("daemon: cannot serve without %s", strings.Join(missing, ", "))
	}
	// Handlers outliving their request (MarkRoomsRead) run on this, so a broken listener
	// can end them while the caller's ctx is still live.
	ctx, cancelHandlers := context.WithCancel(ctx)
	defer cancelHandlers()
	mux := http.NewServeMux()
	mux.Handle(backendv1connect.NewBackendServiceHandler(&server{Daemon: d, lifetime: ctx, seats: newSeats()},
		connect.WithInterceptors(failureLog{log: d.Log}, profileGate{profiles: d.Notifications})))
	// Every handler is counted so none runs after Serve returns and the caller closes
	// the stores, and none starts once Serve is ending.
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
		// The listener broke: cut handlers off, end those on the daemon's lifetime, and wait
		// before the caller closes the stores.
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
		// A client that stopped reading (a suspended TUI) holds its stream in a write: cut
		// every connection and wait for the handlers to fail their writes.
		_ = srv.Close() // ignored: Shutdown already closed the listener, and conns close regardless
		inflight.shut()
		if !drained(&inflight, timeout) {
			return fmt.Errorf("daemon: shutdown: still running %s after closing every connection: %w", timeout, ErrHandlersRunning)
		}
		d.Log.Warn("shutdown closed connections a client had stopped reading", "timeout", timeout)
		return nil
	}
}

// ErrHandlersRunning means Serve gave up on unfinished handlers. They may be
// mid-write, so the caller must leave the stores open and let process exit release
// them: closing the crypto store under a write can corrupt it.
var ErrHandlersRunning = errors.New("handlers still running")

// drained waits for the gate's count to reach zero, giving up after timeout.
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

// handlerGate counts running handlers and, once shut, admits no more. Not a
// WaitGroup: a request read before the server closed could otherwise start after the
// drain saw none running, as the caller closes the stores.
type handlerGate struct {
	mu     sync.Mutex
	n      int
	closed bool
}

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

// failureLog logs each failed call: the client shows a short line or nothing, so the
// journal keeps the reason.
type failureLog struct{ log *slog.Logger }

func (f failureLog) report(ctx context.Context, procedure string, err error) {
	if err == nil {
		return
	}
	// The client leaving, or our own shutdown, is not a failure of the call; nor is a
	// network saying it cannot do what was asked (a client may ask every network, as the
	// folder offer after a login does).
	level := slog.LevelWarn
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || connect.CodeOf(err) == connect.CodeCanceled ||
		errors.Is(err, api.ErrNotOnNetwork) {
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

// WrapStreamingClient is unused: the interceptor is server-only.
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

// rpcErr maps a backend error onto a Connect error, tagging contract sentinels so the
// client can restore them (see sentinel.go).
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

// reply turns a backend result into a handler result; msg is discarded when err is set.
func reply[T any](msg *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(msg), nil
}

// serveStream forwards one hub to one server stream until the client leaves or the
// hub closes, always releasing the subscription. A value frame makes nil is not this
// client's, and is skipped.
func serveStream[T, R any](ctx context.Context, h *hub[T], stream *connect.ServerStream[R], frame func(T) *R) error {
	return serveStreamWith(ctx, h, subBuffer, stream, frame)
}

// serveStreamWith is serveStream queueing up to buffer values for a client that is
// slow to read.
func serveStreamWith[T, R any](ctx context.Context, h *hub[T], buffer int, stream *connect.ServerStream[R], frame func(T) *R) error {
	id, ch := h.subscribeWith(buffer)
	defer h.release(id)
	// Send headers now: the client bounds its wait for them (answerTimeout), and a stream
	// can be silent for hours.
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
			out := frame(v)
			if out == nil {
				continue // not for this client
			}
			if err := stream.Send(out); err != nil {
				return rpcErr(fmt.Errorf("send stream frame: %w", err))
			}
		}
	}
}
