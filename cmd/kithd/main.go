// Command kithd is kith's daemon: it owns the Matrix session, the /sync loop, the
// cache and the E2EE crypto store, and serves them to clients over a unix socket. It is
// the sole owner of that state (an exclusive lock precedes every store), and runs with
// no terminal open so notifications keep working. It cannot log in; run `kith login`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/adrg/xdg"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/buildinfo"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/matrix"
	"github.com/EugeneShtoka/kith/internal/modelsetup"
	"github.com/EugeneShtoka/kith/internal/schedule"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// agentScopeTimeout bounds the startup warning's cache reads.
const agentScopeTimeout = 3 * time.Second

func main() {
	showVersion := flag.Bool("version", false, "print version information and exit")
	configPath := flag.String("config", "", "path to config file (default: XDG config dir)")
	profile := flag.String("profile", "", "which [[profile]] account to serve (default: the first one)")
	logLevel := flag.String("log-level", "", "how much to log ("+logging.Levels+"); overrides $"+logging.EnvLevel+" and `[log] level`")
	verbose := flag.Bool("v", false, "log at debug level (same as --log-level debug)")
	logTarget := flag.String("log-target", "", "where to log when not a systemd unit ("+logging.Targets+"); overrides $"+logging.EnvTarget+" and `[log] target`. A unit always logs to its stderr, which is the journal")
	logFile := flag.String(strings.TrimPrefix(daemon.LogFlag, "--"), "", "log to this `file` (rotated at 1 MB, one old copy kept) when there is no journal, or always with --log-target file; what kith passes when it starts the daemon without systemd")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.String("kithd"))
		return
	}
	if *verbose && *logLevel == "" {
		*logLevel = "debug"
	}
	// The level is settled once the config is read; until then the flag or env rules.
	level := &slog.LevelVar{}
	if parsed, err := logging.Resolve(*logLevel, ""); err == nil {
		level.Set(parsed)
	}
	// Read first, as `[log] target` says where even its own failure is logged.
	cfg, path, err := loadConfig(*configPath, *profile)
	log, closeLog := newLogger(daemonLog{
		flagTarget:    *logTarget,
		configured:    cfg.Log.Target,
		file:          *logFile,
		identifier:    logging.Identifier("kithd", *profile),
		level:         level,
		underJournald: logging.UnderJournald(),
		journal:       logging.SystemJournal,
	})
	slog.SetDefault(log)
	if err == nil {
		err = run(log, level, *logLevel, cfg, path)
	}
	if err != nil {
		log.Error("kithd stopped", "err", err)
	}
	closeLog()
	if err != nil {
		os.Exit(1)
	}
}

// daemonLog is what decides where the daemon logs.
type daemonLog struct {
	flagTarget, configured string // --log-target and `[log] target`
	file                   string // --log-file
	identifier             string // SYSLOG_IDENTIFIER: kithd or kithd-<profile>
	level                  slog.Leveler
	underJournald          bool // stderr is the journal: a systemd unit
	journal                logging.Journal
}

// newLogger picks the daemon's log. A systemd unit logs to stderr, which journald
// already captures under the unit's name, so writing to the journal as well would
// log every line twice. A daemon kith started (with --log-file) logs to the
// journal when there is one, else to that file, as `[log] target` says. One run in
// a terminal without --log-file logs to the terminal unless target is journal.
// Whatever cannot open falls back to stderr, saying so.
func newLogger(d daemonLog) (*slog.Logger, func()) {
	stderr := logging.New(os.Stderr, logging.Options{Level: d.level, NoTime: d.underJournald})
	target, terr := logging.ResolveTarget(d.flagTarget, d.configured)
	if terr != nil {
		stderr.Warn("log target unknown; using auto", "err", terr)
		target = logging.TargetAuto
	}
	if (d.underJournald && target != logging.TargetFile) || (d.file == "" && target != logging.TargetJournal) {
		return stderr, func() {}
	}
	sink, err := logging.Open(logging.Destination{
		Target: target, Identifier: d.identifier, File: d.file, Level: d.level, Journal: &d.journal,
	})
	if err != nil {
		stderr.Warn("cannot open the log; logging to stderr", "target", string(target), "path", d.file, "err", err)
		return stderr, func() {}
	}
	return sink.Logger, func() {
		if cerr := sink.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, "kithd: close log file:", cerr)
		}
	}
}

