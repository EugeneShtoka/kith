package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	backendv1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/themespec"
)

// The graphical clients' theme ([gui.theme]) is the bridge's config, which every device
// shares, under this device's own (device.toml), key by key. The shared one is read and written only
// through kithd (GetConfig, UpdateConfig on the revision the window read), so a device
// that joined a bridge writes the bridge's and never a file here; the device's is
// written here as the settings page's other keys are.

// themePalette is how many of the TUI's sender colors the window is handed: a sender
// whose room slot is past them is drawn in the window's own hashed color.
const themePalette = 128

// kithdBackend is kithd at, as a BackendService client.
func (d device) kithdBackend() backendv1connect.BackendServiceClient {
	return backendv1connect.NewBackendServiceClient(d.at.client, d.at.scheme+"://kithd")
}

// Theme answers the theme as it now resolves, both files read again.
func (d device) Theme(ctx context.Context, _ *connect.Request[devicev1.ThemeRequest]) (*connect.Response[devicev1.ThemeResponse], error) {
	theme, err := d.theme(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(theme), nil
}

// UpdateTheme writes each of the window's theme keys to its scope's file, each file only
// if it is still at the revision the window read it at, then answers the theme again.
func (d device) UpdateTheme(ctx context.Context, req *connect.Request[devicev1.UpdateThemeRequest]) (*connect.Response[devicev1.UpdateThemeResponse], error) {
	var shared, own []*devicev1.ThemeWrite
	for _, w := range req.Msg.GetWrites() {
		var probe config.GUITheme
		if !probe.Set(w.GetKey(), "") {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("gui.theme: no key %q", w.GetKey()))
		}
		if err := setup.GUIThemeValue(w.GetKey(), w.GetValue()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		switch w.GetScope() {
		case devicev1.ThemeScope_THEME_SCOPE_SHARED:
			shared = append(shared, w)
		case devicev1.ThemeScope_THEME_SCOPE_DEVICE:
			own = append(own, w)
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("gui.theme.%s: no scope to write it to", w.GetKey()))
		}
	}
	if len(own) > 0 {
		if err := d.writeOwnTheme(req.Msg.GetDeviceRevision(), own); err != nil {
			return nil, err
		}
	}
	if len(shared) > 0 {
		if err := d.writeSharedTheme(ctx, req.Msg.GetConfigRevision(), shared); err != nil {
			return nil, err
		}
	}
	theme, err := d.theme(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&devicev1.UpdateThemeResponse{Theme: theme}), nil
}

// writeOwnTheme writes writes into device.toml's [gui.theme] if the file is at revision
// (config.UpdateDeviceAt, under the lock every writer of it takes).
func (d device) writeOwnTheme(revision string, writes []*devicev1.ThemeWrite) error {
	path := config.DevicePath(d.configPath)
	_, _, err := config.UpdateDeviceAt(path, revision, func(file config.Device) (config.Device, error) {
		if verr := setup.ValidateDevice(file); verr != nil {
			return config.Device{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("reading %s: %w", path, verr))
		}
		for _, w := range writes {
			file.GUI.Theme.Set(w.GetKey(), w.GetValue())
		}
		return file, nil
	})
	if ce := new(connect.Error); errors.As(err, &ce) {
		return ce
	}
	switch {
	case errors.Is(err, config.ErrDeviceChanged):
		return connect.NewError(connect.CodeAborted, err)
	case err != nil:
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("reading %s: %w", path, err))
	}
	return nil
}

// writeSharedTheme writes writes into the bridge's config through kithd: the config as
// kithd serves it (the file's own keys, no profile applied), its [gui.theme] changed, written
// whole on revision, which kithd refuses (ABORTED) for a file changed since.
func (d device) writeSharedTheme(ctx context.Context, revision string, writes []*devicev1.ThemeWrite) error {
	backend := d.kithdBackend()
	got, err := backend.GetConfig(ctx, connect.NewRequest(&backendv1.GetConfigRequest{}))
	if err != nil {
		return d.at.failure(err)
	}
	cfg, err := config.Decode(got.Msg.GetConfig())
	if err == nil {
		for _, w := range writes {
			cfg.GUI.Theme.Set(w.GetKey(), w.GetValue())
		}
		err = setup.GUITheme(cfg.GUI.Theme)
	}
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s: %w", d.configName(), err))
	}
	_, err = backend.UpdateConfig(ctx, connect.NewRequest(&backendv1.UpdateConfigRequest{BaseRevision: revision, Config: config.Encode(cfg)}))
	if err != nil {
		return d.at.failure(err)
	}
	return nil
}

