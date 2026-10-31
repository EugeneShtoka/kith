package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	devicev1 "github.com/EugeneShtoka/kith/internal/api/device/v1"
	"github.com/EugeneShtoka/kith/internal/api/device/v1/devicev1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/schedule"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/themespec"
)

// accountsWorld is a backend serving accounts, and no rooms.
type accountsWorld struct {
	world
	accounts []domain.Account
}

func (w accountsWorld) Accounts(context.Context) ([]domain.Account, error) { return w.accounts, nil }

// themeAccounts are the accounts the theme's kithd serves: two networks, one bridged
// through Matrix.
var themeAccounts = []domain.Account{
	{Network: domain.ProtocolWhatsApp, On: domain.AccountRooms(domain.ProtocolWhatsApp, "main"), Name: "main"},
	{Network: domain.ProtocolMatrix, On: domain.AccountRooms(domain.ProtocolMatrix, domain.AccountKey("@me:x")), Name: "@me:x"},
}

// themeKithd is a real kithd keeping the config at cfgPath, checking a config as kithd
// does (setup.Validate), on a socket of its own; and, with keys paired, over TLS to
// them at addr as a bridge.
type themeKithd struct {
	socket string
	addr   string
	id     string
}

func serveThemeKithd(t *testing.T, cfgPath string, paired ...*ecdsa.PrivateKey) themeKithd {
	t.Helper()
	dir := t.TempDir()
	var roster strings.Builder
	for i, key := range paired {
		fmt.Fprintf(&roster, "[[device]]\nname = \"device%d\"\nid = %q\n", i, idOf(t, key).String())
	}
	if err := os.WriteFile(filepath.Join(dir, "devices.toml"), []byte(roster.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	backend := accountsWorld{accounts: themeAccounts}
	n, err := daemon.NewNotifications(cfg, backend)
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := daemon.NewUploads(filepath.Join(dir, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	k := themeKithd{socket: filepath.Join(dir, "kithd.sock")}
	ln, err := daemon.Listen(t.Context(), k.socket)
	if err != nil {
		t.Fatal(err)
	}
	key := newKey(t)
	cert := certOf(t, key)
	devices := daemon.NewDevices(filepath.Join(dir, "devices.toml"), meshAt(t, filepath.Join(dir, "mesh.db")),
		func() (tls.Certificate, error) { return cert, nil }, logging.Discard())
	if len(paired) > 0 {
		k.addr, k.id = freeAddr(t), idOf(t, key).String()
		devices.Listen([]string{k.addr})
	}
	log := &messageLog{streams: daemon.NewStreams()}
	d := &daemon.Daemon{
		Backend: backend, Streams: log.streams, MessageLog: log, State: daemon.NewState(), Notifications: n,
		Reload:      func(context.Context) error { return nil },
		CheckConfig: func(_ context.Context, cfg config.Config) error { return setup.Validate(cfg) },
		Config:      daemon.NewConfigFile(cfgPath),
		Scheduler: daemon.NewScheduler(schedule.New(filepath.Join(dir, "scheduled.toml")), backend,
			func() time.Duration { return time.Hour }, nil),
		Logins:  daemon.NewLogins(t.Context(), func() []api.LoginLeader { return nil }),
		Uploads: uploads,
		Log:     logging.Discard(),
		Paired:  devices,
	}
	d.State.Synced(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { defer close(served); _ = daemon.Serve(ctx, ln, d) }()
	t.Cleanup(func() { cancel(); <-served })
	return k
}

// themeConfig writes a config at path holding a profile whose rail replaces the shared
// one, the shared rail, a pinned identity, and theme.
func themeConfig(t *testing.T, path string, theme config.GUITheme) {
	t.Helper()
	cfg := config.Config{
		Display: config.Display{
			Rail: config.Rail{Order: []string{"*", "tag:Family"}},
			Identities: []config.Identity{
				{Alias: "Ada", Color: "green", IDs: []string{"matrix:@ada:x", "whatsapp:15550100@s.whatsapp.net"}},
				{IDs: []string{"matrix:@bob:x"}},
			},
		},
		Profiles: []config.Profile{
			{Name: "Home", Include: []string{"*"}},
			{Name: "Work", Include: []string{"*"}, Rail: config.Rail{Order: []string{"tag:Family", "*"}, Hidden: []string{"tag:Family"}}},
		},
		Tags: []config.Tag{{Name: "Family"}},
		GUI:  config.GUI{Theme: theme},
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
}

func themeOf(t *testing.T, relay devicev1connect.DeviceServiceClient) *devicev1.ThemeResponse {
	t.Helper()
	got, err := relay.Theme(t.Context(), connect.NewRequest(&devicev1.ThemeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return got.Msg
}

// layer is what scope's file holds of key in theme, "" for none.
func layer(theme *devicev1.ThemeResponse, key string, scope devicev1.ThemeScope) string {
	v := theme.GetValues()[key]
	if scope == devicev1.ThemeScope_THEME_SCOPE_DEVICE {
		return v.GetDevice()
	}
	return v.GetShared()
}

// changeTheme is the settings page's change of key at scope to value, from the theme it
// showed, as desktop/src/renderer/theme.ts makes it: told a file changed since, it reads
// the theme again and writes again unless another writer changed that key there, whose
// value it keeps (conceded).
func changeTheme(ctx context.Context, relay devicev1connect.DeviceServiceClient, shown *devicev1.ThemeResponse, key string, scope devicev1.ThemeScope, value string) (*devicev1.ThemeResponse, bool, error) {
	was, base := layer(shown, key, scope), shown
	for range 4 {
		switch layer(base, key, scope) {
		case value:
			return base, false, nil
		case was:
		default:
			return base, true, nil
		}
		got, err := relay.UpdateTheme(ctx, connect.NewRequest(&devicev1.UpdateThemeRequest{
			ConfigRevision: base.GetConfigRevision(), DeviceRevision: base.GetDeviceRevision(),
			Writes: []*devicev1.ThemeWrite{{Key: key, Value: value, Scope: scope}},
		}))
		if err == nil {
			return got.Msg.GetTheme(), false, nil
		}
		if connect.CodeOf(err) != connect.CodeAborted {
			return nil, false, err
		}
		again, err := relay.Theme(ctx, connect.NewRequest(&devicev1.ThemeRequest{}))
		if err != nil {
			return nil, false, err
		}
		base = again.Msg
	}
	return nil, false, errors.New("the files kept changing")
}

// Each key resolves as this device's value, else the shared one, else its default: an
// account's its network's color as the TUI names its rooms; and what the defaults come
// from is the TUI's, its sender colors clear of the pinned ones. Removing this device's
// value shows the shared one again.
func TestTheThemeIsTheDevicesOverTheSharedOverTheDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	var shared config.GUITheme
	shared.Set("mode", "dark")
	shared.Set("light.accent", "#111111")
	shared.Set("dark.text", "#222222")
	shared.Set("people.matrix:@ada:x", "#333333")
	themeConfig(t, cfgPath, shared)
	handWrite(t, cfgPath, "[tui.theme.networks]\nwhatsapp = \"#25d366\"\n\n[gui.theme.accounts]\n\"matrix:"+domain.AccountKey("@me:x")+"/\" = \"#bbbbbb\"\n\n[gui.theme.light]\naccent = \"#aaaaaa\"\n")
	relay := deviceClient(relayFor(t, serveThemeKithd(t, cfgPath).socket, cfgPath))

	theme := themeOf(t, relay)
	whatsapp, matrix := string(themeAccounts[0].On), string(themeAccounts[1].On)
	want := map[string]*devicev1.ThemeValue{
		"mode":                    {Value: "dark", Source: devicev1.ThemeSource_THEME_SOURCE_SHARED, Shared: "dark"},
		"light.accent":            {Value: "#aaaaaa", Source: devicev1.ThemeSource_THEME_SOURCE_DEVICE, Shared: "#111111", Device: "#aaaaaa"},
		"dark.text":               {Value: "#222222", Source: devicev1.ThemeSource_THEME_SOURCE_SHARED, Shared: "#222222"},
		"dark.accent":             {},
		"people.matrix:@ada:x":    {Value: "#333333", Source: devicev1.ThemeSource_THEME_SOURCE_SHARED, Shared: "#333333"},
		"accounts." + whatsapp:    {Value: "#25d366"},
		"accounts." + matrix:      {Value: "#bbbbbb", Source: devicev1.ThemeSource_THEME_SOURCE_DEVICE, Device: "#bbbbbb"},
		"light.selection":         {},
		"accounts.whatsapp:gone/": nil, // named by neither file nor kithd
	}
	for key, w := range want {
		got := theme.GetValues()[key]
		if w == nil {
			if got != nil {
				t.Errorf("%s = %v, want no key", key, got)
			}
			continue
		}
		if got.GetValue() != w.GetValue() || got.GetSource() != w.GetSource() || got.GetShared() != w.GetShared() || got.GetDevice() != w.GetDevice() {
			t.Errorf("%s = %v, want %v", key, got, w)
		}
	}
	if len(theme.GetValues()) != 1+2*len(config.GUIThemeRoles())+len(themeAccounts)+1 {
		t.Errorf("%d keys, want the mode, every role of both modes, each account and the one person", len(theme.GetValues()))
	}
	if got := theme.GetNetworkColors(); got["WhatsApp"] != "#25d366" || got["Google Messages"] == "" || got["Matrix"] != "" {
		t.Errorf("network colors %v: want WhatsApp's set, Google Messages' default, none for Matrix", got)
	}
	green, _ := themespec.Color("green")
	if got := theme.GetIdentities(); got["whatsapp:15550100@s.whatsapp.net"].GetKey() != "id:Ada" || got["matrix:@ada:x"].GetColor() != green ||
		got["matrix:@bob:x"].GetKey() != "id:matrix:@bob:x" || got["matrix:@bob:x"].GetColor() != "" {
		t.Errorf("identities %v: want the TUI's slot keys and Ada's pinned green", got)
	}
	if want := themespec.SenderColors(themePalette, []string{green}); !reflect.DeepEqual(theme.GetSenderPalette(), want) {
		t.Errorf("sender palette %v, want the TUI's clear of the pinned green", theme.GetSenderPalette())
	}
	if len(theme.GetAccounts()) != 2 || theme.GetAccounts()[0].GetOn() != whatsapp || theme.GetAccounts()[1].GetName() != "@me:x" {
		t.Errorf("accounts %v, want kithd's", theme.GetAccounts())
	}

	back, conceded, err := changeTheme(t.Context(), relay, theme, "light.accent", devicev1.ThemeScope_THEME_SCOPE_DEVICE, "")
	if err != nil || conceded {
		t.Fatalf("removing this device's accent: conceded %v, %v", conceded, err)
	}
	if got := back.GetValues()["light.accent"]; got.GetValue() != "#111111" || got.GetSource() != devicev1.ThemeSource_THEME_SOURCE_SHARED {
		t.Errorf("with this device's accent removed, it is %v; want the shared #111111", got)
	}
	dev, err := config.LoadDevice(config.DevicePath(cfgPath))
	if err != nil || dev.GUI.Theme.Light.Accent != "" {
		t.Errorf("device.toml's light accent is %q (%v), want it removed", dev.GUI.Theme.Light.Accent, err)
	}
}

// What a generated other writer of either file does between the settings page's read
// of the theme and its write.
const (
	// tuiEditor is the TUI's settings editor: the whole config written through kithd
	// (UpdateConfig) on the revision it read.
	tuiEditor = iota
	// handConfig edits config.toml by hand, a comment added.
	handConfig
	// otherWindow is another window's settings page, through the relay.
	otherWindow
	// tuiDevice is the TUI's device writer (config.UpdateDevice).
	tuiDevice
	// handDevice edits device.toml by hand.
	handDevice
	writerKinds
)

// themeWrite is one writer's change: a theme key (or the bridge's [log] level, a key the
// theme is not) to value, in the shared file or the device's.
type themeWrite struct {
	kind  int
	key   string // "log.level" for the bridge's level
	scope devicev1.ThemeScope
	value string
}

// themeKeys are the keys generated writers change; two of each kind, so writers meet.
var themeKeys = []string{"mode", "light.accent", "dark.accent", "people.matrix:@ada:x", "accounts.whatsapp:main/"}

func genThemeWrite(rng *rand.Rand) themeWrite {
	w := themeWrite{kind: rng.IntN(writerKinds), key: themeKeys[rng.IntN(len(themeKeys))]}
	switch w.kind {
	case tuiEditor, handConfig:
		w.scope = devicev1.ThemeScope_THEME_SCOPE_SHARED
		if rng.IntN(3) == 0 {
			w.key = "log.level"
		}
	case otherWindow:
		w.scope = devicev1.ThemeScope(1 + rng.IntN(2))
	default:
		w.scope = devicev1.ThemeScope_THEME_SCOPE_DEVICE
	}
	w.value = genThemeValue(rng, w.key)
	return w
}

func genThemeValue(rng *rand.Rand, key string) string {
	switch {
	case key == "log.level":
		return []string{"debug", "warn", "error"}[rng.IntN(3)]
	case key == "mode":
		return config.GUIThemeModes()[rng.IntN(3)]
	case rng.IntN(5) == 0:
		return "" // removed
	}
	return fmt.Sprintf("#%06x", rng.IntN(1<<24))
}

// apply is w made by its writer.
func (w themeWrite) apply(ctx context.Context, cfgPath string, kithd backendv1connect.BackendServiceClient, relay devicev1connect.DeviceServiceClient) error {
	set := func(cfg *config.Config) {
		if w.key == "log.level" {
			cfg.Log.Level = w.value
		} else {
			cfg.GUI.Theme.Set(w.key, w.value)
		}
	}
	devicePath := config.DevicePath(cfgPath)
	switch w.kind {
	case tuiEditor:
		for {
			got, err := kithd.GetConfig(ctx, connect.NewRequest(&v1.GetConfigRequest{}))
			if err != nil {
				return err
			}
			cfg, err := config.Decode(got.Msg.GetConfig())
			if err != nil {
				return err
			}
			set(&cfg)
			_, err = kithd.UpdateConfig(ctx, connect.NewRequest(&v1.UpdateConfigRequest{BaseRevision: got.Msg.GetRevision(), Config: config.Encode(cfg)}))
			if connect.CodeOf(err) != connect.CodeAborted { // the TUI reads the config again and reapplies
				return err
			}
		}
	case handConfig:
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return err
		}
		set(&cfg)
		return os.WriteFile(cfgPath, []byte(config.Encode(cfg)+"# edited by hand\n"), 0o600)
	case otherWindow:
		got, err := relay.Theme(ctx, connect.NewRequest(&devicev1.ThemeRequest{}))
		if err != nil {
			return err
		}
		_, _, err = changeTheme(ctx, relay, got.Msg, w.key, w.scope, w.value)
		return err
	case tuiDevice:
		was, err := config.LoadDevice(devicePath)
		if err != nil {
			return err
		}
		now := was.Clone()
		now.GUI.Theme.Set(w.key, w.value)
		_, err = config.UpdateDevice(devicePath, was, now)
		return err
	default:
		dev, err := config.LoadDevice(devicePath)
		if err != nil {
			return err
		}
		dev.GUI.Theme.Set(w.key, w.value)
		var b strings.Builder
		b.WriteString("# by hand\n[gui.theme]\n")
		if dev.GUI.Theme.Mode != "" {
			fmt.Fprintf(&b, "mode = %q\n", dev.GUI.Theme.Mode)
		}
		for _, key := range dev.GUI.Theme.Keys() {
			table, entry, _ := strings.Cut(key, ".")
			value, _ := dev.GUI.Theme.Get(key)
			switch {
			case key == "mode":
			case table == config.GUIThemeAccounts || table == config.GUIThemePeople:
				fmt.Fprintf(&b, "[gui.theme.%s]\n%q = %q\n", table, entry, value)
			default:
				fmt.Fprintf(&b, "[gui.theme.%s]\n%s = %q\n", table, entry, value)
			}
		}
		return os.WriteFile(devicePath, []byte(b.String()), 0o600)
	}
}

// valueIn is what the files hold of w's key in w's scope.
func (w themeWrite) valueIn(t *testing.T, cfgPath string) string {
	t.Helper()
	if w.scope == devicev1.ThemeScope_THEME_SCOPE_DEVICE {
		dev, err := config.LoadDevice(config.DevicePath(cfgPath))
		if err != nil {
			t.Fatal(err)
		}
		v, _ := dev.GUI.Theme.Get(w.key)
		return v
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if w.key == "log.level" {
		return cfg.Log.Level
	}
	v, _ := cfg.GUI.Theme.Get(w.key)
	return v
}

// Over generated writers of both files (the TUI's settings editor through kithd, its
// device writer, another window, hand edits), between the settings page's read of the
// theme and its write of one key, or at once with it: every other writer's change is
// kept; the page's own is written, unless a writer changed the same key in the same
// file after the page read it, whose value the page then keeps (conceded, in order
// only); a profile's rail stays the profile's, the shared rail as it was; and the theme
// read after resolves the key as this device's value over the shared one.
func TestThemeWritesKeepEveryOtherWritersChange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	themeConfig(t, cfgPath, config.GUITheme{})
	k := serveThemeKithd(t, cfgPath)
	relay := deviceClient(relayFor(t, k.socket, cfgPath))
	kithd := backendv1connect.NewBackendServiceClient(daemon.Dial(k.socket), "http://kithd")
	for seed := range uint64(120) {
		rng := rand.New(rand.NewPCG(seed, 0x100c)) // #nosec G404 -- reproducible
		var start config.GUITheme
		for _, key := range themeKeys {
			if rng.IntN(2) == 0 {
				start.Set(key, genThemeValue(rng, key))
			}
		}
		themeConfig(t, cfgPath, start)
		handWrite(t, cfgPath, "")
		atOnce := seed%3 == 2
		page := themeWrite{key: themeKeys[rng.IntN(len(themeKeys))], scope: devicev1.ThemeScope(1 + rng.IntN(2))}
		page.value = genThemeValue(rng, page.key)
		shown := themeOf(t, relay)
		others := make([]themeWrite, rng.IntN(3))
		for i := range others {
			others[i] = genThemeWrite(rng)
			if atOnce && (others[i].kind == handConfig || others[i].kind == handDevice) {
				others[i].kind = tuiEditor // a hand edit takes no lock: it races every writer
				if others[i].scope == devicev1.ThemeScope_THEME_SCOPE_DEVICE {
					others[i].kind = tuiDevice
				}
			}
		}
		var got *devicev1.ThemeResponse
		var conceded bool
		var err error
		if atOnce {
			var wg sync.WaitGroup
			for _, w := range others {
				wg.Go(func() {
					if werr := w.apply(t.Context(), cfgPath, kithd, relay); werr != nil {
						t.Errorf("seed %d: %+v: %v", seed, w, werr)
					}
				})
			}
			got, conceded, err = changeTheme(t.Context(), relay, shown, page.key, page.scope, page.value)
			wg.Wait()
		} else {
			for _, w := range others {
				if werr := w.apply(t.Context(), cfgPath, kithd, relay); werr != nil {
					t.Fatalf("seed %d: %+v: %v", seed, w, werr)
				}
			}
			got, conceded, err = changeTheme(t.Context(), relay, shown, page.key, page.scope, page.value)
		}
		if err != nil {
			t.Fatalf("seed %d: the page's write: %v", seed, err)
		}
		checkThemeWrites(t, seed, cfgPath, atOnce, shown, page, others, conceded)
		if !atOnce {
			checkResolved(t, seed, got, page.key)
		}
	}
}

// checkThemeWrites holds TestThemeWritesKeepEveryOtherWritersChange's property over the
// files as the writers left them.
func checkThemeWrites(t *testing.T, seed uint64, cfgPath string, atOnce bool, shown *devicev1.ThemeResponse, page themeWrite, others []themeWrite, conceded bool) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("seed %d: %v", seed, err)
	}
	if want := (config.Rail{Order: []string{"*", "tag:Family"}}); !reflect.DeepEqual(cfg.Display.Rail, want) {
		t.Fatalf("seed %d: the shared rail is %+v, want %+v: a profile's was written over it", seed, cfg.Display.Rail, want)
	}
	if work := cfg.Profiles[1].Rail; !reflect.DeepEqual(work.Hidden, []string{"tag:Family"}) || !reflect.DeepEqual(work.Order, []string{"tag:Family", "*"}) {
		t.Fatalf("seed %d: profile Work's rail is %+v, want its own", seed, work)
	}
	// The page concedes exactly when the key, in its file, holds neither what the page
	// showed nor the page's value once the others wrote (in order: the last of them's).
	same := func(a, b themeWrite) bool { return a.key == b.key && a.scope == b.scope }
	met, after := false, layer(shown, page.key, page.scope)
	for _, w := range others {
		if same(w, page) {
			met, after = true, w.value
		}
	}
	changedBefore := after != layer(shown, page.key, page.scope) && after != page.value
	switch {
	case !atOnce && conceded != changedBefore:
		t.Fatalf("seed %d: the page conceded %v; another writer changed %s first: %v (others %+v)", seed, conceded, page.key, changedBefore, others)
	case atOnce && conceded && !met:
		t.Fatalf("seed %d: the page conceded %s, which no other writer changed", seed, page.key)
	}
	// Each writer's value stands unless another wrote the same key after it (in order:
	// a later one, or the page not conceding; at once: any other).
	for i, w := range others {
		overwritten := !conceded && same(w, page)
		for j, o := range others {
			overwritten = overwritten || (same(o, w) && (j > i || (atOnce && j != i)))
		}
		if atOnce && same(w, page) {
			overwritten = true
		}
		if got := w.valueIn(t, cfgPath); !overwritten && got != w.value {
			t.Fatalf("seed %d: %+v was lost: the file holds %q (others %+v, page %+v)", seed, w, got, others, page)
		}
	}
	if got := page.valueIn(t, cfgPath); !conceded && (!atOnce || !met) && got != page.value {
		t.Fatalf("seed %d: the page wrote %s = %q, the file holds %q (others %+v)", seed, page.key, page.value, got, others)
	}
}

// checkResolved holds that theme resolves key as this device's value over the shared one.
func checkResolved(t *testing.T, seed uint64, theme *devicev1.ThemeResponse, key string) {
	t.Helper()
	v := theme.GetValues()[key]
	switch {
	case v.GetDevice() != "" && (v.GetValue() != v.GetDevice() || v.GetSource() != devicev1.ThemeSource_THEME_SOURCE_DEVICE):
		t.Fatalf("seed %d: %s = %v: this device's value is not the one in force", seed, key, v)
	case v.GetDevice() == "" && v.GetShared() != "" && (v.GetValue() != v.GetShared() || v.GetSource() != devicev1.ThemeSource_THEME_SOURCE_SHARED):
		t.Fatalf("seed %d: %s = %v: the shared value is not the one in force", seed, key, v)
	}
}

// On a device that joined a bridge, a shared write reaches the bridge's config through
// it, and nothing is written here but device.toml, where a device write lands.
func TestAJoinedDeviceWritesTheSharedThemeToTheBridge(t *testing.T) {
	t.Parallel()
	path, laptop := clientDevice(t)
	bridgeConfig := filepath.Join(t.TempDir(), "config.toml")
	themeConfig(t, bridgeConfig, config.GUITheme{})
	b := serveThemeKithd(t, bridgeConfig, laptop)
	handWrite(t, path, fmt.Sprintf("[[bridge]]\nname = \"home\"\nid = %q\naddresses = [%q]\n", b.id, b.addr))
	localBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	relay := deviceClient(openWindow(t, path).socket)
	theme := within(t, 10*time.Second, "the bridge answering", func() *devicev1.ThemeResponse {
		for {
			got, lerr := relay.Theme(t.Context(), connect.NewRequest(&devicev1.ThemeRequest{}))
			if lerr == nil {
				return got.Msg
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	theme, _, err = changeTheme(t.Context(), relay, theme, "dark.accent", devicev1.ThemeScope_THEME_SCOPE_SHARED, "#123456")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = changeTheme(t.Context(), relay, theme, "mode", devicev1.ThemeScope_THEME_SCOPE_DEVICE, "light"); err != nil {
		t.Fatal(err)
	}
	onBridge, err := config.Load(bridgeConfig)
	if err != nil || onBridge.GUI.Theme.Dark.Accent != "#123456" || onBridge.GUI.Theme.Mode != "" {
		t.Errorf("the bridge's config holds dark accent %q and mode %q (%v), want the shared write alone", onBridge.GUI.Theme.Dark.Accent, onBridge.GUI.Theme.Mode, err)
	}
	if localAfter, rerr := os.ReadFile(path); rerr != nil || !bytes.Equal(localAfter, localBefore) {
		t.Errorf("this device's config.toml was written (%v)", rerr)
	}
	dev, err := config.LoadDevice(config.DevicePath(path))
	if err != nil || dev.GUI.Theme.Mode != "light" || dev.GUI.Theme.Dark.Accent != "" || len(dev.Bridges) != 1 {
		t.Errorf("device.toml holds mode %q, dark accent %q and %d bridges (%v); want this device's mode, and its bridge kept", dev.GUI.Theme.Mode, dev.GUI.Theme.Dark.Accent, len(dev.Bridges), err)
	}
}

// A write the window could not draw is refused and nothing is written; a [gui.theme] made
// unreadable by hand is refused to the window (which keeps the theme it had) and to the
// TUI's editor, by kithd.
func TestAThemeTheWindowCannotDrawIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	themeConfig(t, cfgPath, config.GUITheme{})
	k := serveThemeKithd(t, cfgPath)
	relay := deviceClient(relayFor(t, k.socket, cfgPath))
	theme := themeOf(t, relay)
	files := func() string { return snapshotOf(t, cfgPath, config.DevicePath(cfgPath)) }
	before := files()
	for _, w := range []*devicev1.ThemeWrite{
		{Key: "dark.accent", Value: "green", Scope: devicev1.ThemeScope_THEME_SCOPE_SHARED},
		{Key: "dark.accent", Value: "#12345", Scope: devicev1.ThemeScope_THEME_SCOPE_DEVICE},
		{Key: "mode", Value: "sepia", Scope: devicev1.ThemeScope_THEME_SCOPE_SHARED},
		{Key: "dark.border", Value: "#123456", Scope: devicev1.ThemeScope_THEME_SCOPE_DEVICE},
		{Key: "people.", Value: "#123456", Scope: devicev1.ThemeScope_THEME_SCOPE_SHARED},
		{Key: "dark.accent", Value: "#123456"},
	} {
		_, err := relay.UpdateTheme(t.Context(), connect.NewRequest(&devicev1.UpdateThemeRequest{
			ConfigRevision: theme.GetConfigRevision(), DeviceRevision: theme.GetDeviceRevision(),
			Writes: []*devicev1.ThemeWrite{{Key: "light.accent", Value: "#654321", Scope: devicev1.ThemeScope_THEME_SCOPE_SHARED}, w},
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("writing %v = %v, want INVALID_ARGUMENT", w, err)
		}
		if files() != before {
			t.Fatalf("writing %v wrote a file", w)
		}
	}

	handWrite(t, cfgPath, "")
	text, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(cfgPath, append(text, "\n[gui.theme.dark]\naccent = \"blue\"\n"...), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if _, terr := relay.Theme(t.Context(), connect.NewRequest(&devicev1.ThemeRequest{})); connect.CodeOf(terr) != connect.CodeFailedPrecondition {
		t.Errorf("Theme over a hand-edited [gui.theme] = %v, want FAILED_PRECONDITION", terr)
	}
	kithd := backendv1connect.NewBackendServiceClient(daemon.Dial(k.socket), "http://kithd")
	got, err := kithd.GetConfig(t.Context(), connect.NewRequest(&v1.GetConfigRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kithd.UpdateConfig(t.Context(), connect.NewRequest(&v1.UpdateConfigRequest{BaseRevision: got.Msg.GetRevision(), Config: got.Msg.GetConfig()})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("kithd writing a config whose [gui.theme] does not check = %v, want INVALID_ARGUMENT", err)
	}
	handWrite(t, cfgPath, "[gui.theme]\nmode = \"bright\"\n")
	if _, err := relay.Theme(t.Context(), connect.NewRequest(&devicev1.ThemeRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("Theme over a device.toml whose [gui.theme] does not check = %v, want FAILED_PRECONDITION", err)
	}
}

// snapshotOf is the files' bytes, a missing one as none.
func snapshotOf(t *testing.T, paths ...string) string {
	t.Helper()
	var b strings.Builder
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteString("\x00")
	}
	return b.String()
}
