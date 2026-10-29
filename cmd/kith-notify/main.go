// Command kith-notify shows kithd's desktop notifications on this machine. It holds no
// rules: kithd decides what notifies and publishes each notification to the topic of
// every profile holding its room. kith-notify listens to the profiles it is given (the
// first without one), shows each notification once however many of them carry it, and
// keeps listening across kithd restarts.
package main

import (
	"context"
	"errors"
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

	"github.com/EugeneShtoka/kith/internal/api"
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
	logLevel := flag.String("log-level", "", "how much to log ("+logging.Levels+"); overrides $"+logging.EnvLevel+" and `[log] level`")
	logFile := flag.String(strings.TrimPrefix(daemon.LogFlag, "--"), "", "log to the journal, or to this `file` when there is none, instead of stderr; what kith passes when it starts kith-notify without systemd")
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
	log, closeLog := newLogger(*logFile, level)
	defer closeLog()
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log, level, *logLevel, *configPath, profiles); err != nil {
		log.Error("kith-notify stopped", "err", err)
		return 1
	}
	return 0
}

// newLogger logs to stderr, which a systemd unit's journal captures; started by kith
// (--log-file), to the journal if any, else that file.
func newLogger(file string, level slog.Leveler) (*slog.Logger, func()) {
	stderr := logging.New(os.Stderr, logging.Options{Level: level, NoTime: logging.UnderJournald()})
	if file == "" || logging.UnderJournald() {
		return stderr, func() {}
	}
	sink, err := logging.Open(logging.Destination{Target: logging.TargetAuto, Identifier: "kith-notify", File: file, Level: level})
	if err != nil {
		stderr.Warn("cannot open the log; logging to stderr", "path", file, "err", err)
		return stderr, func() {}
	}
	return sink.Logger, func() {
		if cerr := sink.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, "kith-notify: close log file:", cerr)
		}
	}
}

func run(ctx context.Context, log *slog.Logger, level *slog.LevelVar, flagLevel, configPath string, profiles []string) error {
	cfg, storage, device, err := loadAccount(log, configPath)
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
	release, held, err := daemon.AcquireNotifier(storage)
	if err != nil {
		return fmt.Errorf("the single-instance lock: %w", err)
	}
	if !held {
		log.Info("another kith-notify already shows this config's notifications here")
		return nil
	}
	defer func() {
		if rerr := release(); rerr != nil {
			log.Warn("releasing the single-instance lock", "err", rerr)
		}
	}()
	// Delivery has no caller to tell: each failed sink is logged, never with the message.
	sinks := setup.NotifierSinks(func(sink string, err error) {
		log.Warn("notification delivery failed", "sink", sink, "err", err)
	})
	dev, err := config.LoadDevice(device)
	if err != nil {
		return fmt.Errorf("reading %s: %w", device, err)
	}
	if err := setup.ValidateDevice(dev); err != nil {
		return fmt.Errorf("reading %s: %w", device, err)
	}
	if uerr := setup.AutoCopyUnavailable(cfg, dev); uerr != nil {
		log.Warn("auto-copy is on but cannot run", "err", uerr)
	}
	a := newAgent(log, func() *daemon.Remote { return daemon.NewRemote(socket) }, sinks, device, dev)
	a.run(ctx, names)
	return nil
}

