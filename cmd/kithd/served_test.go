package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// servedFor builds the daemon's backend for cfg in a fresh instance.
func servedFor(t *testing.T, cfg config.Config, saved domain.Session) served {
	t.Helper()
	dir := t.TempDir()
	storage := domain.Storage{Instance: "test", DataDir: dir, StateDir: dir, CacheDir: dir, RuntimeDir: dir, KeyringService: "kith-test"}
	cache, err := db.Open(context.Background(), storage.CachePath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	backend, err := newServed(context.Background(), cache, slog.New(slog.DiscardHandler), cfg, storage, saved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		backend.Stop()
		backend.Close(slog.New(slog.DiscardHandler))
	})
	return backend
}

// A config with WhatsApp alone serves without Matrix: nothing to log Matrix in to,
// Matrix's lists empty, and nothing to wait for while no account is linked.
func TestADaemonWithoutMatrixServes(t *testing.T) {
	t.Parallel()
	cfg := config.Config{WhatsApp: config.WhatsApp{Enabled: true, Accounts: []config.WhatsAppAccount{{Name: "bg", Phone: "+359 88 000 0001"}}}}
	backend := servedFor(t, cfg, domain.Session{})
	if backend.matrix != nil || backend.matrixLogin() != nil {
		t.Error("a Matrix adapter without a Matrix account in the config")
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

// Matrix in the config is served logged out until a session takes; the daemon waits
// for it only when one was saved.
func TestMatrixIsWaitedForOnlyWithASavedSession(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Homeserver: "https://matrix.invalid", User: "@me:x"}
	ctx := context.Background()

	none := servedFor(t, cfg, domain.Session{})
	if none.matrix == nil || none.matrixLogin() == nil || none.matrix.LoggedIn() {
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