// run takes the single-instance lock, resumes the stored session
// and serves until SIGINT/SIGTERM (systemd stops units with SIGTERM).
func run(log *slog.Logger, level *slog.LevelVar, flagLevel string, cfg config.Config, path string) error {
	if warning := config.ModeWarning(path); warning != "" {
		log.Warn(warning)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal starts shutdown; restoring the default lets a second one end a
	// shutdown that is stuck.
	context.AfterFunc(ctx, stop)

	log = log.With("user", cfg.User)
	relevel, err := settleLevel(log, level, flagLevel, cfg.Log.Level)
	if err != nil {
		return err
	}

	lock, held, err := daemon.Acquire(cfg.User)
	if err != nil {
		return fmt.Errorf("acquire single-instance lock: %w", err)
	}
	if !held {
		// Another daemon won a spawn race; that is what the caller wanted.
		return nil
	}
	// Set when Serve gave up on live handlers: the stores are then left for the process
	// exit to release rather than closed under a write, and so is the lock. Released
	// here, it would let a respawned daemon open the same stores while those handlers
	// still write; the kernel drops it when the process exits.
	var handlersLive bool
	defer func() {
		if !handlersLive {
			releaseLock(log, lock)
		}
	}()

	// Read before any store is opened so a logged-out account creates nothing.
	saved, err := storedSession(cfg)
	if err != nil {
		return err
	}

	cache := openCache(ctx, log, cfg.User)
	backend := matrix.New(cache)
	backend.UseLogger(log)
	// Registered before configure, so what it starts is stopped on every return.
	defer func() {
		if !handlersLive {
			backend.Stop()
			closeCache(log, cache)
		}
	}()
	configure(log, backend, cfg)
	prepare, err := resumeSession(ctx, log, backend, cfg.User, saved)
	if err != nil {
		return err
	}

	warnAboutAgentScope(ctx, log, backend, cfg.Agent)

	err = serve(ctx, log, relevel, lock, backend, cfg, path, prepare)
	if handlersLive = errors.Is(err, daemon.ErrHandlersRunning); handlersLive {
		log.Error("leaving the stores open: handlers were still running at exit", "err", err)
	}
	return err
}

// resumeSession restores the saved session. prepare is the homeserver-dependent
// startup left to do, nil when the homeserver answered.
func resumeSession(
	ctx context.Context, log *slog.Logger, backend *matrix.InProc, user string, saved domain.Session,
) (prepare func(context.Context) error, err error) {
	rerr := backend.Resume(ctx, saved)
	if rerr == nil || errors.Is(rerr, api.ErrUnreachable) {
		// Before Serve, and offline too: the state store must be in place before any
		// RPC runs (see matrix.InProc.OpenCryptoStore).
		openCryptoStore(ctx, log, backend, user)
	}
	switch {
	case rerr == nil:
		enableEncryption(ctx, log, backend, user)
		return nil, nil
	case errors.Is(rerr, api.ErrUnreachable):
		// Serve the on-disk cache offline and connect once the homeserver is back.
		log.Warn("cannot reach the homeserver; serving cached history and connecting when it is back", "err", rerr)
		return func(ctx context.Context) error {
			if werr := waitForHomeserver(ctx, backend); werr != nil {
				return werr
			}
			log.Info("homeserver reachable again; syncing")
			enableEncryption(ctx, log, backend, user)
			return nil
		}, nil
	default:
		return nil, fmt.Errorf("saved session for %s is unusable (run `kith login` again): %w", user, rerr)
	}
}

// releaseLock gives up the single-instance lock on the way out, logging a failure.
func releaseLock(log *slog.Logger, lock *daemon.Lock) {
	if err := lock.Release(); err != nil {
		log.Warn("release single-instance lock failed", "err", err)
	}
}

// closeCache closes the cache on the way out (nil: there was none), logging a failure.
func closeCache(log *slog.Logger, cache *db.Cache) {
	if cache == nil {
		return
	}
	if err := cache.Close(); err != nil {
		log.Warn("close cache failed", "err", err)
	}
}

// settleLevel applies the config's level (the flag and $KITH_LOG_LEVEL still win) and
// returns what a reload calls to apply a changed one.
func settleLevel(log *slog.Logger, level *slog.LevelVar, flagLevel, configured string) (func(string), error) {
	resolved, err := logging.Resolve(flagLevel, configured)
	if err != nil {
		return nil, fmt.Errorf("log level: %w", err)
	}
	level.Set(resolved)
	return func(configured string) {
		if next, lerr := logging.Resolve(flagLevel, configured); lerr != nil {
			log.Warn("reload: log level unchanged", "err", lerr)
		} else {
			level.Set(next)
		}
	}, nil
}

// warnAboutAgentScope logs the `[agent.write]` entries `[agent.read]` rules out. Never fatal.
func warnAboutAgentScope(ctx context.Context, log *slog.Logger, places setup.AgentPlaces, agent config.Agent) {
	ctx, cancel := context.WithTimeout(ctx, agentScopeTimeout)
	defer cancel()
	for _, warning := range setup.AgentWarnings(ctx, places, agent) {
		log.Warn("agent scope: " + warning)
	}
}

// configure applies the config to the backend; internal/matrix reads no config itself.
func configure(log *slog.Logger, backend *matrix.InProc, cfg config.Config) {
	backend.UseSpell(matrix.SpellSettings{
		Enabled:      cfg.Spell.SpellEnabled(),
		Command:      cfg.Spell.EngineOrDefault(),
		Dictionaries: cfg.Spell.Dictionaries,
		FlagRare:     cfg.Spell.FlagRareWords,
		RareRatio:    cfg.Spell.RareRatio,
	})
	backend.UseModel(modelSettings(log, cfg))
	backend.UseCompletionModel(modelsetup.CompletionModel(cfg.Complete.Model, xdg.DataHome))
	backend.KeepDeleted(cfg.Display.Deleted.Keep())
	backend.UseIdentities(context.Background(), identityGroups(cfg))
}

// identityGroups is each [[display.identity]]'s user IDs.
func identityGroups(cfg config.Config) [][]string {
	groups := make([][]string, 0, len(cfg.Display.Identities))
	for _, ident := range cfg.Display.Identities {
		groups = append(groups, ident.IDs)
	}
	return groups
}

// modelSettings translates `[assist]` and `[complete.model]` and reads the API key. A
// missing key is not fatal: the endpoint's refusal reaches the composer instead.
func modelSettings(log *slog.Logger, cfg config.Config) matrix.ModelSettings {
	assist, completion := cfg.Assist, cfg.Complete.Model
	key, kerr := session.Secret(assist.KeyRef)
	if kerr != nil && assist.KeyRef != "" {
		// Not fatal (the endpoint's refusal reaches the composer), but say why here.
		log.Warn("read the language model's API key failed", "key_ref", assist.KeyRef, "err", kerr)
	}
	// Already checked by setup.Validate; fall back to defaults defensively.
	pick, err := setup.CompletionPick(completion)
	if err != nil {
		log.Error("[complete.model] pick is invalid; using the default", "err", err)
		pick, _ = setup.CompletionPick(config.CompleteModel{}) // the zero section always parses
	}
	return matrix.ModelSettings{
		Endpoint: assist.Endpoint,
		Model:    assist.Model,
		Key:      key,
		Scope: domain.ModelScope{
			Only:      assist.Rooms,
			Except:    assist.Except,
			Encrypted: assist.Encrypted,
		},
		Budget:    assist.BudgetOrDefault(),
		Budgets:   setup.ModelBudgets(assist, completion),
		Pick:      pick,
		Options:   setup.CompletionCount(completion),
		TodoRooms: setup.TodoRooms(assist),
		PerMinute: assist.RateOrDefault(),
		Timeout:   assist.TimeoutOrDefault(),
		Tasks:     modelsetup.ModelTasks(assist),
	}
}

// waitForHomeserver polls until the homeserver answers, ctx ends, or the session is
// rejected. Backoff is capped at 30s so a laptop waking from suspend reconnects quickly.
func waitForHomeserver(ctx context.Context, backend *matrix.InProc) error {
	const (
		first = 2 * time.Second
		most  = 30 * time.Second
	)
	for wait := first; ; {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the homeserver: %w", ctx.Err())
		case <-time.After(wait):
		}
		switch err := backend.Reachable(ctx); {
		case err == nil:
			return nil
		case errors.Is(err, api.ErrUnreachable):
		default:
			return fmt.Errorf("saved session is unusable (run `kith login` again): %w", err)
		}
		if wait *= 2; wait > most {
			wait = most
		}
	}
}

