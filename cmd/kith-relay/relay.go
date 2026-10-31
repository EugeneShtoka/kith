package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
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
	// id is the ID of the key a bridge is dialed with, as it pins this device; "" for
	// the local kithd.
	id string
	// keyring is this machine's keyring service (domain.Storage), where user commands'
	// secrets are kept.
	keyring string
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
	id, err := certID(cert)
	if err != nil {
		return kithdAt{}, fmt.Errorf("dialing bridge %s: %w", b.Name, err)
	}
	refused := new(atomic.Bool)
	client.Transport = heard{RoundTripper: client.Transport, refused: refused}
	return kithdAt{client: client, scheme: "https", bridge: b.Name, refused: refused, id: id.String()}, nil
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
func newHandler(at kithdAt, configPath string, runs *commandRuns, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	proxy := forward(at, log)
	methods := backendv1.File_backend_v1_services_proto.Services().ByName("BackendService").Methods()
	for i := range methods.Len() {
		mux.Handle("POST /"+backendv1connect.BackendServiceName+"/"+string(methods.Get(i).Name()), proxy)
	}
	mux.Handle(devicev1connect.NewDeviceServiceHandler(device{at: at, configPath: configPath, runs: runs}))
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
	// runs is the user command running, if one is (commands.go).
	runs *commandRuns
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
// resolves it (an unset key is its default), and the revision of the bytes read.
func (d device) DeviceSettings(context.Context, *connect.Request[devicev1.DeviceSettingsRequest]) (*connect.Response[devicev1.DeviceSettingsResponse], error) {
	dev, revision, err := readDevice(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&devicev1.DeviceSettingsResponse{Settings: resolved(dev), Revision: revision}), nil
}

// UpdateDeviceSettings writes the window's settings to the device file if it is still at
// the revision they were read at (config.UpdateDeviceAt, under the lock every writer of
// it takes). The window's change is measured from the file it read, so one made since
// (by hand, by the TUI's editor) would be undone by it: the window is told, and reads
// the file again. Each key the window holds otherwise than the file resolves it is
// written, checked as the TUI checks the file; every other is left as the file has it.
func (d device) UpdateDeviceSettings(_ context.Context, req *connect.Request[devicev1.UpdateDeviceSettingsRequest]) (*connect.Response[devicev1.UpdateDeviceSettingsResponse], error) {
	want := req.Msg.GetSettings()
	if want == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no settings to write"))
	}
	path := config.DevicePath(d.configPath)
	dev, revision, err := config.UpdateDeviceAt(path, req.Msg.GetBaseRevision(), func(file config.Device) (config.Device, error) {
		if verr := setup.ValidateDevice(file); verr != nil {
			return config.Device{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("reading %s: %w", path, verr))
		}
		next, aerr := applySettings(file, want)
		if aerr == nil {
			aerr = setup.ValidateDevice(next)
		}
		if aerr != nil {
			return config.Device{}, connect.NewError(connect.CodeInvalidArgument, aerr)
		}
		return next, nil
	})
	if ce := new(connect.Error); errors.As(err, &ce) {
		return nil, ce
	}
	switch {
	case errors.Is(err, config.ErrDeviceChanged):
		return nil, connect.NewError(connect.CodeAborted, err)
	case err != nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("reading %s: %w", path, err))
	}
	return connect.NewResponse(&devicev1.UpdateDeviceSettingsResponse{Settings: resolved(dev), Revision: revision}), nil
}

// reactionScopes are the rankings [display.reactions] scope takes, as the TUI offers them.
var reactionScopes = []string{"room", "space", "global", "static"}

