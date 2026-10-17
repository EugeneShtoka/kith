// Package signalhelper is kith-signal's process: the SignalService it serves kithd over
// a socket kithd hands it, and the heartbeat that ends it when kithd is gone. Signal
// itself sits behind Network, which only a binary built with the signal tag implements
// (internal/signalhelper/meow): the rest builds, and is tested, without cgo.
package signalhelper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/api/signal/v1/signalv1connect"
)

// Device is a Signal account the helper is linked to as a secondary device.
type Device struct {
	ACI    string // Signal's own ID for the account (a UUID)
	Number string // E.164
}

// Network is Signal as the helper reaches it.
type Network interface {
	// Open opens the key store with key and lists the devices linked in it.
	Open(ctx context.Context, key []byte) ([]Device, error)
	// Link makes the helper a linked device named name, calling show with each link to
	// scan (a new one when the last lapses) until the phone scans one.
	Link(ctx context.Context, name string, show func(url string) error) (Device, error)
	// Connect connects the linked account aci, handing sink its events from then on,
	// until the helper ends.
	Connect(ctx context.Context, aci string, sink Sink) error
}

// readHeaderTimeout bounds a request's headers; kithd sends them at once.
const readHeaderTimeout = 30 * time.Second

// Serve serves network on ln until ctx ends. When no heartbeat has come for lapse
// (counted from the start, so a kithd that never calls is noticed too), it calls
// lapsed, which in the helper exits the process on the spot: anything the helper is
// holding then stays unacknowledged, and Signal sends it again.
func Serve(ctx context.Context, ln net.Listener, network Network, lapse time.Duration, lapsed func(), log *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	beats := make(chan struct{}, 1)
	go watch(ctx, beats, lapse, lapsed)
	mux := http.NewServeMux()
	mux.Handle(signalv1connect.NewSignalServiceHandler(&service{network: network, beats: beats, log: log, out: newOutbox()}))
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return fmt.Errorf("signal helper: serve: %w", err)
	case <-ctx.Done():
		_ = srv.Close() // ending: the helper's handlers have nothing to finish
		<-errc
		return nil
	}
}

// watch calls lapsed once no beat has come for lapse.
func watch(ctx context.Context, beats <-chan struct{}, lapse time.Duration, lapsed func()) {
	timer := time.NewTimer(lapse)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beats:
			timer.Reset(lapse)
		case <-timer.C:
			lapsed()
			return
		}
	}
}

// service is SignalService over a Network.
type service struct {
	network Network
	beats   chan<- struct{}
	log     *slog.Logger
	out     *outbox

	mu      sync.Mutex
	started bool
}

var errNotStarted = errors.New("signal helper: not started (Start opens the key store first)")

func (s *service) Heartbeat(context.Context, *connect.Request[signalv1.HeartbeatRequest]) (*connect.Response[signalv1.HeartbeatResponse], error) {
	select {
	case s.beats <- struct{}{}:
	default: // a beat is pending already
	}
	return connect.NewResponse(&signalv1.HeartbeatResponse{}), nil
}

func (s *service) Start(ctx context.Context, req *connect.Request[signalv1.StartRequest]) (*connect.Response[signalv1.StartResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("signal helper: started already"))
	}
	devices, err := s.network.Open(ctx, req.Msg.GetKey())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	s.started = true
	out := make([]*signalv1.Device, 0, len(devices))
	for _, d := range devices {
		out = append(out, wireDevice(d))
	}
	return connect.NewResponse(&signalv1.StartResponse{Devices: out}), nil
}

func (s *service) Link(ctx context.Context, req *connect.Request[signalv1.LinkRequest], stream *connect.ServerStream[signalv1.LinkResponse]) error {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return connect.NewError(connect.CodeFailedPrecondition, errNotStarted)
	}
	device, err := s.network.Link(ctx, req.Msg.GetDeviceName(), func(url string) error {
		return stream.Send(&signalv1.LinkResponse{Step: &signalv1.LinkResponse_Url{Url: url}})
	})
	if err != nil {
		s.log.Warn("linking failed", "err", err)
		return connect.NewError(connect.CodeUnavailable, err)
	}
	s.log.Info("linked", "aci", device.ACI)
	return stream.Send(&signalv1.LinkResponse{Step: &signalv1.LinkResponse_Linked{Linked: wireDevice(device)}}) //nolint:wrapcheck // the stream's own error
}

func (s *service) Connect(ctx context.Context, req *connect.Request[signalv1.ConnectRequest]) (*connect.Response[signalv1.ConnectResponse], error) {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errNotStarted)
	}
	// Connected for as long as the helper runs, not the call.
	if err := s.network.Connect(context.WithoutCancel(ctx), req.Msg.GetAci(), s.out); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&signalv1.ConnectResponse{}), nil
}

func (s *service) Events(ctx context.Context, _ *connect.Request[signalv1.EventsRequest], stream *connect.ServerStream[signalv1.EventsResponse]) error {
	// Headers now, as kithd's own streams do: the stream can be silent for hours.
	if err := stream.Send(nil); err != nil {
		return fmt.Errorf("signal helper: open the events: %w", err)
	}
	return s.out.listen(ctx, stream.Send)
}

func (s *service) Ack(_ context.Context, req *connect.Request[signalv1.AckRequest]) (*connect.Response[signalv1.AckResponse], error) {
	s.out.ack(req.Msg.GetToken())
	return connect.NewResponse(&signalv1.AckResponse{}), nil
}

func wireDevice(d Device) *signalv1.Device {
	return &signalv1.Device{Aci: d.ACI, Number: d.Number}
}