// connectAndSync finishes homeserver-dependent startup, then runs the sync loop.
// SyncFault drops errors caused by our own shutdown, so a clean stop exits 0.
func connectAndSync(ctx context.Context, backend *matrix.InProc, prepare func(context.Context) error) error {
	if prepare != nil {
		if err := daemon.SyncFault(ctx, prepare(ctx)); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
	}
	if err := daemon.SyncFault(ctx, backend.Start(ctx)); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

// serve runs the workers and the socket, and returns only after every worker has
// exited: the caller closes the crypto store and cache right after.
func serve(
	parent context.Context,
	log *slog.Logger,
	relevel func(configured string),
	lock *daemon.Lock,
	backend *matrix.InProc,
	cfg config.Config,
	configPath string,
	prepare func(context.Context) error,
) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	ln, err := lock.Listen(ctx)
	if err != nil {
		return fmt.Errorf("open socket: %w", err)
	}
	defer func() { _ = ln.Close() }() // Serve's shutdown has closed it already

	w, err := newWorkers(log, cfg, backend)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	var syncErr error
	wg.Add(5)
	go func() { defer wg.Done(); w.streams.Run(ctx, backend) }()
	go func() { defer wg.Done(); w.notifications.Run(ctx, w.streams) }()
	go func() { defer wg.Done(); w.refresher.Run(ctx) }()
	go func() { defer wg.Done(); w.backups.Run(ctx) }()
	go func() {
		defer wg.Done()
		syncErr = connectAndSync(ctx, backend, prepare)
		if syncErr != nil {
			// Clients see this as the daemon's state; the journal needs it at once,
			// not only when the process exits.
			log.Error("sync stopped", "err", syncErr)
		}
		w.state.Failed(syncErr)
	}()

	scheduler, cutoff := startScheduler(ctx, log, cfg, backend)
	defer scheduler.Stop()

	log.Info("serving", "socket", lock.Socket(), "version", buildinfo.String("kithd"))
	err = daemon.Serve(ctx, ln, &daemon.Daemon{
		Backend:       backend,
		Streams:       w.streams,
		State:         w.state,
		Notifications: w.notifications,
		Scheduler:     scheduler,
		Log:           log,
		Reload:        reloader(configPath, relevel, cutoff, backend, w.notifications),
	})
	// Cancel (not backend.Stop) and join: a sync still decrypting needs the store, and
	// when Serve failed the parent ctx is still live.
	cancel()
	wg.Wait()
	return errors.Join(err, syncErr)
}

