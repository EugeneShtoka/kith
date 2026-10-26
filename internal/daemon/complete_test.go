package daemon_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/schedule"
)

// complete gives d every part a test left out, as kithd builds it: a config file and
// a send-later queue in a temp dir, no networks to log in, a silent notifier and log,
// and a config check and reload that accept.
func complete(t *testing.T, d *daemon.Daemon) *daemon.Daemon {
	t.Helper()
	dir := t.TempDir()
	if d.Streams == nil {
		d.Streams = daemon.NewStreams()
	}
	if d.State == nil {
		d.State = daemon.NewState()
	}
	if d.Notifications == nil {
		n, err := daemon.NewNotifications(asStarted(config.Config{}), scopeOnly{d.Backend})
		if err != nil {
			t.Fatalf("NewNotifications: %v", err)
		}
		d.Notifications = n
	}
	if d.Reload == nil {
		d.Reload = func(context.Context) error { return nil }
	}
	if d.CheckConfig == nil {
		d.CheckConfig = func(context.Context, config.Config) error { return nil }
	}
	if d.Config == nil {
		d.Config = daemon.NewConfigFile(filepath.Join(dir, "config.toml"))
	}
	if d.Scheduler == nil {
		d.Scheduler = daemon.NewScheduler(schedule.New(filepath.Join(dir, "scheduled.toml")), d.Backend,
			func() time.Duration { return time.Hour }, nil)
	}
	if d.Logins == nil {
		d.Logins = daemon.NewLogins(t.Context(), func() []api.LoginLeader { return nil })
	}
	if d.Log == nil {
		d.Log = logging.Discard()
	}
	return d
}

// A daemon missing a part is not served, and the refusal names what is missing.
func TestServeRefusesAnIncompleteDaemon(t *testing.T) {
	t.Parallel()
	ln, err := daemon.Listen(context.Background(), filepath.Join(t.TempDir(), "kithd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	d := complete(t, &daemon.Daemon{Backend: &fakeBackend{Nop: nopWithChannels()}})
	d.CheckConfig, d.Logins = nil, nil
	err = daemon.Serve(t.Context(), ln, d)
	if err == nil || !strings.Contains(err.Error(), "CheckConfig, Logins") {
		t.Errorf("Serve = %v, want it to name CheckConfig and Logins", err)
	}
}
