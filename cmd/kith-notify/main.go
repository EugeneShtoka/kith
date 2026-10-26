// Command kith-notify shows kithd's desktop notifications on this machine. It holds no
// rules: kithd decides what notifies and publishes each notification to the topic of
// every profile holding its room. kith-notify listens to the profiles it is given (the
// first without one), shows each notification once however many of them carry it, and
// keeps listening across kithd restarts.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/EugeneShtoka/kith/internal/buildinfo"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
)

func main() { os.Exit(start()) }

// start runs kith-notify and returns its exit status, so deferred cleanup runs first.
func start() int {
	showVersion := flag.Bool("version", false, "print version information and exit")
	configPath := flag.String("config", "", "path to config file (default: XDG config dir)")
	logLevel := flag.String("log-level", "", "how much to log to stderr ("+logging.Levels+"); overrides $"+logging.EnvLevel+" and `[log] level`")
	var profiles []string
	flag.Func("profile", "a profile whose notifications to show; repeat for several (default: the first)", func(name string) error {
		profiles = append(profiles, name)
		return nil
	})
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.String("kith-notify"))
		return 0
	}
	level := &slog.LevelVar{}
	if parsed, err := logging.Resolve(*logLevel, ""); err == nil {
		level.Set(parsed)
	}
	log := logging.New(os.Stderr, logging.Options{Level: level, NoTime: logging.UnderJournald()})
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log, level, *logLevel, *configPath, profiles); err != nil {
		log.Error("kith-notify stopped", "err", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, log *slog.Logger, level *slog.LevelVar, flagLevel, configPath string, profiles []string) error {
	cfg, storage, err := loadAccount(log, configPath)
	if err != nil {
		return err
	}
	resolved, err := logging.Resolve(flagLevel, cfg.Log.Level)
	if err != nil {
		return fmt.Errorf("log level: %w", err)
	}
	level.Set(resolved)
	names, err := profileNames(cfg, profiles)
	if err != nil {
		return err
	}
	socket, err := daemon.SocketPath(storage)
	if err != nil {
		return fmt.Errorf("finding the daemon's socket: %w", err)
	}
	// Delivery has no caller to tell: each failed sink is logged, never with the message.
	sinks := setup.NotifierSinks(func(sink string, err error) {
		log.Warn("notification delivery failed", "sink", sink, "err", err)
	})
	a := newAgent(log, func() *daemon.Remote { return daemon.NewRemote(socket) }, sinks, cfg.Notifications)
	a.run(ctx, names)
	return nil
}

// loadAccount reads the config for where kithd's socket is, as kith-mcp does.
func loadAccount(log *slog.Logger, configPath string) (config.Config, domain.Storage, error) {
	path := configPath
	if path == "" {
		resolved, err := config.Path()
		if err != nil {
			return config.Config{}, domain.Storage{}, fmt.Errorf("finding the config: %w", err)
		}
		path = resolved
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, domain.Storage{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if warning := config.ModeWarning(path); warning != "" {
		log.Warn(warning)
	}
	if err = setup.Validate(cfg); err != nil {
		return config.Config{}, domain.Storage{}, fmt.Errorf("reading %s: %w", path, err)
	}
	storage, err := setup.StorageFor(cfg, path)
	if err != nil {
		return config.Config{}, domain.Storage{}, err
	}
	return cfg, storage, nil
}

// profileNames are the profiles named, as the config spells them, or the first when
// none is; one the config lacks is refused naming those it has.
func profileNames(cfg config.Config, named []string) ([]string, error) {
	profiles, err := setup.Profiles(cfg)
	if err != nil {
		return nil, err
	}
	if len(named) == 0 {
		return []string{profiles[0].Name}, nil
	}
	have := make([]string, len(profiles))
	for i, p := range profiles {
		have[i] = p.Name
	}
	out := make([]string, 0, len(named))
	for _, name := range named {
		i := slices.IndexFunc(have, func(p string) bool { return strings.EqualFold(p, strings.TrimSpace(name)) })
		if i < 0 {
			return nil, fmt.Errorf("--profile %q: the config has %s", name, strings.Join(have, ", "))
		}
		out = append(out, have[i])
	}
	return out, nil
}

// The wait before listening again after kithd's stream ended: first, doubling to most.
const (
	retryFirst = time.Second
	retryMost  = 30 * time.Second
)

// configWait bounds reading kithd's config before a notification is shown; the last
// one read is used past it.
const configWait = 5 * time.Second

// agent shows the notifications kithd publishes to its profiles, each once.
type agent struct {
	log   *slog.Logger
	kithd func() *daemon.Remote // a new client of kithd
	sinks func(config.Notifications) notify.Notifier

	mu sync.Mutex
	// seen are the notifications shown lately, by ID: one in several profiles arrives
	// on each of their streams.
	seen recent
	// notifier shows with kithd's [notifications] settings as of revision.
	notifier notify.Notifier
	revision string
}

// newAgent shows with sinks built from notifs until kithd's own config is read.
func newAgent(log *slog.Logger, kithd func() *daemon.Remote, sinks func(config.Notifications) notify.Notifier, notifs config.Notifications) *agent {
	return &agent{log: log, kithd: kithd, sinks: sinks, notifier: sinks(notifs), seen: newRecent(recentIDs)}
}

// run listens to every profile until ctx ends.
func (a *agent) run(ctx context.Context, profiles []string) {
	settings := a.kithd()
	var wg sync.WaitGroup
	for _, profile := range profiles {
		wg.Go(func() { a.listen(ctx, settings, profile) })
	}
	wg.Wait()
}

// listen holds one profile's stream open, opening it again whenever it ends.
func (a *agent) listen(ctx context.Context, settings *daemon.Remote, profile string) {
	r := a.kithd()
	r.UseProfile(profile)
	wait := retryFirst
	for {
		err := r.Notifications(ctx, func() {
			wait = retryFirst
			a.log.Info("listening for notifications", "profile", profile)
		}, func(v daemon.Notice) { a.show(ctx, settings, v) })
		if ctx.Err() != nil {
			return
		}
		a.log.Warn("notifications from kithd stopped; listening again", "profile", profile, "in", wait, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, retryMost)
	}
}

// show shows v unless it was shown already, with kithd's settings as they are now.
func (a *agent) show(ctx context.Context, settings *daemon.Remote, v daemon.Notice) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.seen.add(v.ID) {
		return
	}
	read, cancel := context.WithTimeout(ctx, configWait)
	snap, err := settings.GetConfig(read)
	cancel()
	switch {
	case err != nil:
		a.log.Warn("reading kithd's config; showing with the settings read last", "err", err)
	case snap.Revision != a.revision:
		a.notifier, a.revision = a.sinks(snap.Config.Notifications), snap.Revision
	}
	a.notifier.Notify(v.Alert)
}

// recentIDs is how many notification IDs are remembered: far more than arrive while
// one is on its way down every stream.
const recentIDs = 1024

// recent remembers the last IDs added.
type recent struct {
	ids  map[string]bool
	ring []string
	next int
}

func newRecent(size int) recent {
	return recent{ids: make(map[string]bool, size), ring: make([]string, size)}
}

// add remembers id and reports whether it was new.
func (r *recent) add(id string) bool {
	if r.ids[id] {
		return false
	}
	delete(r.ids, r.ring[r.next])
	r.ring[r.next] = id
	r.ids[id] = true
	r.next = (r.next + 1) % len(r.ring)
	return true
}
