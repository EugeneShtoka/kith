package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

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

// kithdAt is where the relay reaches kithd (daemon.Upstream), and what the window is
// told of it.
type kithdAt struct {
	daemon.Upstream
	// socket is the local kithd's socket; "" for a bridge.
	socket string
	// id is the ID of the key a bridge is dialed with, as it pins this device; "" for
	// the local kithd.
	id string
	// keyring is this machine's keyring service (domain.Storage), where user commands'
	// secrets are kept.
	keyring string
}

// local is the kithd whose socket is socket.
func local(socket string) kithdAt {
	return kithdAt{Upstream: daemon.LocalUpstream(socket), socket: socket}
}

// bridged is the bridge b, reached as the device whose certificate is cert.
func bridged(cert tls.Certificate, b config.Bridge) (kithdAt, error) {
	up, err := daemon.BridgeUpstream(cert, b)
	if err != nil {
		return kithdAt{}, fmt.Errorf("dialing bridge %s: %w", b.Name, err)
	}
	id, err := certID(cert)
	if err != nil {
		return kithdAt{}, fmt.Errorf("dialing bridge %s: %w", b.Name, err)
	}
	return kithdAt{Upstream: up, id: id.String()}, nil
}

// newHandler serves what the window may reach: BackendService, forwarded to kithd at
// (daemon.HandleBackend), and DeviceService, answered here.
func newHandler(at kithdAt, configPath string, runs *commandRuns, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	daemon.HandleBackend(mux, at.Forward(log))
	mux.Handle(devicev1connect.NewDeviceServiceHandler(device{at: at, configPath: configPath, runs: runs}))
	return mux
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
		Socket: d.at.socket, Bridge: d.at.Bridge, Profile: spelled(profiles, dev.ProfileInUse), Profiles: profiles,
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
	if !slices.EqualFunc(want.GetJumps(), has.GetJumps(), sameJump) {
		js, err := jumpEntries(want.GetJumps(), file.Keys.Jump)
		if err != nil {
			return config.Device{}, err
		}
		file.Keys.Jump = js
	}
	if !slices.Equal(want.GetThreadsFolded(), has.GetThreadsFolded()) {
		file.Display.Threads.Folded = foldTable(want.GetThreadsFolded())
	}
	applySpelling(&file, want, has)
	return file, nil
}

// sameJump is whether two jumps say the same.
func sameJump(a, b *devicev1.Jump) bool {
	return a.GetSequence() == b.GetSequence() && a.GetRoom() == b.GetRoom() && a.GetPlace() == b.GetPlace()
}

// jumpEntries is the window's [[keys.jump]] as the file spells them, after the file's
// entries the window is not shown (jumps leaves them out), which it cannot have meant to
// drop. A sequence of a key kith does not name, or a jump to no place, two, or one the
// window could not read back, is refused.
func jumpEntries(want []*devicev1.Jump, had config.Jumps) (config.Jumps, error) {
	var out config.Jumps
	for _, j := range had {
		if len(jumps(config.Jumps{j})) == 0 {
			out = append(out, j)
		}
	}
	for _, j := range want {
		seq := config.NormalizeSequence(j.GetSequence())
		if seq == "" || !config.SequenceValid(seq) {
			return nil, fmt.Errorf("keys.jump: %q is not a key sequence kith names", j.GetSequence())
		}
		target := domain.JumpTarget{Kind: domain.JumpRoom, Name: j.GetRoom()}
		if j.GetPlace() != "" {
			target = domain.JumpTarget{Kind: domain.JumpHome, Name: j.GetPlace()}
		}
		entry := config.Jump{Chord: seq, Target: target.String()}
		if (j.GetRoom() == "") == (j.GetPlace() == "") || len(jumps(config.Jumps{entry})) == 0 {
			return nil, fmt.Errorf("keys.jump: %s goes to one room or one place kith reads", seq)
		}
		out = append(out, entry)
	}
	return out, nil
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

// readRules is the window's rules as the file spells them. The window does not write a
// rule's delay, so each keeps the delay of the file's rule for the same match and sender:
// of several, the n-th the n-th's.
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
		if r.Delay != nil {
			rules[i].Delay = new(int64(*r.Delay))
		}
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
		Keys:                keySequences(dev.Keys),
		Jumps:               jumps(dev.Keys.Jump),
		TagKeys:             tagKeys(dev.Keys.Tag),
		ReadDelay:           int64(dev.Display.ReadAfter() / time.Second),
		AudioSpeed:          dev.Display.Media.Audio.PlaySpeed(),
		AudioSkip:           int64(dev.Display.Media.Audio.SkipStep() / time.Second),
		ThreadsInRoomList:   dev.Display.Threads.Mode(),
		ThreadsMax:          int64(dev.Display.Threads.MaxInRoomList),
		ThreadsDefaultMax:   config.DefaultThreadsInRoomList,
		ThreadsMarker:       dev.Display.Threads.RowMark(),
		ThreadRules:         threadRules(dev.Display.Threads.Rules),
		ThreadsFolded:       folded(dev.Display.Threads.Folded),
	}
}

