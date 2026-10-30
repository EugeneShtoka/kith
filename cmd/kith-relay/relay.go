package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"

	backendv1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/api/device/v1/devicev1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/media"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// kithdAt is where the relay reaches kithd: the local one's socket, or the bridge this
// device joined, over pinned mutual TLS with this device's key (daemon.DialBridge).
type kithdAt struct {
	client *http.Client
	// scheme is what client is asked for: TLS to a bridge, plain over the socket.
	scheme string
	// socket is the local kithd's socket, bridge the joined bridge's name; one is "".
	socket string
	bridge string
	// refused is the bridge having refused this device's key, until it answers again. In
	// TLS 1.3 a server judges the client's key once the client's handshake is done, so
	// its refusal is an alert where the first answer would come; a request can meet the
	// connection reset first instead, and a bridge bounds the handshakes each address may
	// start, dropping the rest before it reads the key. Neither says anything of the key,
	// so the window is told the last verdict. Nil for the local kithd.
	refused *atomic.Bool
}

// local is the kithd whose socket is socket.
func local(socket string) kithdAt {
	return kithdAt{client: daemon.Dial(socket), scheme: "http", socket: socket}
}

// bridged is the bridge b, reached as the device whose certificate is cert.
func bridged(cert tls.Certificate, b config.Bridge) (kithdAt, error) {
	client, err := daemon.DialBridge(cert, b)
	if err != nil {
		return kithdAt{}, fmt.Errorf("dialing bridge %s: %w", b.Name, err)
	}
	refused := new(atomic.Bool)
	client.Transport = heard{RoundTripper: client.Transport, refused: refused}
	return kithdAt{client: client, scheme: "https", bridge: b.Name, refused: refused}, nil
}

// heard forgets a bridge's refusal once it answers: an answer comes only through a
// handshake that took this device's key.
type heard struct {
	http.RoundTripper
	refused *atomic.Bool
}

func (h heard) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := h.RoundTripper.RoundTrip(r)
	if err != nil {
		return nil, fmt.Errorf("reaching the bridge: %w", err)
	}
	h.refused.Store(false)
	return res, nil
}

// failure is err, met reaching kithd, as the window reads it: kithd's own answer keeps
// its code; a bridge refusing this device's key (a TLS alert from it), now or last time
// and not answering since, is permission denied, saying so; anything else is kithd not
// reachable, unavailable, which the window tries again.
func (at kithdAt) failure(err error) *connect.Error {
	var alert *net.OpError
	if at.bridge != "" && (at.refused.Load() || errors.As(err, &alert) && alert.Op == "remote error") {
		at.refused.Store(true)
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"bridge %s refused this device's key; it is not paired there (kith pair there, kith join here): %w", at.bridge, err))
	}
	if ce := new(connect.Error); errors.As(err, &ce) {
		return ce
	}
	return connect.NewError(connect.CodeUnavailable, err)
}

// newHandler serves what the window may reach: BackendService, forwarded to kithd at,
// and DeviceService, answered here. Each of BackendService's methods is a path of its
// own, so anything else (another service, a path that cleans to one) is refused and the
// relay lends its reach to nothing else of kithd's.
func newHandler(at kithdAt, configPath string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	proxy := forward(at, log)
	methods := backendv1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods()
	for i := range methods.Len() {
		mux.Handle("POST /"+backendv1connect.BackendServiceName+"/"+string(methods.Get(i).Name()), proxy)
	}
	mux.Handle(devicev1connect.NewDeviceServiceHandler(device{at: at, configPath: configPath}))
	return mux
}

// forward proxies to kithd at, flushing each stream frame as it comes: the window's
// streams are live. kithd unreachable is answered as failure reads it, in the protocol
// the window asked in; kithd gone mid-stream, or a bridge's connection found dead by its
// PING, cuts the window's stream, which it reopens.
func forward(at kithdAt, log *slog.Logger) http.Handler {
	errs := connect.NewErrorWriter()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = at.scheme
			r.Out.URL.Host = "kithd"
		},
		Transport:     at.client.Transport,
		FlushInterval: -1,
		ErrorLog:      slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("kithd did not answer", "method", r.URL.Path, "err", err)
			if werr := errs.Write(w, r, at.failure(err)); werr != nil {
				w.WriteHeader(http.StatusBadGateway)
			}
		},
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
		body := &heldBody{body: r.Body}
		r.Body = body
		defer body.end()
		proxy.ServeHTTP(w, r)
	})
}

