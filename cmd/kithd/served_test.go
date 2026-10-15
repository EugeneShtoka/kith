package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/zalando/go-keyring"
)

// TestMain keeps tests off the system keyring: it refuses everything, so a Matrix
// session lives in the file fallback (allow_token_file).
func TestMain(m *testing.M) {
	keyring.MockInitWithError(errors.New("no secret service in tests"))
	os.Exit(m.Run())
}

// servedFor builds the daemon's backend for cfg in a fresh instance, with a Matrix
// session saved when saved has one.
func servedFor(t *testing.T, cfg config.Config, saved domain.Session) served {
	t.Helper()
	dir := t.TempDir()
	storage := domain.Storage{Instance: "test", DataDir: dir, StateDir: dir, CacheDir: dir, RuntimeDir: dir, KeyringService: "kith-test"}
	if saved.AccessToken != "" {
		if err := session.Save(session.StoreFor(storage, cfg.User), saved, true); err != nil {
			t.Fatal(err)
		}
	}
	cache, err := db.Open(context.Background(), storage.CachePath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	backend := newServed(context.Background(), cache, slog.New(slog.DiscardHandler), cfg, storage)
	t.Cleanup(func() {
		backend.Stop()
		backend.Close(slog.New(slog.DiscardHandler))
	})
	return backend
}

// A WhatsApp-only config serves: Matrix has no account, empty lists, and nothing to
// wait for.
func TestADaemonWithoutMatrixServes(t *testing.T) {
	t.Parallel()
	cfg := config.Config{WhatsApp: config.WhatsApp{Accounts: []config.WhatsAppAccount{{Name: "home", Phone: "+44 7700 900001"}}}}
	backend := servedFor(t, cfg, domain.Session{})
	if n := backend.matrix.LoginNetwork(); len(n.Accounts) != 0 || backend.matrix.LoggedIn() {
		t.Errorf("Matrix without an account in the config = %+v, logged in %v; want no account", n, backend.matrix.LoggedIn())
	}
	ctx := context.Background()
	if _, err := backend.Rooms(ctx); err != nil {
		t.Errorf("Rooms = %v", err)
	}
	if spaces, err := backend.Spaces(ctx); err != nil || len(spaces) != 0 {
		t.Errorf("Spaces = (%v, %v), want none", spaces, err)
	}
	if got := expected(ctx, slog.New(slog.DiscardHandler), backend); len(got) != 0 {
		t.Errorf("expected = %v with nothing linked, want nothing to wait for", got)
	}
}

// A reload that hides someone recounts the rooms they wrote in, through the network
// that owns each room, onto the daemon's unread stream.
func TestAReloadThatHidesSomeoneRecountsTheirRooms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := config.Config{WhatsApp: config.WhatsApp{Accounts: []config.WhatsAppAccount{{Name: "home", Phone: "+1 555 000 0001"}}}}
	backend := servedFor(t, cfg, domain.Session{})
	const room domain.RoomID = "whatsapp:15550000001/120363@g.us"
	start := time.Unix(1_700_000_000, 0)
	if err := backend.cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "whatsapp:15550000001/A1", RoomID: room, Sender: "whatsapp:15550000002@s.whatsapp.net", Body: "hi", Timestamp: start.Add(time.Minute)},
		{ID: "whatsapp:15550000001/A2", RoomID: room, Sender: "whatsapp:15550000003@s.whatsapp.net", Body: "buy", Timestamp: start.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := backend.cache.SaveUnread(ctx, domain.Unread{RoomID: room}, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	stream := backend.Unread()
	muted := false
	cfg.Tags = []config.Tag{{Name: "Spam", Notifies: &muted, People: []string{"whatsapp:15550000003@s.whatsapp.net"}}}
	if err := backend.useHidden(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-stream:
		if u.RoomID != room || !u.Counted || u.Messages != 1 {
			t.Errorf("streamed %+v, want the room counted at 1 without the hidden person", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hiding someone streamed no recount")
	}
}

// Every network is built whatever the config holds, so a first account needs no
// restart; an empty config waits for nothing.
func TestEveryNetworkIsBuiltWithoutAnAccount(t *testing.T) {
	t.Parallel()
	backend := servedFor(t, config.Config{}, domain.Session{})
	var got []domain.Protocol
	for _, n := range backend.networks {
		got = append(got, n.Network())
	}
	want := []domain.Protocol{
		domain.ProtocolMatrix, domain.ProtocolWhatsApp, domain.ProtocolSlack, domain.ProtocolTelegram, domain.ProtocolMessenger,
		domain.ProtocolLinkedIn, domain.ProtocolGMessages,
	}
	if !slices.Equal(got, want) {
		t.Errorf("networks = %v, want %v", got, want)
	}
	var logins []string
	for _, l := range backend.loginLeaders() {
		logins = append(logins, l.LoginNetwork().Network)
	}
	if want := []string{"matrix", "whatsapp", "slack", "telegram", "messenger", "linkedin", "gmessages"}; !slices.Equal(logins, want) {
		t.Errorf("logins = %v, want %v", logins, want)
	}
	if got := expected(context.Background(), slog.New(slog.DiscardHandler), backend); len(got) != 0 {
		t.Errorf("expected = %v with no account, want nothing to wait for", got)
	}
}

// Matrix is served logged out until a session takes; it is waited for only with a
// saved session.
func TestMatrixIsWaitedForOnlyWithASavedSession(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Homeserver: "https://matrix.invalid", User: "@me:x", AllowTokenFile: true}
	ctx := context.Background()

	none := servedFor(t, cfg, domain.Session{})
	if none.loginLeaders()[0] != api.LoginLeader(none.matrix) || none.matrix.LoggedIn() {
		t.Fatal("want a Matrix adapter, logged out, that can be logged in")
	}
	if got := expected(ctx, slog.New(slog.DiscardHandler), none); len(got) != 0 {
		t.Errorf("expected = %v without a saved session, want nothing", got)
	}

	saved := servedFor(t, cfg, domain.Session{Homeserver: cfg.Homeserver, UserID: cfg.User, DeviceID: "D", AccessToken: "t"})
	got := expected(ctx, slog.New(slog.DiscardHandler), saved)
	if len(got) != 1 || got[0].Network != string(domain.ProtocolMatrix) || got[0].Account != "@me:x" {
		t.Errorf("expected = %v with a saved session, want the Matrix account", got)
	}
}

// A fresh instance's directories are made, private, before any store opens in them.
func TestStorageDirsAreMade(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	storage := domain.Storage{
		DataDir: filepath.Join(root, "data", "kith"), StateDir: filepath.Join(root, "state", "kith"),
		CacheDir: filepath.Join(root, "cache", "kith"),
	}
	if err := makeStorageDirs(storage); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{storage.DataDir, storage.StateDir, storage.CacheDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v; want a 0700 directory", dir, info, err)
		}
	}
	if _, err := db.Open(context.Background(), storage.CachePath()); err != nil {
		t.Errorf("the cache does not open in a made directory: %v", err)
	}
}

// A cache that will not open stops the daemon, naming the file.
func TestACacheThatWillNotOpenIsAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A directory where the file should be: SQLite cannot open it.
	path := filepath.Join(dir, "cache.db")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	cache, err := openCache(context.Background(), slog.New(slog.DiscardHandler), path)
	if err == nil || cache != nil || !strings.Contains(err.Error(), path) {
		t.Errorf("openCache(a directory) = (%v, %v), want an error naming it", cache, err)
	}
}

