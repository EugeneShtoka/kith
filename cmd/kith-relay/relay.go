package main

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"slices"
	"strings"

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

// newHandler serves what the window may reach: BackendService, forwarded to kithd's
// socket, and DeviceService, answered here. Each of BackendService's methods is a path
// of its own, so anything else (another service, a path that cleans to one) is refused
// and the relay lends its socket's reach to nothing else of kithd's.
func newHandler(socket, configPath string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	proxy := forward(socket, log)
	methods := backendv1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods()
	for i := range methods.Len() {
		mux.Handle("POST /"+backendv1connect.BackendServiceName+"/"+string(methods.Get(i).Name()), proxy)
	}
	mux.Handle(devicev1connect.NewDeviceServiceHandler(device{socket: socket, configPath: configPath}))
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

// device answers DeviceService: the socket it was started with, and the profiles as the
// config and the device file say now, read again on each call.
type device struct {
	socket string
	// configPath is the bridge's config; the device file is beside it.
	configPath string
}

func (d device) Device(context.Context, *connect.Request[devicev1.DeviceRequest]) (*connect.Response[devicev1.DeviceResponse], error) {
	profiles, err := profileNames(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	dev, err := loadDevice(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&devicev1.DeviceResponse{
		Socket: d.socket, Profile: spelled(profiles, dev.ProfileInUse), Profiles: profiles,
	}), nil
}

// ChooseProfile writes the profile named into the device file through the writer every
// window shares (config.UpdateDevice, under the file's lock), so a change made to the
// file meanwhile, by hand or by another window, is kept.
func (d device) ChooseProfile(_ context.Context, req *connect.Request[devicev1.ChooseProfileRequest]) (*connect.Response[devicev1.ChooseProfileResponse], error) {
	profiles, err := profileNames(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
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

// profileNames are the names of the config's profiles, in its order.
func profileNames(configPath string) ([]string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", configPath, err)
	}
	profiles, err := setup.Profiles(cfg)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", configPath, err)
	}
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names, nil
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