// applySettings is file with each of want's keys that file resolves otherwise set, as the
// file spells it; every other key is file's own, so an unset one stays unset. A skin
// tone or a ranking the TUI does not know is refused (an emoji set, by ValidateDevice).
func applySettings(file config.Device, want *devicev1.DeviceSettings) (config.Device, error) {
	has := resolved(file)
	flag := func(dst **bool, got, had bool) {
		if got != had {
			*dst = &got
		}
	}
	flag(&file.Display.SendReceipts, want.GetSendReceipts(), has.GetSendReceipts())
	flag(&file.Display.UnreadLine, want.GetUnreadLine(), has.GetUnreadLine())
	flag(&file.Composer.Markdown, want.GetMarkdown(), has.GetMarkdown())
	flag(&file.Composer.ConfirmSMS, want.GetConfirmSms(), has.GetConfirmSms())
	flag(&file.Display.SendTyping, want.GetSendTyping(), has.GetSendTyping())
	if want.GetSkinTone() != has.GetSkinTone() {
		names := config.SkinToneNames()
		i := slices.IndexFunc(names, func(name string) bool {
			modifier, _ := config.SkinToneModifier(name)
			return modifier == want.GetSkinTone()
		})
		if i < 0 {
			return config.Device{}, fmt.Errorf("display.skin_tone: %q is no skin tone's modifier", want.GetSkinTone())
		}
		file.Display.SkinTone = names[i]
	}
	if want.GetEmojiSet() != has.GetEmojiSet() {
		file.Display.Emoji.Set = want.GetEmojiSet()
	}
	if !maps.Equal(want.GetEmojiExtra(), has.GetEmojiExtra()) {
		file.Display.Emoji.Extra = want.GetEmojiExtra()
	}
	if want.GetReactionsScope() != has.GetReactionsScope() {
		if !slices.Contains(reactionScopes, want.GetReactionsScope()) {
			return config.Device{}, fmt.Errorf("display.reactions.scope: unknown ranking %q (want %s)",
				want.GetReactionsScope(), strings.Join(reactionScopes, ", "))
		}
		file.Display.Reactions.Scope = want.GetReactionsScope()
	}
	if !slices.Equal(want.GetReactionsStatic(), has.GetReactionsStatic()) {
		file.Display.Reactions.Static = want.GetReactionsStatic()
	}
	if !slices.EqualFunc(want.GetReadRules(), has.GetReadRules(), sameRule) {
		file.Display.ReadRules = readRules(want.GetReadRules(), file.Display.ReadRules)
	}
	applySpelling(&file, want, has)
	return file, nil
}

// applySpelling is applySettings for [spell] and [complete]: a word the TUI does not know
// (an underline, a mode, a scope or a source) is refused by ValidateDevice.
func applySpelling(file *config.Device, want, has *devicev1.DeviceSettings) {
	word := func(dst *string, got, had string) {
		if got != had {
			*dst = got
		}
	}
	list := func(dst *[]string, got, had []string) {
		if !slices.Equal(got, had) {
			*dst = got
		}
	}
	word(&file.Spell.Underline, want.GetSpellUnderline(), has.GetSpellUnderline())
	word(&file.Spell.RareUnderline, want.GetRareUnderline(), has.GetRareUnderline())
	if want.GetCheckBeforeSend() != has.GetCheckBeforeSend() {
		file.Spell.CheckBeforeSend = want.GetCheckBeforeSend()
	}
	if want.GetRareBeforeSend() != has.GetRareBeforeSend() {
		file.Spell.RareBeforeSend = want.GetRareBeforeSend()
	}
	if want.GetAutocorrect() != has.GetAutocorrect() {
		file.Spell.Autocorrect = config.Autocorrect(want.GetAutocorrect())
	}
	list(&file.Spell.Declined, want.GetSpellDeclined(), has.GetSpellDeclined())
	list(&file.Spell.FrequenciesDeclined, want.GetFrequenciesDeclined(), has.GetFrequenciesDeclined())
	if got := want.GetComplete(); got != has.GetComplete() {
		file.Complete.Enabled = &got
	}
	if got := want.GetCompleteGhost(); got != has.GetCompleteGhost() {
		file.Complete.Ghost = &got
	}
	if want.GetCompleteRatio() != has.GetCompleteRatio() {
		file.Complete.Ratio = int(want.GetCompleteRatio())
	}
	word(&file.Complete.Scope, want.GetCompleteScope(), has.GetCompleteScope())
	list(&file.Complete.Sources, want.GetCompleteSources(), has.GetCompleteSources())
	if want.GetModelDeclined() != has.GetModelDeclined() {
		file.Complete.Model.Declined = want.GetModelDeclined()
	}
}