// Each network judges its own section: an account with no name is refused naming its
// table, at start and on re-read, and the re-read then changes nothing.
func TestEveryNetworkChecksItsOwnSection(t *testing.T) {
	t.Parallel()
	backend := servedFor(t, config.Config{}, domain.Session{})
	ctx := context.Background()
	if err := backend.checkConfig(ctx, config.Config{}); err != nil {
		t.Fatalf("an empty config: %v", err)
	}
	for _, n := range (config.Config{}).Networks() {
		var cfg config.Config
		if err := cfg.Write(n.Table(), map[string]string{"name": ""}); err != nil {
			t.Fatal(err)
		}
		err := backend.checkConfig(ctx, cfg)
		if err == nil || !strings.Contains(err.Error(), n.Table()) {
			t.Errorf("%s: checkConfig = %v, want it refused naming %s", n.Network(), err, n.Table())
		}

		path := filepath.Join(t.TempDir(), "config.toml")
		if err := config.Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		relevel := func(string) { t.Errorf("%s: a refused re-read changed the log level", n.Network()) }
		if err := reloader(configFile{path: path}, relevel, nil, backend, nil)(ctx); err == nil {
			t.Errorf("%s: the re-read took a config its network refuses", n.Network())
		}
	}
}

// A re-read takes the running profile's Matrix account, not another's and not none.
func TestAReReadTakesTheProfilesMatrixAccount(t *testing.T) {
	t.Parallel()
	backend := servedFor(t, config.Config{}, domain.Session{})
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := config.Config{AllowTokenFile: true, Profiles: []config.Profile{
		{Name: "home", Homeserver: "https://home.invalid", User: "@me:home.invalid"},
		{Name: "work", Homeserver: "https://work.invalid", User: "@me:work.invalid"},
	}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	notifications, err := daemon.NewNotifications(config.Config{}, backend, func(config.Notifications) notify.Notifier { return notify.Nop{} })
	if err != nil {
		t.Fatal(err)
	}
	if err := reloader(configFile{path: path, profile: "work"}, func(string) {}, &atomic.Int64{}, backend, notifications)(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := backend.matrix.LoginNetwork().Accounts; len(got) != 1 || got[0].Name != "@me:work.invalid" {
		t.Errorf("Matrix accounts after the re-read = %v, want the work profile's", got)
	}
}