// errHandled is a request's body read after its handler returned.
var errHandled = errors.New("kith-relay: the request was already answered")

// heldBody is a request's body read only while its handler runs, and to its end. A
// full-duplex server whose handler left the body unread reads it to its end itself, as
// it closes it once the handler returned, and reaching the end starts its own read of
// the connection while it already reads the next request, which it does not survive. A
// round trip that failed before sending the body (a bridge refusing the connection)
// leaves it unread, and a transport may go on reading it after its round trip ended
// (net/http allows it).
type heldBody struct {
	mu    sync.Mutex
	body  io.ReadCloser
	ended bool
}

func (b *heldBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ended {
		return 0, errHandled
	}
	n, err := b.body.Read(p)
	switch {
	case err == nil:
		return n, nil
	case errors.Is(err, io.EOF):
		return n, io.EOF
	default:
		return n, fmt.Errorf("reading the request: %w", err)
	}
}

func (b *heldBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ended {
		return nil // the server closes the body itself
	}
	if err := b.body.Close(); err != nil {
		return fmt.Errorf("closing the request: %w", err)
	}
	return nil
}

// unreadBound is how much of a request end reads that the transport left unread, as the
// server itself would after its handler: a window's requests are one message each.
const unreadBound = 256 << 10

// end waits out a read in progress and reads what is left of the request; no read
// after it reaches the request.
func (b *heldBody) end() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ended = true
	_, _ = io.CopyN(io.Discard, b.body, unreadBound) // its error is the request's end, or a cut the server sees itself
}

// device answers DeviceService: where kithd is, and the profiles as the config and the
// device file say now, read again on each call.
type device struct {
	at kithdAt
	// configPath is kithd's config, unless at is a bridge; the device file is beside it.
	configPath string
}

func (d device) Device(ctx context.Context, _ *connect.Request[devicev1.DeviceRequest]) (*connect.Response[devicev1.DeviceResponse], error) {
	profiles, err := d.profileNames(ctx)
	if err != nil {
		return nil, err
	}
	dev, err := loadDevice(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&devicev1.DeviceResponse{
		Socket: d.at.socket, Bridge: d.at.bridge, Profile: spelled(profiles, dev.ProfileInUse), Profiles: profiles,
	}), nil
}

// ChooseProfile writes the profile named into the device file through the writer every
// window shares (config.UpdateDevice, under the file's lock), so a change made to the
// file meanwhile, by hand or by another window, is kept.
func (d device) ChooseProfile(ctx context.Context, req *connect.Request[devicev1.ChooseProfileRequest]) (*connect.Response[devicev1.ChooseProfileResponse], error) {
	profiles, err := d.profileNames(ctx)
	if err != nil {
		return nil, err
	}
	name := spelled(profiles, req.Msg.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("the config has no profile %q (it has %s)", req.Msg.GetName(), strings.Join(profiles, ", ")))
	}
	was, err := loadDevice(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	now := was.Clone()
	now.ProfileInUse = name
	if _, err := config.UpdateDevice(config.DevicePath(d.configPath), was, now); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&devicev1.ChooseProfileResponse{Profile: name}), nil
}

