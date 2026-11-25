package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// lockedChildEnv holds the instance directory TestALockedKeyringIsNeverAskedToUnlock
// runs its child on; the child reads the keyring through go-keyring's real Secret
// Service client, so TestMain leaves it unmocked.
const lockedChildEnv = "KITH_TEST_LOCKED_KEYRING"

const (
	secretsLogin   = dbus.ObjectPath("/org/freedesktop/secrets/collection/login")
	secretsItem    = secretsLogin + "/1"
	secretsSession = dbus.ObjectPath("/org/freedesktop/secrets/session/1")
)

// secrets is a Secret Service holding the cache's key in a login collection, locked
// until its unlockedAt-th read of Locked: someone unlocked it since the retry before.
// It says each Unlock call while locked and each read of Locked.
type secrets struct {
	mu                sync.Mutex
	locked            bool
	reads, unlockedAt int
	key               string
	asked             chan string
}

func (s *secrets) say(what string) {
	select {
	case s.asked <- what:
	default:
	}
}

func (s *secrets) OpenSession(string, dbus.Variant) (dbus.Variant, dbus.ObjectPath, *dbus.Error) {
	return dbus.MakeVariant(""), secretsSession, nil
}

// Unlock unlocks nothing while locked, with no prompt: a prompt nobody answers.
func (s *secrets) Unlock(objects []dbus.ObjectPath) ([]dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked {
		s.say("unlock")
		return []dbus.ObjectPath{}, "/", nil
	}
	return objects, "/", nil
}

func (s *secrets) SearchItems(map[string]string) ([]dbus.ObjectPath, *dbus.Error) {
	return []dbus.ObjectPath{secretsItem}, nil
}

type secretValue struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

func (s *secrets) GetSecret(dbus.ObjectPath) (secretValue, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return secretValue{Session: secretsSession, Parameters: []byte{}, Value: []byte(s.key), ContentType: "text/plain"}, nil
}

func (s *secrets) Close() *dbus.Error { return nil }

// Get answers org.freedesktop.DBus.Properties.Get for the service's Collections and
// the collection's Locked.
func (s *secrets) Get(iface, property string) (dbus.Variant, *dbus.Error) {
	switch iface + "." + property {
	case "org.freedesktop.Secret.Service.Collections":
		return dbus.MakeVariant([]dbus.ObjectPath{secretsLogin}), nil
	case "org.freedesktop.Secret.Collection.Locked":
		s.mu.Lock()
		defer s.mu.Unlock()
		s.say("locked?")
		if s.reads++; s.reads == s.unlockedAt {
			s.locked = false
		}
		return dbus.MakeVariant(s.locked), nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(os.ErrNotExist)
}

// privateBus starts a session bus of the test's own and returns its address.
func privateBus(t *testing.T) string {
	t.Helper()
	busDaemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("no dbus-daemon to run a private bus on")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "bus.conf")
	if err = os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=`+filepath.Join(dir, "bus")+`</listen>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	bus := exec.CommandContext(ctx, busDaemon, "--config-file="+conf, "--nofork", "--print-address=1") // #nosec G204 -- the test's own bus
	out, err := bus.StdoutPipe()
	if err != nil {
		stop()
		t.Fatal(err)
	}
	if err = bus.Start(); err != nil {
		stop()
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); _ = bus.Wait() })
	address, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("the bus's address: %v", err)
	}
	return strings.TrimSpace(address)
}

// kithd started while the keyring is locked never asks it to unlock: its first try and
// its retries only read whether it is still locked, saying the cache is locked meanwhile.
// Every Unlock with no display to prompt on cost gnome-keyring a file descriptor, until
// after some 1000 retries it stopped answering anyone. Once the keyring is unlocked the
// next retry opens the cache.
func TestALockedKeyringIsNeverAskedToUnlock(t *testing.T) {
	if dir := os.Getenv(lockedChildEnv); dir != "" {
		lockedKeyringChild(t, dir)
		return
	}
	address := privateBus(t)
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	storage, lock, key := lockedInstance(t)
	if err = lock.Release(); err != nil { // the child holds it, as kithd
		t.Fatal(err)
	}
	service := &secrets{locked: true, unlockedAt: 3, key: base64.StdEncoding.EncodeToString(key), asked: make(chan string, 64)}
	for path, ifaces := range map[dbus.ObjectPath][]string{
		"/org/freedesktop/secrets": {"org.freedesktop.Secret.Service", "org.freedesktop.DBus.Properties"},
		secretsLogin:               {"org.freedesktop.Secret.Collection", "org.freedesktop.DBus.Properties"},
		secretsItem:                {"org.freedesktop.Secret.Item"},
		secretsSession:             {"org.freedesktop.Secret.Session"},
	} {
		for _, iface := range ifaces {
			if err = conn.Export(service, path, iface); err != nil {
				t.Fatal(err)
			}
		}
	}
	if reply, nameErr := conn.RequestName("org.freedesktop.secrets", dbus.NameFlagDoNotQueue); nameErr != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName() = %v, %v", reply, nameErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestALockedKeyringIsNeverAskedToUnlock$", "-test.v") // #nosec G204 -- this test's own binary
	child.Env = append(os.Environ(), lockedChildEnv+"="+storage.DataDir, "DBUS_SESSION_BUS_ADDRESS="+address)
	var report strings.Builder
	child.Stdout, child.Stderr = &report, &report
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()

	next := func() string {
		t.Helper()
		select {
		case what := <-service.asked:
			return what
		case exitErr := <-exited:
			t.Fatalf("kithd stopped before the keyring was unlocked: %v\n%s", exitErr, report.String())
		case <-ctx.Done():
			t.Fatalf("kithd asked the keyring nothing:\n%s", report.String())
		}
		return ""
	}
	if what := next(); what != "locked?" {
		t.Fatalf("kithd's first try asked a locked keyring to %s; want it only to read whether it is locked", what)
	}
	const says = "the cache is locked: unlock the keyring"
	waitingSays(t, storage, says)
	socket, err := daemon.SocketPath(storage)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, said, statusErr := daemon.NewRemote(socket).Status(ctx); said != says {
		t.Errorf("while the keyring is locked kithd says %q (%v); want %q", said, statusErr, says)
	}
	for range 2 {
		if what := next(); what != "locked?" {
			t.Fatalf("a retry asked a locked keyring to %s; want it only to read whether it is locked", what)
		}
	}
	if err = <-exited; err != nil {
		t.Fatalf("kithd did not open the cache once the keyring was unlocked: %v\n%s", err, report.String())
	}
}

// lockedKeyringChild is kithd opening the cache of the instance at dir.
func lockedKeyringChild(t *testing.T, dir string) {
	t.Helper()
	storage := domain.Storage{Instance: "test", DataDir: dir, StateDir: dir, CacheDir: dir, RuntimeDir: dir, KeyringService: "kith-test"}
	lock, held, err := daemon.Acquire(storage)
	if err != nil || !held {
		t.Fatalf("Acquire() = %v, %v", held, err)
	}
	defer func() { _ = lock.Release() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cache, err := openCache(ctx, slog.New(slog.DiscardHandler), lock, storage)
	if err != nil || cache == nil {
		t.Fatalf("openCache() = %v, %v", cache, err)
	}
	_ = cache.Close()
}
