// Command kithd is kith's daemon: the sole owner of the networks' sessions (Matrix
// /sync and E2EE crypto store, WhatsApp linked devices) and the cache, served to
// clients over a unix socket. It runs without a terminal so notifications keep
// working, and with nothing logged in so `kith login` can reach it.
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

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/buildinfo"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/local"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/matrix"
	"github.com/EugeneShtoka/kith/internal/modelsetup"
	"github.com/EugeneShtoka/kith/internal/route"
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
	// Settled once the config is read; until then the flag or env rules.
	level := &slog.LevelVar{}
	if parsed, err := logging.Resolve(*logLevel, ""); err == nil {
		level.Set(parsed)
	}
	// Read first: `[log] target` says where even its own failure is logged.
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
	var storage domain.Storage
	if err == nil {
		storage, err = setup.StorageFor(cfg, path, *profile)
	}
	if err == nil {
		err = run(log, level, *logLevel, cfg, configFile{path: path, profile: *profile}, storage)
	}
	if err != nil {
		log.Error("kithd stopped", "err", err)
	}
	closeLog()
	if err != nil {
		os.Exit(1)
	}
}

type daemonLog struct {
	flagTarget, configured string // --log-target and `[log] target`
	file                   string // --log-file
	identifier             string // SYSLOG_IDENTIFIER: kithd or kithd-<profile>
	level                  slog.Leveler
	underJournald          bool // stderr is the journal (a systemd unit)
	journal                logging.Journal
}

// newLogger picks the daemon's log. A systemd unit logs to stderr, which journald
// already captures (writing the journal too would double every line). A daemon kith
// started (--log-file) logs to the journal if any, else that file, per `[log] target`.
// In a terminal without --log-file it logs to the terminal unless target is journal.
// Anything that cannot open falls back to stderr, saying so.
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

