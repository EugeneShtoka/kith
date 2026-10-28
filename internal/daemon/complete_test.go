package daemon_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/schedule"
)

// complete gives d every part a test left out, as kithd builds it: a cache of what the
// backend sends (cacheOf), a config file,
// a send-later queue and an uploads folder in a temp dir, no networks to log in, a silent notifier and log,
// and a config check and reload that accept.
func complete(t *testing.T, d *daemon.Daemon) *daemon.Daemon {
	t.Helper()
	dir := t.TempDir()
	if d.Streams == nil {
		d.Streams = daemon.NewStreams()
	}
	if d.MessageLog == nil {
		d.MessageLog = cacheOf(d.Streams)
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
	if d.Uploads == nil {
		u, err := daemon.NewUploads(filepath.Join(dir, "uploads"))
		if err != nil {
			t.Fatalf("NewUploads: %v", err)
		}
		d.Uploads = u
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

// messageCache stands in for kithd's cache: every message the backend sends is one it
// cached first, numbered as the cache numbers a change, a message seen again moving to
// a new number.
type messageCache struct {
	mu      sync.Mutex
	last    int64
	changes []domain.MessageChange
}

// cacheOf caches what streams' backend sends, from now until it ends, telling streams
// of each change as kithd's cache does.
func cacheOf(streams *daemon.Streams) *messageCache {
	c := &messageCache{}
	_, msgs := streams.SubscribeMessages()
	go func() {
		for m := range msgs {
			c.add(m)
			streams.MessagesChanged()
		}
	}()
	return c
}

func (c *messageCache) add(m domain.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.changes = slices.DeleteFunc(c.changes, func(old domain.MessageChange) bool { return old.Message.ID == m.ID })
	c.last++
	c.changes = append(c.changes, domain.MessageChange{Number: c.last, Message: m})
}

func (c *messageCache) LastChange(context.Context) (string, int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return "test", c.last, nil
}

func (c *messageCache) ChangesAfter(_ context.Context, after int64, limit int) (string, []domain.MessageChange, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []domain.MessageChange
	for i := range c.changes {
		if ch := &c.changes[i]; ch.Number > after && len(out) < limit {
			out = append(out, *ch)
		}
	}
	return "test", out, nil
}