// folded are the rooms a fold table holds folded, sorted.
func folded(table map[string]bool) []string {
	var out []string
	for room, fold := range table {
		if fold {
			out = append(out, room)
		}
	}
	slices.Sort(out)
	return out
}

// foldTable is the rooms folded as the file spells them; none is no table.
func foldTable(rooms []string) map[string]bool {
	if len(rooms) == 0 {
		return nil
	}
	out := make(map[string]bool, len(rooms))
	for _, room := range rooms {
		out[room] = true
	}
	return out
}

// threadRules are [[display.threads.rule]] as the window reads them, in the file's order.
func threadRules(rules []config.ThreadRule) []*devicev1.ThreadRule {
	out := make([]*devicev1.ThreadRule, len(rules))
	for i, r := range rules {
		out[i] = &devicev1.ThreadRule{Match: r.Match, Max: int64(r.MaxInRoomList)}
	}
	return out
}

// tagKeys are ts as the window reads them: each chord one sequence of keys kith names,
// to a tag named; the TUI reports the others.
func tagKeys(ts config.TagKeys) []*devicev1.TagKey {
	var out []*devicev1.TagKey
	for _, t := range ts {
		seqs := config.SplitKeys(t.Chord)
		if len(seqs) != 1 || !config.SequenceValid(seqs[0]) || strings.TrimSpace(t.Tag) == "" {
			continue
		}
		out = append(out, &devicev1.TagKey{Sequence: seqs[0], Tag: strings.TrimSpace(t.Tag)})
	}
	return out
}

// jumps are js as the window reads them: each chord one sequence of keys kith names, to
// a target domain.ParseJump reads; the TUI reports the others.
func jumps(js config.Jumps) []*devicev1.Jump {
	var out []*devicev1.Jump
	for _, j := range js {
		seqs := config.SplitKeys(j.Chord)
		target, ok := domain.ParseJump(j.Target)
		if len(seqs) != 1 || !config.SequenceValid(seqs[0]) || !ok {
			continue
		}
		jump := &devicev1.Jump{Sequence: seqs[0]}
		if target.Kind == domain.JumpRoom {
			jump.Room = target.Name
		} else {
			jump.Place = target.Name
		}
		out = append(out, jump)
	}
	return out
}

// keySequences are k's bindings as the window reads them: each split, and only the
// sequences whose every key is one kith names (the TUI reports the others).
func keySequences(k config.Keys) map[string]*devicev1.KeySequences {
	out := make(map[string]*devicev1.KeySequences, len(k.Bindings))
	for path, list := range k.Bindings {
		var valid []string
		for _, seq := range config.SplitKeys(list) {
			if config.SequenceValid(seq) {
				valid = append(valid, seq)
			}
		}
		out[path] = &devicev1.KeySequences{Sequences: valid}
	}
	return out
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
		if d.at.Bridge != "" {
			read = "the config of bridge " + d.at.Bridge
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
	if d.at.Bridge == "" {
		cfg, err := config.Load(d.configPath)
		if err != nil {
			return config.Config{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("reading %s: %w", d.configPath, err))
		}
		return cfg, nil
	}
	res, err := backendv1connect.NewBackendServiceClient(d.at.Client, d.at.Scheme+"://kithd").
		GetConfig(ctx, connect.NewRequest(&backendv1.GetConfigRequest{}))
	if err != nil {
		return config.Config{}, d.at.Failure(err)
	}
	cfg, err := config.Decode(res.Msg.GetConfig())
	if err != nil {
		return config.Config{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the config of bridge %s: %w", d.at.Bridge, err))
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