// reloader re-reads the file the daemon was started with, never a client payload.
func reloader(
	configPath string,
	relevel func(string),
	cutoff *atomic.Int64,
	backend *matrix.InProc,
	notifications *daemon.Notifications,
) daemon.Reload {
	// One reload at a time: two interleaved would leave a mix of both files' settings.
	var one sync.Mutex
	return func(ctx context.Context) error {
		one.Lock()
		defer one.Unlock()
		reloaded, lerr := config.Load(configPath)
		if lerr != nil {
			return fmt.Errorf("load config: %w", lerr)
		}
		relevel(reloaded.Log.Level)
		cutoff.Store(int64(reloaded.Schedule.Cutoff()))
		backend.UseCompletionModel(modelsetup.CompletionModel(reloaded.Complete.Model, xdg.DataHome))
		backend.UseIdentities(ctx, identityGroups(reloaded))
		return notifications.Reload(reloaded)
	}
}

// workers is everything the daemon runs beside the socket, wired together and into
// the backend's callbacks before anything starts.
type workers struct {
	notifications *daemon.Notifications
	refresher     *daemon.Refresher
	state         *daemon.State
	streams       *daemon.Streams
	backups       *daemon.KeyBackup
}

// newWorkers builds the workers and registers their backend callbacks; serve starts them.
func newWorkers(log *slog.Logger, cfg config.Config, backend *matrix.InProc) (*workers, error) {
	// Delivery happens in the background with no caller to tell: each failed sink is
	// logged here, by sink, never with the message.
	sinks := setup.NotifierSinks(func(sink string, err error) {
		log.Warn("notification delivery failed", "sink", sink, "err", err)
	})
	notifications, err := daemon.NewNotifications(cfg, backend, cfg.User, sinks)
	if err != nil {
		return nil, fmt.Errorf("notifications: %w", err)
	}
	if uerr := setup.AutoCopyUnavailable(cfg); uerr != nil {
		log.Warn("auto-copy is on but cannot run", "err", uerr)
	}
	notifications.UseLogger(log)
	refresher := daemon.NewRefresher(backend, notifications.InvalidateScope)
	refresher.UseLogger(log)
	backend.OnRoomsChanged(refresher.Changed)

	// Readiness is "a sync arrived", not "socket open": a cold cache looks like no rooms.
	state := daemon.NewState()
	backend.OnSynced(func(t time.Time) {
		state.Synced(t)
		notifications.Synced(t)
	})
	backups := daemon.NewKeyBackup(backend, func(level slog.Level, line string) {
		log.Log(context.Background(), level, "key backup: "+line, "op", "key backup")
	})
	return &workers{
		notifications: notifications,
		refresher:     refresher,
		state:         state,
		streams:       daemon.NewStreams(),
		backups:       backups,
	}, nil
}

