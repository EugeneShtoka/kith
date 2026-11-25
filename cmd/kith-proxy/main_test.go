package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/zalando/go-keyring"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/backendv1connect"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// TestMain keeps this machine's device key off the system keyring: an in-memory one
// stands in.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

// clientConfig is a machine that runs no kithd: a config holding only where it keeps its
// own files.
func clientConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.Save(path, config.Config{Storage: config.Storage{
		Instance: "laptop", DataDir: filepath.Join(dir, "data"), StateDir: filepath.Join(dir, "state"),
		CacheDir: filepath.Join(dir, "cache"), RuntimeDir: filepath.Join(dir, "run"),
	}}); err != nil {
		t.Fatal(err)
	}
	return path
}

// runs starts run on the config at path, returning its result's channel and its stop.
func runs(t *testing.T, path string) (<-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done, finished := make(chan error, 1), make(chan struct{})
	go func() { defer close(finished); done <- run(ctx, slog.New(slog.DiscardHandler), path) }()
	t.Cleanup(func() { cancel(); <-finished })
	return done, cancel
}

// ended is run's result, which must come within a few seconds.
func ended(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return")
		return nil
	}
}

// On a machine that joined no bridge there is nothing to forward: run returns at once,
// listening nowhere, and deletes the store of a bridge it left.
func TestNoBridgeJoinedIsNothingToDo(t *testing.T) {
	path := clientConfig(t)
	storage, _, err := setup.ClientStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storage.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storage.ProxyStorePath(), []byte("left behind"), 0o600); err != nil {
		t.Fatal(err)
	}
	done, _ := runs(t, path)
	if err := ended(t, done); err != nil {
		t.Errorf("run() with no bridge joined = %v, want nil", err)
	}
	if _, err := os.Stat(storage.ProxyStorePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the store of a bridge this machine left: %v, want it deleted", err)
	}
}

// The store is the bridge's it was filled from: opened for another bridge it is made
// anew; for the bridge it was filled from, or one whose key was rotated as the proxy
// started (pinned), it keeps what it holds.
func TestTheStoreIsOneBridges(t *testing.T) {
	storage, _, err := setup.ClientStorage(clientConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storage.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	holds := func(id, pinned string) bool {
		t.Helper()
		s, err := openStore(t.Context(), storage, id, pinned)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		_, ok, err := s.Answer(t.Context(), "Rooms", "Home", emptyKey)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			if err := s.PutAnswer(t.Context(), "Rooms", "Home", emptyKey, []byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		return ok
	}
	for _, step := range []struct {
		name, id, pinned string
		want             bool
	}{
		{"first opened", "B1", "B1", false},
		{"opened again", "B1", "B1", true},
		{"the bridge rotated to B2 as it started", "B2", "B1", true},
		{"opened for B2 since", "B2", "B2", true},
		{"joined another bridge", "C1", "C1", false},
	} {
		if got := holds(step.id, step.pinned); got != step.want {
			t.Errorf("%s: kept what it held = %v, want %v", step.name, got, step.want)
		}
	}
}

// Joined to a bridge, the proxy serves BackendService on this machine's proxy socket,
// forwarding it (a bridge not answering reads as unavailable); a second proxy finds the
// lock held and returns; stopped, the first takes its socket away.
func TestTheProxyServesOneSocketPerMachine(t *testing.T) {
	path := clientConfig(t)
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	device := fmt.Sprintf("[[bridge]]\nname = \"home\"\nid = %q\naddresses = [\"127.0.0.1:1\"]\n", id.String())
	if werr := os.WriteFile(config.DevicePath(path), []byte(device), 0o600); werr != nil {
		t.Fatal(werr)
	}
	storage, _, err := setup.ClientStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	done, stop := runs(t, path)
	deadline := time.Now().Add(10 * time.Second)
	for !daemon.ProxyAnswers(t.Context(), storage) {
		if time.Now().After(deadline) {
			t.Fatal("the proxy's socket never answered")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if info, err := os.Stat(storage.ProxySocketPath()); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Errorf("the proxy's socket = %v, %v; want it owner-only", info.Mode(), err)
	}
	client := backendv1connect.NewBackendServiceClient(daemon.Dial(storage.ProxySocketPath()), "http://kithd")
	if _, err := client.Status(t.Context(), connect.NewRequest(&v1.StatusRequest{})); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("Status through the proxy, the bridge not answering = %v, want unavailable", err)
	}

	second, _ := runs(t, path)
	if err := ended(t, second); err != nil {
		t.Errorf("a second proxy's run() = %v, want nil (the lock held)", err)
	}
	if !daemon.ProxyAnswers(t.Context(), storage) {
		t.Error("the first proxy stopped answering when a second started")
	}

	stop()
	if err := ended(t, done); err != nil {
		t.Errorf("run() once stopped = %v, want nil", err)
	}
	if _, err := os.Stat(storage.ProxySocketPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the proxy's socket after it stopped: %v, want it gone", err)
	}
}
