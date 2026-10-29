package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"

	"connectrpc.com/connect"

	backendv1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/api/device/v1/devicev1connect"
	"github.com/EugeneShtoka/kith/internal/daemon"
)

// newHandler serves what the window may reach: BackendService, forwarded to kithd's
// socket, and DeviceService, answered here. Each of BackendService's methods is a path
// of its own, so anything else (another service, a path that cleans to one) is refused
// and the relay lends its socket's reach to nothing else of kithd's.
func newHandler(socket, profile string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	proxy := forward(socket, log)
	methods := backendv1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods()
	for i := range methods.Len() {
		mux.Handle("POST /"+backendv1connect.BackendServiceName+"/"+string(methods.Get(i).Name()), proxy)
	}
	mux.Handle(devicev1connect.NewDeviceServiceHandler(device{socket: socket, profile: profile}))
	return mux
}

// forward proxies to kithd's socket, flushing each stream frame as it comes: the
// window's streams are live. kithd unreachable is a 502, which Connect reads as
// unavailable; kithd gone mid-stream cuts the window's stream, which it reopens.
func forward(socket string, log *slog.Logger) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = "kithd"
		},
		Transport:     daemon.Dial(socket).Transport,
		FlushInterval: -1,
		ErrorLog:      slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// kithd answers a stream with its headers as it opens, which can be before the
		// window's request is all read. An HTTP/1.1 server otherwise consumes the rest of
		// a request's body before it writes the response: the headers would wait on the
		// request's end, and that read races the copy forwarding the body to kithd, which
		// then cuts the stream.
		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
			log.Warn("cannot read a request while answering it; a stream may be cut", "err", err)
		}
		proxy.ServeHTTP(w, r)
	})
}

// device answers DeviceService from what the relay was started with.
type device struct {
	socket  string
	profile string
}

func (d device) Device(context.Context, *connect.Request[devicev1.DeviceRequest]) (*connect.Response[devicev1.DeviceResponse], error) {
	return connect.NewResponse(&devicev1.DeviceResponse{Socket: d.socket, Profile: d.profile}), nil
}