// DeviceSettings answers what the window reads of the device file, resolved as the TUI
// resolves it: an unset key is its default.
func (d device) DeviceSettings(context.Context, *connect.Request[devicev1.DeviceSettingsRequest]) (*connect.Response[devicev1.DeviceSettingsResponse], error) {
	dev, err := loadDevice(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	rules := make([]*devicev1.ReadRule, len(dev.Display.ReadRules))
	for i, r := range dev.Display.ReadRules {
		rules[i] = &devicev1.ReadRule{Match: r.Match, Sender: r.Sender, Send: r.Send}
	}
	// Checked as the device file loaded (setup.ValidateDevice): a name here is one of them.
	tone, _ := setup.SkinTone(dev.Display.SkinTone)
	set, _ := setup.EmojiTier(dev.Display.Emoji.Set)
	return connect.NewResponse(&devicev1.DeviceSettingsResponse{
		SendReceipts:    dev.Display.SendsReceipts(),
		ReadRules:       rules,
		UnreadLine:      dev.Display.ShowUnreadLine(),
		Markdown:        dev.Composer.MarkdownEnabled(),
		ConfirmSms:      dev.Composer.ConfirmsSMS(),
		SendTyping:      dev.Display.SendsTyping(),
		SkinTone:        tone,
		EmojiSet:        set,
		EmojiExtra:      dev.Display.Emoji.Extra,
		ReactionsScope:  cmp.Or(dev.Display.Reactions.Scope, "room"), // as the TUI reads it unset
		ReactionsStatic: dev.Display.Reactions.Static,
	}), nil
}

// Outgoing readies a file for sending as the TUI's sendFileCmd does, where [display.media]
// anonymize holds: a picture copied without its metadata, the file under a neutral name.
// A rule is a place's, which the relay cannot place a room in yet, so one that
// anonymizes anywhere counts everywhere: a file goes anonymized wherever it may have to.
func (d device) Outgoing(_ context.Context, req *connect.Request[devicev1.OutgoingRequest]) (*connect.Response[devicev1.OutgoingResponse], error) {
	dev, err := loadDevice(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	path := req.Msg.GetPath()
	anonymize := dev.Display.Media.Anonymizes() || slices.ContainsFunc(dev.Display.Media.Rules,
		func(r config.MediaRule) bool { return r.Anonymize != nil && *r.Anonymize })
	if !anonymize {
		return connect.NewResponse(&devicev1.OutgoingResponse{Path: path}), nil
	}
	// The clean copy goes where the TUI's does: the media cache's outgoing folder.
	outgoing := dev.Display.Media.CacheDir
	if rest, ok := strings.CutPrefix(outgoing, "~/"); ok {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, herr)
		}
		outgoing = filepath.Join(home, rest)
	}
	if outgoing != "" {
		outgoing = filepath.Join(outgoing, "outgoing")
	}
	clean, what, err := media.Strip(path, outgoing)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&devicev1.OutgoingResponse{
		Path: clean, Name: domain.SentName(filepath.Base(path)), Copied: clean != path, Kept: what == media.Kept,
	}), nil
}

// profileNames are the names of kithd's config's profiles, in its order, as the window
// reads a failure: a config here that does not read is a failed precondition, a bridge's
// as failure says.
func (d device) profileNames(ctx context.Context) ([]string, error) {
	cfg, err := d.config(ctx)
	if err != nil {
		return nil, err
	}
	profiles, err := setup.Profiles(cfg)
	if err != nil {
		read := d.configPath
		if d.at.bridge != "" {
			read = "the config of bridge " + d.at.bridge
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("reading %s: %w", read, err))
	}
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names, nil
}

// config is kithd's config: the bridge's, read through it (GetConfig), or the file here.
func (d device) config(ctx context.Context) (config.Config, error) {
	if d.at.bridge == "" {
		cfg, err := config.Load(d.configPath)
		if err != nil {
			return config.Config{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("reading %s: %w", d.configPath, err))
		}
		return cfg, nil
	}
	res, err := backendv1connect.NewBackendServiceClient(d.at.client, d.at.scheme+"://kithd").
		GetConfig(ctx, connect.NewRequest(&backendv1.GetConfigRequest{}))
	if err != nil {
		return config.Config{}, d.at.failure(err)
	}
	cfg, err := config.Decode(res.Msg.GetConfig())
	if err != nil {
		return config.Config{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the config of bridge %s: %w", d.at.bridge, err))
	}
	return cfg, nil
}

// loadDevice is the device file beside the config, checked as every client checks it.
func loadDevice(configPath string) (config.Device, error) {
	path := config.DevicePath(configPath)
	dev, err := config.LoadDevice(path)
	if err == nil {
		err = setup.ValidateDevice(dev)
	}
	if err != nil {
		return config.Device{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return dev, nil
}

// spelled is the profile name names as the config spells it (names are unique in any
// case, and trimmed); "" when the config has none by that name.
func spelled(profiles []string, name string) string {
	for _, p := range profiles {
		if strings.EqualFold(p, strings.TrimSpace(name)) {
			return p
		}
	}
	return ""
}