// loadConfig reads and validates the config for the account to serve, returning the
// path too: ReloadConfig re-reads the same file.
func loadConfig(configPath, profile string) (config.Config, string, error) {
	path := configPath
	if path == "" {
		resolved, err := config.Path()
		if err != nil {
			return config.Config{}, "", fmt.Errorf("resolve config path: %w", err)
		}
		path = resolved
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, "", fmt.Errorf("load config: %w", err)
	}
	cfg, err = cfg.Profile(profile)
	if err != nil {
		return config.Config{}, "", fmt.Errorf("select profile: %w", err)
	}
	if cfg.User == "" {
		return config.Config{}, "", errors.New("no `user` in the config; set it and run `kith login`")
	}
	if err := setup.Validate(cfg); err != nil {
		return config.Config{}, "", err
	}
	return cfg, path, nil
}

// storedSession reads the session saved by `kith login`; there is no password fallback.
func storedSession(cfg config.Config) (domain.Session, error) {
	saved, found, err := session.Load(cfg.User, cfg.AllowTokenFile)
	if err != nil {
		return domain.Session{}, fmt.Errorf("load session: %w", err)
	}
	if !found {
		return domain.Session{}, fmt.Errorf("no saved session for %s; run `kith login` first", cfg.User)
	}
	return saved, nil
}

// enableEncryption turns on E2EE. Failures only warn: unencrypted rooms still work.
// openCryptoStore opens the crypto/state store, logging a failure: encryption then
// stays off and encrypted rooms refuse sends.
func openCryptoStore(ctx context.Context, log *slog.Logger, backend *matrix.InProc, user string) {
	dbPath, err := db.CryptoPath(user)
	if err == nil {
		err = backend.OpenCryptoStore(ctx, dbPath)
	}
	if err != nil {
		log.Error("encryption disabled", "err", err)
	}
}

func enableEncryption(ctx context.Context, log *slog.Logger, backend *matrix.InProc, user string) {
	pickleKey, err := session.LoadOrCreatePickleKey(user)
	if err != nil {
		log.Error("encryption disabled", "err", err)
		return
	}
	if err := backend.EnableEncryption(ctx, pickleKey); err != nil {
		log.Error("encryption disabled", "err", err)
		return
	}
	if verr := backend.VerificationUnavailable(); verr != nil {
		log.Warn("device verification unavailable", "err", verr)
	}
}

// openCache opens the per-user cache, or returns nil with a warning: without it the
// backend falls through to the network.
func openCache(ctx context.Context, log *slog.Logger, user string) *db.Cache {
	path, err := db.DefaultPath(user)
	if err != nil {
		log.Error("cache disabled", "err", err)
		return nil
	}
	cache, err := db.Open(ctx, path)
	if err != nil {
		log.Error("cache disabled", "path", path, "err", err)
		return nil
	}
	cache.UseLogger(log)
	cache.UseAccount(user)
	if asideErr := cache.AsideFailed(); asideErr != nil {
		log.Warn("the previous cache could not be kept", "err", asideErr)
	}
	return cache
}

// startScheduler starts the send-later queue before Serve, so overdue messages go out
// without waiting for a client. The returned atomic holds the overdue cutoff so a
// reload can change it.
func startScheduler(ctx context.Context, log *slog.Logger, cfg config.Config, sender daemon.Sender) (*daemon.Scheduler, *atomic.Int64) {
	cutoff := &atomic.Int64{}
	cutoff.Store(int64(cfg.Schedule.Cutoff()))

	queuePath, err := schedule.DefaultPath(cfg.User)
	if err != nil {
		log.Error("scheduled messages unavailable", "err", err)
		return nil, cutoff
	}
	scheduler := daemon.NewScheduler(
		schedule.New(queuePath), sender,
		func() time.Duration { return time.Duration(cutoff.Load()) },
		func(level slog.Level, line string) {
			log.Log(context.Background(), level, "scheduler: "+line, "op", "scheduled send")
		},
	)
	scheduler.Start(ctx)
	return scheduler, cutoff
}