// loadAccount reads the config for where kithd's socket is, as kith-mcp does, after
// moving its device settings out (config.SplitDevice); device is the device file.
func loadAccount(log *slog.Logger, configPath string) (cfg config.Config, storage domain.Storage, device string, err error) {
	path := configPath
	if path == "" {
		resolved, perr := config.Path()
		if perr != nil {
			return config.Config{}, domain.Storage{}, "", fmt.Errorf("finding the config: %w", perr)
		}
		path = resolved
	}
	device = config.DevicePath(path)
	if _, serr := config.SplitDevice(path, device); serr != nil {
		return config.Config{}, domain.Storage{}, "", fmt.Errorf("reading %s: %w", path, serr)
	}
	cfg, err = config.Load(path)
	if err != nil {
		return config.Config{}, domain.Storage{}, "", fmt.Errorf("reading %s: %w", path, err)
	}
	if warning := config.ModeWarning(path); warning != "" {
		log.Warn(warning)
	}
	if err = setup.Validate(cfg); err != nil {
		return config.Config{}, domain.Storage{}, "", fmt.Errorf("reading %s: %w", path, err)
	}
	storage, err = setup.StorageFor(cfg, path)
	if err != nil {
		return config.Config{}, domain.Storage{}, "", err
	}
	return cfg, storage, device, nil
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

// agent shows the notifications kithd publishes to its profiles, each once.
type agent struct {
	log   *slog.Logger
	kithd func() *daemon.Remote // a new client of kithd
	sinks func(config.DeviceNotifications) notify.Notifier
	// device is the device file whose [notifications] and [clipboard] it shows and
	// copies with.
	device string

	mu sync.Mutex
	// seen are the notifications shown lately, by ID: one in several profiles arrives
	// on each of their streams.
	seen recent
	// notifier shows, sounds picks its sound, and clip copies codes, with the device
	// file's [notifications] and [clipboard] as of revision.
	notifier notify.Notifier
	sounds   config.DeviceNotifications
	clip     notify.Clipboard
	revision string
	// toldWaiting is set once kithd's waiting (api.ErrDaemonWaiting) has been shown, so
	// every profile's retries add nothing; a stream opening clears it.
	toldWaiting bool
}

// newAgent shows and copies with dev's settings until the device file at device is
// read again.
func newAgent(log *slog.Logger, kithd func() *daemon.Remote, sinks func(config.DeviceNotifications) notify.Notifier,
	device string, dev config.Device,
) *agent {
	return &agent{
		log: log, kithd: kithd, sinks: sinks, device: device, seen: newRecent(recentIDs),
		notifier: sinks(dev.Notifications), sounds: dev.Notifications, clip: notify.Clipboard{Command: dev.Clipboard.Command},
	}
}

// run listens to every profile until ctx ends.
func (a *agent) run(ctx context.Context, profiles []string) {
	var wg sync.WaitGroup
	for _, profile := range profiles {
		wg.Go(func() { a.listen(ctx, profile) })
	}
	wg.Wait()
}

// listen holds one profile's stream open, opening it again whenever it ends.
func (a *agent) listen(ctx context.Context, profile string) {
	r := a.kithd()
	r.UseProfile(profile)
	wait := retryFirst
	for {
		err := r.Notifications(ctx, func() {
			wait = retryFirst
			a.started()
			a.log.Info("listening for notifications", "profile", profile)
		}, func(v daemon.Notice) { a.show(ctx, v) })
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, api.ErrDaemonWaiting) {
			a.waiting(err)
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

// waiting shows, once until kithd starts, that it cannot start yet and why: nothing
// else would say so while it serves no notifications.
func (a *agent) waiting(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.toldWaiting {
		return
	}
	a.toldWaiting = true
	why := err.Error()
	if _, after, ok := strings.Cut(why, api.ErrDaemonWaiting.Error()+": "); ok {
		why = after
	}
	a.notifier.Notify(notify.Notification{
		Sender: "kith", Room: "kith", Body: "kithd is waiting: " + why,
		Sent: time.Now(), Replaces: "kithd:waiting",
	})
}

// started notes that kithd serves again, so a later wait is shown once more.
func (a *agent) started() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.toldWaiting = false
}

// show shows v unless it was shown already, with the device's settings as they are
// now and the sound they give its rules, first copying the code it announces, if any,
// and saying whether it could.
func (a *agent) show(ctx context.Context, v daemon.Notice) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.seen.add(v.ID) {
		return
	}
	a.reread()
	if !v.Alert.Silent {
		v.Alert.Sound = a.sounds.SoundFor(v.Alert.Rules)
	}
	if v.Code != "" {
		v.Alert.Body = v.Code + " — copied to the clipboard"
		if err := a.clip.Copy(ctx, v.Code); err != nil {
			// The notification carrying the code is then the delivery. The log names the
			// room, never the code.
			a.log.Warn("auto-copy: copy to the clipboard failed", "room", v.Alert.Room, "err", err)
			v.Alert.Body = v.Code + " — could not copy it: " + err.Error()
		}
	}
	a.notifier.Notify(v.Alert)
}

// reread takes the device file's settings again when the file changed since they
// were read, so a change made in the app applies to the next notification.
func (a *agent) reread() {
	data, err := os.ReadFile(a.device)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		a.log.Warn("reading the device file; showing with the settings read last", "err", err)
		return
	}
	revision := config.Revision(data)
	if revision == a.revision {
		return
	}
	dev, err := config.LoadDevice(a.device)
	if err != nil {
		a.log.Warn("reading the device file; showing with the settings read last", "err", err)
		return
	}
	a.notifier, a.sounds, a.revision = a.sinks(dev.Notifications), dev.Notifications, revision
	a.clip = notify.Clipboard{Command: dev.Clipboard.Command}
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