// run takes the single-instance lock, resumes stored sessions and serves until
// SIGINT/SIGTERM.
func run(log *slog.Logger, level *slog.LevelVar, flagLevel string, cfg config.Config, file configFile, storage domain.Storage) error {
	if warning := config.ModeWarning(file.path); warning != "" {
		log.Warn(warning)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal starts shutdown; restoring the default lets a second end a stuck one.
	context.AfterFunc(ctx, stop)

	relevel, err := settleLevel(log, level, flagLevel, cfg.Log.Level)
	if err != nil {
		return err
	}

	lock, held, err := daemon.Acquire(storage)
	if err != nil {
		return fmt.Errorf("acquire single-instance lock: %w", err)
	}
	if !held {
		// Another daemon won a spawn race, which is what the caller wanted.
		return nil
	}
	// Set when Serve gave up on live handlers: the stores and the lock are then left for
	// process exit to release. Releasing the lock here would let a respawned daemon open
	// the stores while those handlers still write.
	var handlersLive bool
	defer func() {
		if !handlersLive {
			releaseLock(log, lock)
		}
	}()

	if derr := makeStorageDirs(storage); derr != nil {
		return derr
	}

	cache, err := openCache(ctx, log, storage.CachePath())
	if err != nil {
		return err
	}
	backend := newServed(ctx, cache, log, cfg, storage)
	// Registered before configure, so what it starts is stopped on every return.
	defer func() {
		if !handlersLive {
			backend.Stop()
			backend.Close(log)
			closeCache(log, cache)
		}
	}()
	// Networks check their own sections, so only once built; nothing has started yet.
	if cerr := backend.checkConfig(ctx, cfg); cerr != nil {
		return cerr
	}
	configure(log, backend, cfg, storage)

	warnAboutAgentScope(ctx, log, backend, cfg)

	err = serve(ctx, log, relevel, lock, backend, cfg, file)
	if handlersLive = errors.Is(err, daemon.ErrHandlersRunning); handlersLive {
		log.Error("leaving the stores open: handlers were still running at exit", "err", err)
	}
	return err
}

// releaseLock gives up the single-instance lock, logging a failure.
func releaseLock(log *slog.Logger, lock *daemon.Lock) {
	if err := lock.Release(); err != nil {
		log.Warn("release single-instance lock failed", "err", err)
	}
}

// closeCache closes the cache (nil: none), logging a failure.
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
func warnAboutAgentScope(ctx context.Context, log *slog.Logger, places setup.AgentPlaces, cfg config.Config) {
	ctx, cancel := context.WithTimeout(ctx, agentScopeTimeout)
	defer cancel()
	for _, warning := range setup.AgentWarnings(ctx, places, setup.PlacesOf(cfg), cfg.Agent) {
		log.Warn("agent scope: " + warning)
	}
}

// served is the daemon's api.Backend: one router for everything reaching a network and
// the local service for cache and engine answers. A method both offered would be
// ambiguous and fail to compile. matrix is the Matrix adapter itself, for kithd-only
// uses (sync callbacks, key backup), built whether or not the config names an account.
type served struct {
	*route.Router
	*local.Service
	matrix *matrix.Adapter
	// dataDir and schedulePath are this instance's ([storage]).
	dataDir, schedulePath string
	// networks is every network built, Matrix among them when there.
	networks []network
	// stores are the networks' own stores, closed with the backend.
	stores []store
	// cache is the cache every network writes; it learns how much of each room to keep
	// once the notifications' place index exists (newWorkers).
	cache *db.Cache
}

var _ api.Backend = served{}

// newServed builds the networks, the router over them and the service over one cache,
// the service hearing what each network caches.
func newServed(ctx context.Context, cache *db.Cache, log *slog.Logger, cfg config.Config, storage domain.Storage) served {
	networks, mx, stores := openNetworks(ctx, cache, log, cfg, storage)
	adapters := make(map[domain.Protocol]route.Adapter, len(networks))
	for _, n := range networks {
		adapters[n.Network()] = n
	}
	router := route.New(adapters)
	// Before any network writes: whose reactions a trim keeps.
	cache.UseSelves(router.Me)
	service := local.New(cache, router)
	service.UseLogger(log)
	for _, n := range networks {
		if w, ok := n.(cacheWriter); ok {
			w.OnCached(service.MessageCached, service.RoomChanged)
		}
	}
	return served{
		Router: router, Service: service, matrix: mx, networks: networks, stores: stores,
		dataDir: storage.DataDir, schedulePath: storage.SchedulePath(), cache: cache,
	}
}

func (s served) loginLeaders() []api.LoginLeader {
	leaders := make([]api.LoginLeader, 0, len(s.networks))
	for _, n := range s.networks {
		leaders = append(leaders, n)
	}
	return leaders
}

// Close stops the local engines and closes the networks' stores (after Stop
// disconnected their accounts), logging a failure.
func (s served) Close(log *slog.Logger) {
	s.Service.Close()
	for _, st := range s.stores {
		if err := st.Close(); err != nil {
			log.Warn("close a network's store failed", "network", st.name, "err", err)
		}
	}
}

// configure applies the config to the backend; internal/local reads no config itself.
func configure(log *slog.Logger, backend served, cfg config.Config, storage domain.Storage) {
	backend.UseSpell(local.SpellSettings{
		Enabled:      cfg.Spell.SpellEnabled(),
		Command:      cfg.Spell.EngineOrDefault(),
		Dictionaries: cfg.Spell.Dictionaries,
		FlagRare:     cfg.Spell.FlagRareWords,
		RareRatio:    cfg.Spell.RareRatio,
	})
	backend.UseDataDir(storage.DataDir)
	backend.UseModel(modelSettings(log, cfg, storage.KeyringService))
	backend.UseCompletionModel(modelsetup.CompletionModel(cfg.Complete.Model, storage.DataDir))
	backend.UsePlaces(setup.PlacesOf(cfg))
}

// modelSettings translates `[assist]` and `[complete.model]` and reads the API key.
func modelSettings(log *slog.Logger, cfg config.Config, keyring string) local.ModelSettings {
	assist, completion := cfg.Assist, cfg.Complete.Model
	key, kerr := session.Secret(keyring, assist.KeyRef)
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
	return local.ModelSettings{
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

// connectAndSync runs every network. SyncFault drops errors caused by our own shutdown,
// so a clean stop exits 0.
func connectAndSync(ctx context.Context, router *route.Router) error {
	if err := domain.SyncFault(ctx, router.Start(ctx)); err != nil {
		return fmt.Errorf("networks: %w", err)
	}
	return nil
}

// serve runs the workers and the socket, returning only after every worker exited: the
// caller closes the crypto store and cache next.
func serve(
	parent context.Context,
	log *slog.Logger,
	relevel func(configured string),
	lock *daemon.Lock,
	backend served,
	cfg config.Config,
	file configFile,
) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	ln, err := lock.Listen(ctx)
	if err != nil {
		return fmt.Errorf("open socket: %w", err)
	}
	defer func() { _ = ln.Close() }() // Serve's shutdown already closed it

	w, err := newWorkers(log, cfg, backend)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	var syncErr error
	// Before Serve answers: a client polling Status must not see a daemon waiting for
	// nothing yet.
	w.state.Expect(expected(ctx, log, backend)...)
	wg.Go(func() { w.streams.Run(ctx, backend) })
	wg.Go(func() { w.notifications.Run(ctx, w.streams) })
	wg.Go(func() { w.refresher.Run(ctx) })
	wg.Go(func() { w.backups.Run(ctx) })
	wg.Go(func() {
		syncErr = connectAndSync(ctx, backend.Router)
		if syncErr != nil {
			// Clients see this as daemon state; the journal needs it now, not at exit.
			log.Error("sync stopped", "err", syncErr)
		}
		w.state.Failed(syncErr)
	})

	scheduler, cutoff := startScheduler(ctx, log, cfg, backend, backend.schedulePath)
	defer scheduler.Stop()

	logins := daemon.NewLogins(ctx, backend.loginLeaders)
	logins.LogTo(log)
	log.Info("serving", "socket", lock.Socket(), "version", buildinfo.String("kithd"))
	err = daemon.Serve(ctx, ln, &daemon.Daemon{
		Backend:       backend,
		Streams:       w.streams,
		State:         w.state,
		Notifications: w.notifications,
		Scheduler:     scheduler,
		Logins:        logins,
		Log:           log,
		Reload:        reloader(file, relevel, cutoff, backend, w.notifications),
		CheckConfig:   backend.checkConfig,
		Config:        daemon.NewConfigFile(file.path, file.profile),
	})
	// Cancel (not backend.Stop) and join: a sync still decrypting needs the store, and
	// when Serve failed the parent ctx is still live.
	cancel()
	wg.Wait()
	return errors.Join(err, syncErr)
}

// reloader re-reads the file the daemon was started with, never a client payload.
func reloader(
	file configFile,
	relevel func(string),
	cutoff *atomic.Int64,
	backend served,
	notifications *daemon.Notifications,
) daemon.Reload {
	// One reload at a time: interleaved ones would mix both files' settings.
	var one sync.Mutex
	return func(ctx context.Context) error {
		one.Lock()
		defer one.Unlock()
		reloaded, lerr := file.read()
		if lerr != nil {
			return fmt.Errorf("load config: %w", lerr)
		}
		// All or nothing: a config kithd would not start with changes nothing.
		if err := backend.checkConfig(ctx, reloaded); err != nil {
			return err
		}
		relevel(reloaded.Log.Level)
		cutoff.Store(int64(reloaded.Schedule.Cutoff()))
		backend.UseCompletionModel(modelsetup.CompletionModel(reloaded.Complete.Model, backend.dataDir))
		backend.UsePlaces(setup.PlacesOf(reloaded))
		for _, n := range backend.networks {
			n.UseConfig(ctx, reloaded)
		}
		return notifications.Reload(reloaded)
	}
}

// workers is everything the daemon runs beside the socket, wired into the backend's
// callbacks before anything starts.
type workers struct {
	notifications *daemon.Notifications
	refresher     *daemon.Refresher
	state         *daemon.State
	streams       *daemon.Streams
	backups       *daemon.KeyBackup
}

// newWorkers builds the workers and registers their backend callbacks; serve starts them.
func newWorkers(log *slog.Logger, cfg config.Config, backend served) (*workers, error) {
	// Delivery has no caller to tell: each failed sink is logged here, never with the
	// message.
	sinks := setup.NotifierSinks(func(sink string, err error) {
		log.Warn("notification delivery failed", "sink", sink, "err", err)
	})
	notifications, err := daemon.NewNotifications(cfg, backend, sinks)
	if err != nil {
		return nil, fmt.Errorf("notifications: %w", err)
	}
	if uerr := setup.AutoCopyUnavailable(cfg); uerr != nil {
		log.Warn("auto-copy is on but cannot run", "err", uerr)
	}
	notifications.UseLogger(log)
	notifications.UseSelves(backend.Me)
	notifications.UseDirectory(backend.Directory)
	// Before any network writes: retention follows [storage] by room place, and reloads.
	if backend.cache != nil {
		notifications.UseMemberNames(backend.cache.RoomMemberName)
		backend.cache.UseKeep(notifications.MessagesKept)
	}
	refresher := daemon.NewRefresher(backend, notifications.InvalidateScope)
	refresher.UseLogger(log)

	// Readiness is "what the daemon started with has synced", not "socket open": a cold
	// cache looks like no rooms.
	state := daemon.NewState()
	streams := daemon.NewStreams()
	for _, n := range backend.networks {
		n.OnStatus(func(s domain.AccountStatus) { state.Report(daemon.StatusOf(s), time.Now()) })
		// A network rewriting its own rooms needs the scope reread and clients told; one whose
		// rooms went stale needs the refresher, which refreshes every network (not for the
		// first kind: its own refresh would retrigger it).
		if r, ok := n.(roomsRewriter); ok {
			r.OnRoomsChanged(func() {
				notifications.InvalidateScope()
				streams.RoomsChanged() // attached clients reread their room list
			})
		}
		if r, ok := n.(roomsStaler); ok {
			r.OnRoomsStale(refresher.Changed)
		}
	}
	m := backend.matrix
	backups := daemon.NewKeyBackup(m, func(level slog.Level, line string) {
		log.Log(context.Background(), level, "key backup: "+line, "op", "key backup")
	})
	// Logged in after the startup refresh: refresh again with Matrix, and back up what the
	// startup sweep missed.
	m.OnLoggedIn(func() {
		refresher.Changed()
		backups.Soon()
	})
	// A sync means the account is online as of the sync's time, and is the catch-up
	// notifications wait for.
	m.OnSynced(func(t time.Time) {
		state.Report(daemon.StatusOf(m.Online()), t)
		notifications.Synced(t)
	})
	return &workers{
		notifications: notifications,
		refresher:     refresher,
		state:         state,
		streams:       streams,
		backups:       backups,
	}, nil
}

// expected is every account the daemon starts with a session, for State.Expect. A
// network whose sessions cannot be read is not waited for.
func expected(ctx context.Context, log *slog.Logger, backend served) []daemon.NetworkStatus {
	var out []daemon.NetworkStatus
	for _, n := range backend.networks {
		accounts, err := n.SavedSessions(ctx)
		if err != nil {
			log.Warn("read which accounts are logged in failed", "network", n.Network(), "err", err)
		}
		for _, account := range accounts {
			out = append(out, daemon.StatusOf(domain.AccountStatus{Network: n.Network(), Account: account, Phase: daemon.PhaseConnecting}))
		}
	}
	return out
}

// configFile is the config kithd serves: its path, and the [[profile]] it runs as.
type configFile struct{ path, profile string }

// read re-reads the file with its profile selected, so a reload sees the account a
// start does.
func (f configFile) read() (config.Config, error) {
	cfg, err := config.Load(f.path)
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	cfg, err = cfg.Profile(f.profile)
	if err != nil {
		return config.Config{}, fmt.Errorf("select profile: %w", err)
	}
	return cfg, nil
}

// loadConfig reads and validates the config to serve, returning its path for reloads.
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
	if err := setup.Validate(cfg); err != nil {
		return config.Config{}, "", err
	}
	return cfg, path, nil
}

// makeStorageDirs creates the instance's directories, private to this user: stores
// create files, not their directories. The runtime one holds the lock.
func makeStorageDirs(storage domain.Storage) error {
	for _, dir := range []string{storage.DataDir, storage.StateDir, storage.CacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// openCache opens the instance's cache. There is no daemon without it: every network
// writes into it and every client reads from it.
func openCache(ctx context.Context, log *slog.Logger, path string) (*db.Cache, error) {
	cache, err := db.Open(ctx, path)
	if err != nil {
		// A unit retries every few seconds, so fixing the cause is enough.
		return nil, fmt.Errorf("open the cache %s (free disk space or fix its permissions): %w", path, err)
	}
	cache.UseLogger(log)
	if asideErr := cache.AsideFailed(); asideErr != nil {
		log.Warn("the previous cache could not be kept", "err", asideErr)
	}
	return cache, nil
}

// startScheduler starts the send-later queue before Serve, so overdue messages go out
// without a client. The returned atomic holds the overdue cutoff for reloads.
func startScheduler(ctx context.Context, log *slog.Logger, cfg config.Config, sender daemon.Sender, queuePath string) (*daemon.Scheduler, *atomic.Int64) {
	cutoff := &atomic.Int64{}
	cutoff.Store(int64(cfg.Schedule.Cutoff()))

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