// configName is how a failure names kithd's config.
func (d device) configName() string {
	if d.at.bridge != "" {
		return "the config of bridge " + d.at.bridge
	}
	return d.configPath
}

// theme reads the shared theme through kithd, this device's from its file, and the
// accounts kithd serves, and resolves them.
func (d device) theme(ctx context.Context) (*devicev1.ThemeResponse, error) {
	backend := d.kithdBackend()
	got, err := backend.GetConfig(ctx, connect.NewRequest(&backendv1.GetConfigRequest{}))
	if err != nil {
		return nil, d.at.failure(err)
	}
	cfg, err := config.Decode(got.Msg.GetConfig())
	if err == nil {
		err = setup.GUITheme(cfg.GUI.Theme)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s: %w", d.configName(), err))
	}
	dev, revision, err := readDevice(d.configPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	accounts, err := backend.Accounts(ctx, connect.NewRequest(&backendv1.AccountsRequest{}))
	if err != nil {
		return nil, d.at.failure(err)
	}
	theme := resolveTheme(cfg, dev, accounts.Msg.GetAccounts())
	theme.ConfigRevision, theme.DeviceRevision = got.Msg.GetRevision(), revision
	return theme, nil
}

// resolveTheme is each key of the theme as the window draws it: this device's value, else
// the shared one, else its default; and what the defaults come from, as the TUI has
// them.
func resolveTheme(cfg config.Config, dev config.Device, accounts []*backendv1.Account) *devicev1.ThemeResponse {
	out := &devicev1.ThemeResponse{
		Values:        map[string]*devicev1.ThemeValue{},
		NetworkColors: networkColors(dev.Display.Theme.Networks),
		Identities:    map[string]*devicev1.ThemeIdentity{},
	}
	defaults := map[string]string{}
	paths := []string{"mode"}
	for _, mode := range []string{"light", "dark"} {
		for _, role := range config.GUIThemeRoles() {
			paths = append(paths, mode+"."+role)
		}
	}
	for _, a := range accounts {
		out.Accounts = append(out.Accounts, &devicev1.ThemeAccount{On: a.GetOn(), Network: a.GetNetwork(), Name: a.GetName()})
		path := config.GUIThemeAccounts + "." + a.GetOn()
		paths = append(paths, path)
		defaults[path] = out.GetNetworkColors()[a.GetNetwork()]
	}
	paths = append(paths, cfg.GUI.Theme.Keys()...)
	paths = append(paths, dev.GUI.Theme.Keys()...)
	for _, path := range paths {
		shared, _ := cfg.GUI.Theme.Get(path)
		own, _ := dev.GUI.Theme.Get(path)
		v := &devicev1.ThemeValue{Value: defaults[path], Shared: shared, Device: own}
		switch {
		case own != "":
			v.Value, v.Source = own, devicev1.ThemeSource_THEME_SOURCE_DEVICE
		case shared != "":
			v.Value, v.Source = shared, devicev1.ThemeSource_THEME_SOURCE_SHARED
		}
		out.Values[path] = v
	}
	// A room's slots key a [[display.identity]]'s people by one key, and the TUI spreads
	// its sender colors clear of the identities' pinned colors (setup.SenderKey, tui's
	// hues): here, of every pinned color, as no room is known.
	var pinned []string
	for i, id := range cfg.Display.Identities {
		color, _ := themespec.Color(id.Color)
		if color != "" {
			pinned = append(pinned, color)
		}
		for _, user := range id.IDs {
			out.Identities[user] = &devicev1.ThemeIdentity{Key: setup.SenderKey(i, id), Color: color}
		}
	}
	out.SenderPalette = themespec.SenderColors(themePalette, pinned)
	return out
}

// networkColors is each network's color as the TUI names its rooms ("#rrggbb"), by the
// network as a Room names it; one with none is left out.
func networkColors(n config.NetworkColors) map[string]string {
	out := map[string]string{}
	for key := range n.Set() {
		network, ok := domain.ProtocolNamed(strings.ReplaceAll(key, "_", " "))
		if !ok {
			continue
		}
		if hex, ok := themespec.Color(n.For(network.String())); ok {
			out[network.String()] = hex
		}
	}
	return out
}
