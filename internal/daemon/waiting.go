package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"connectrpc.com/connect"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
)

// ServeWaiting answers on ln while the daemon cannot serve yet (its cache will not
// open): Status as not ready, with why() as the error a client shows, and every other
// call refused as unavailable for the same reason. It returns once ctx ends, having
// closed ln.
func ServeWaiting(ctx context.Context, ln net.Listener, why func() string) error {
	mux := http.NewServeMux()
	mux.Handle(backendv1connect.NewBackendServiceHandler(waiting{why: why}, connect.WithInterceptors(waitingGate{why: why})))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("daemon: serve while waiting: %w", err)
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutCtx) // only Status runs here, and it does not block
		return nil
	}
}

// waiting is the backend while the daemon waits: only Status answers.
type waiting struct {
	backendv1connect.UnimplementedBackendServiceHandler
	why func() string
}

func (w waiting) Status(context.Context, *connect.Request[v1.StatusRequest]) (*connect.Response[v1.StatusResponse], error) {
	return connect.NewResponse(&v1.StatusResponse{LastError: w.why()}), nil
}

// waitingGate refuses every call but Status as unavailable, saying why.
type waitingGate struct{ why func() string }

func (g waitingGate) refused(procedure string) error {
	if procedure == backendv1connect.BackendServiceStatusProcedure {
		return nil
	}
	return connect.NewError(connect.CodeUnavailable, errors.New(g.why()))
}

func (g waitingGate) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := g.refused(req.Spec().Procedure); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient is unused: the gate is server-only.
func (g waitingGate) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (g waitingGate) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := g.refused(conn.Spec().Procedure); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}