// sameRule is whether two rules say the same, as the window reads them.
func sameRule(a, b *devicev1.ReadRule) bool {
	return a.GetMatch() == b.GetMatch() && a.GetSender() == b.GetSender() &&
		(a.Send == nil) == (b.Send == nil) && a.GetSend() == b.GetSend()
}

// readRules is the window's rules as the file spells them. The window is not told a
// rule's delay (the TUI's), so each keeps the delay of the file's rule for the same match
// and sender: of several, the n-th the n-th's.
func readRules(want []*devicev1.ReadRule, had []config.ReadRule) []config.ReadRule {
	type place struct{ match, sender string }
	delays := map[place][]*int{}
	for _, r := range had {
		at := place{r.Match, r.Sender}
		delays[at] = append(delays[at], r.Delay)
	}
	out := make([]config.ReadRule, len(want))
	for i, r := range want {
		at := place{r.GetMatch(), r.GetSender()}
		out[i] = config.ReadRule{Match: at.match, Sender: at.sender, Send: r.Send}
		if left := delays[at]; len(left) > 0 {
			out[i].Delay, delays[at] = left[0], left[1:]
		}
	}
	return out
}

// resolved is what the window reads of dev: each key as the TUI resolves it.
func resolved(dev config.Device) *devicev1.DeviceSettings {
	rules := make([]*devicev1.ReadRule, len(dev.Display.ReadRules))
	for i, r := range dev.Display.ReadRules {
		rules[i] = &devicev1.ReadRule{Match: r.Match, Sender: r.Sender, Send: r.Send}
	}
	// Checked as the device file loaded (setup.ValidateDevice): a name here is one of them.
	tone, _ := setup.SkinTone(dev.Display.SkinTone)
	set, _ := setup.EmojiTier(dev.Display.Emoji.Set)
	underline, _ := setup.SpellUnderline(dev.Spell.Underline)
	rare, _ := setup.RareUnderline(dev.Spell.RareUnderline)
	autocorrect, _ := setup.AutocorrectMode(dev.Spell.Autocorrect)
	return &devicev1.DeviceSettings{
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

		SpellUnderline:      underline,
		RareUnderline:       rare,
		CheckBeforeSend:     dev.Spell.CheckBeforeSend,
		RareBeforeSend:      dev.Spell.RareBeforeSend,
		Autocorrect:         autocorrect,
		SpellDeclined:       dev.Spell.Declined,
		FrequenciesDeclined: dev.Spell.FrequenciesDeclined,
		Complete:            dev.Complete.CompleteEnabled(),
		CompleteGhost:       dev.Complete.GhostEnabled(),
		CompleteRatio:       int64(dev.Complete.RatioOrDefault()),
		CompleteScope:       dev.Complete.ScopeOrDefault(),
		CompleteSources:     dev.Complete.Sources,
		ModelDeclined:       dev.Complete.Model.Declined,
		FilingSpaces:        dev.Display.FilingSpaces,
	}
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
	dev, _, err := readDevice(configPath)
	return dev, err
}

// readDevice is loadDevice, and the revision of the bytes it read.
func readDevice(configPath string) (config.Device, string, error) {
	path := config.DevicePath(configPath)
	dev, revision, err := config.ReadDevice(path)
	if err == nil {
		err = setup.ValidateDevice(dev)
	}
	if err != nil {
		return config.Device{}, "", fmt.Errorf("reading %s: %w", path, err)
	}
	return dev, revision, nil
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
