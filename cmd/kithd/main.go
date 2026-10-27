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
	if len(os.Args) > 1 {
		if run, ok := subcommands[os.Args[1]]; ok {
			if err := run(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "kith:", err)
				os.Exit(1)
			}
			return
		}
	}
	showVersion := flag.Bool("version", false, "print version information and exit")
	configPath := flag.String("config", "", "path to config file (default: XDG config dir)")
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
	cfg, path, err := loadConfig(*configPath)
	log, closeLog := newLogger(daemonLog{
		flagTarget:    *logTarget,
		configured:    cfg.Log.Target,
		file:          *logFile,
		identifier:    "kithd",
		level:         level,
		underJournald: logging.UnderJournald(),
		journal:       logging.SystemJournal,
	})
	slog.SetDefault(log)
	var storage domain.Storage
	if err == nil {
		storage, err = setup.StorageFor(cfg, path)
	}
	if err == nil {
		err = run(log, level, *logLevel, cfg, configFile{path: path}, storage)
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
	identifier             string // SYSLOG_IDENTIFIER: kithd
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

	cache, err := openCache(ctx, log, lock, storage)
	if err != nil || cache == nil {
		return err // nil: stopped while waiting for the cache
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
	// dataDir and schedulePath are this instance's ([storage]); storage is all of it,
	// for the daily backup.
	dataDir, schedulePath string
	storage               domain.Storage
	// networks is every network built, Matrix among them when there.
	networks []network
	// stores are the networks' own stores, closed with the backend.
	stores []store
	// cache is the cache every network writes; it learns how much of each room to keep
	// once the notifications' place index exists (newWorkers).
	cache *db.Cache
	// defaults is each network's default account, for Accounts.
	defaults *accountDefaults
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
	// Every network but Matrix keeps its filters' moves here.
	router.UseVerdicts(cache)
	// Before any network writes: whose reactions a trim keeps.
	cache.UseSelves(router.Me)
	if marked, muted, groups, err := setup.HiddenInputs(cfg); err == nil {
		if err := cache.UseHidden(ctx, marked, muted, groups); err != nil {
			log.Warn("find the hidden people's messages", "err", err)
		}
	}
	service := local.New(cache, router)
	service.UseLogger(log)
	for _, n := range networks {
		if w, ok := n.(cacheWriter); ok {
			w.OnCached(service.MessageCached, service.RoomChanged)
		}
	}
	defaults := &accountDefaults{}
	defaults.use(cfg)
	return served{
		Router: router, Service: service, matrix: mx, networks: networks, stores: stores,
		dataDir: storage.DataDir, schedulePath: storage.SchedulePath(), storage: storage, cache: cache, defaults: defaults,
	}
}

// useHidden tells the cache who cfg's muted tags hide.
func (s served) useHidden(ctx context.Context, cfg config.Config) error {
	if s.cache == nil {
		return nil
	}
	marked, muted, groups, err := setup.HiddenInputs(cfg)
	if err != nil {
		return err
	}
	if err := s.cache.UseHidden(ctx, marked, muted, groups); err != nil {
		return fmt.Errorf("find the hidden people's messages: %w", err)
	}
	return nil
}

// Ignore, Unignore and Ignored are Matrix-only (m.ignored_user_list is a Matrix
// account-data event, not a kith-wide concept): they go straight to the Matrix
// adapter rather than through the router. A kithd that configured no Matrix account
// still has the adapter built (newServed always makes one), so a client that calls
// in before login gets Matrix's own "not signed in" style error.
func (s served) Ignore(ctx context.Context, mxid string) error {
	if err := s.matrix.Ignore(ctx, mxid); err != nil {
		return fmt.Errorf("served: %w", err)
	}
	return nil
}

func (s served) Unignore(ctx context.Context, mxid string) error {
	if err := s.matrix.Unignore(ctx, mxid); err != nil {
		return fmt.Errorf("served: %w", err)
	}
	return nil
}

// Ignored reads the mirror rather than the homeserver: the sync keeps it in
// step and a client should not wait on an account-data GET to list what it
// already has. It is every account's list as one, as ignoring applies to all. A
// fresh install (empty cache) answers empty, as it would from the server.
func (s served) Ignored(ctx context.Context) ([]string, error) {
	ids, err := s.cache.MatrixIgnored(ctx)
	if err != nil {
		return nil, fmt.Errorf("served: %w", err)
	}
	return ids, nil
}

// FetchEventSource is Matrix-only (nothing on the other networks exposes a
// comparable raw event): it goes straight to the Matrix adapter rather than
// through the router.
func (s served) FetchEventSource(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]byte, error) {
	src, err := s.matrix.FetchEventSource(ctx, roomID, eventID)
	if err != nil {
		return nil, fmt.Errorf("served: %w", err)
	}
	return src, nil
}

// PublicRooms lists the Matrix public rooms directory. Matrix only; the other
// networks have no equivalent (invite-only, workspace-scoped, phone-contact-
// scoped).
func (s served) PublicRooms(ctx context.Context, on domain.RoomOwner, server, since string) (domain.PublicRoomsPage, error) {
	page, err := s.matrix.PublicRooms(ctx, on, server, since)
	if err != nil {
		return domain.PublicRoomsPage{}, fmt.Errorf("served: %w", err)
	}
	return page, nil
}

// KnockRoom asks to join a knock-only Matrix room; a moderator decides. Matrix
// only (no other adapter has a comparable half-joined state).
func (s served) KnockRoom(ctx context.Context, roomIDOrAlias, reason string, via []string) (domain.RoomID, error) {
	id, err := s.matrix.KnockRoom(ctx, roomIDOrAlias, reason, via)
	if err != nil {
		return "", fmt.Errorf("served: %w", err)
	}
	return id, nil
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
	w.run(ctx, &wg, log, backend, file)
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
	reload := reloader(file, relevel, cutoff, backend, w.notifications)
	// A hand edit to config.toml is picked up without a restart: the watcher
	// runs the same reload the RPC does, and logs the outcome.
	if werr := daemon.WatchConfig(ctx, file.path, reload, log); werr != nil {
		log.Warn("config hot reload is off; edits need a restart or `kith config reload`", "err", werr)
	}
	err = daemon.Serve(ctx, ln, &daemon.Daemon{
		Backend:       backend,
		Streams:       w.streams,
		State:         w.state,
		Notifications: w.notifications,
		Scheduler:     scheduler,
		Logins:        logins,
		Log:           log,
		Reload:        reload,
		CheckConfig:   backend.checkConfig,
		Config:        daemon.NewConfigFile(file.path),
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
		backend.defaults.use(reloaded)
		for _, n := range backend.networks {
			n.UseConfig(ctx, reloaded)
		}
		if err := notifications.Reload(reloaded); err != nil {
			return fmt.Errorf("reload notifications: %w", err)
		}
		// Last: everything else is in force even if the cache cannot be read.
		return backend.useHidden(ctx, reloaded)
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
	// maintenance is held by the cache's vacuum and the daily backup, so they never
	// run at once: the vacuum would put itself off while the backup reads the cache.
	maintenance sync.Mutex
}

// run starts the workers on wg; each runs until ctx ends.
func (w *workers) run(ctx context.Context, wg *sync.WaitGroup, log *slog.Logger, backend served, file configFile) {
	wg.Go(func() { w.streams.Run(ctx, backend) })
	wg.Go(func() { w.notifications.Run(ctx, w.streams) })
	wg.Go(func() { w.refresher.Run(ctx) })
	wg.Go(func() { w.backups.Run(ctx) })
	wg.Go(func() { vacuumCache(ctx, log, backend.cache, &w.maintenance) })
	daily := dailyBackups{log: log, file: file, storage: backend.storage, say: w.notifications.BackupsSay, maintenance: &w.maintenance}
	wg.Go(func() { daily.run(ctx) })
}

// newWorkers builds the workers and registers their backend callbacks; serve starts them.
func newWorkers(log *slog.Logger, cfg config.Config, backend served) (*workers, error) {
	notifications, err := daemon.NewNotifications(cfg, backend)
	if err != nil {
		return nil, fmt.Errorf("notifications: %w", err)
	}
	notifications.UseLogger(log)
	notifications.UseSelves(backend.Me)
	notifications.UseDirectory(backend.Directory)
	// push_archive hands diffs back to the Router's SetRoomArchived, which forwards
	// to the room's network. A network without an archive (Slack, LinkedIn, Matrix)
	// refuses with ErrNotOnNetwork — the reconciler keeps silent on that error.
	notifications.UseArchiver(backend.SetRoomArchived)
	if backend.cache != nil {
		useCache(backend, notifications)
	}
	refresher := daemon.NewRefresher(backend, notifications.InvalidateScope)
	refresher.UseLogger(log)

	// Readiness is "what the daemon started with has synced", not "socket open": a cold
	// cache looks like no rooms.
	state := daemon.NewState()
	streams := daemon.NewStreams()
	for _, n := range backend.networks {
		n.OnStatus(func(s domain.AccountStatus) {
			backend.ForgetMe() // who you are on it may have changed with it
			notifications.AccountChanged(s, state.Report(daemon.StatusOf(s), time.Now()))
		})
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
	backups := daemon.NewKeyBackup(m, func(level slog.Level, account, line string) {
		log.Log(context.Background(), level, "key backup: "+line, "op", "key backup", "account", account)
	})
	// Logged in after the startup refresh: refresh again with Matrix, and back up what the
	// startup sweep missed.
	m.OnLoggedIn(func() {
		refresher.Changed()
		backups.Soon()
	})
	// A sync means the account is online as of the sync's time, and is the catch-up
	// notifications wait for.
	m.OnSynced(func(on domain.RoomOwner, t time.Time) {
		state.Report(daemon.StatusOf(m.Online(on)), t)
		notifications.Synced(on, t)
	})
	return &workers{
		notifications: notifications,
		refresher:     refresher,
		state:         state,
		streams:       streams,
		backups:       backups,
	}, nil
}

// useCache wires notifications and the cache together, before any network writes:
// retention follows [storage] by room place, and reloads; what is cached unannounced
// is judged by the filters too.
func useCache(backend served, notifications *daemon.Notifications) {
	notifications.UseMemberNames(backend.cache.RoomMemberName)
	notifications.UseHidden(backend.cache.Hides)
	// After the first list: nothing is counted yet. Later changes recount the rooms
	// the newly hidden or shown wrote in, and move their direct messages.
	backend.cache.OnHiddenChange(func(ctx context.Context, rooms []domain.RoomID) {
		backend.RecountUnread(ctx, rooms)
		notifications.MoveWithHidden(ctx, rooms)
	})
	backend.cache.UseKeep(notifications.MessagesKept)
	backend.cache.OnArrived(notifications.Arrived)
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

// configFile is the config kithd serves: its path.
type configFile struct{ path string }

// read re-reads the file, so a reload sees what a start does.
func (f configFile) read() (config.Config, error) {
	cfg, err := config.Load(f.path)
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

// loadConfig reads and validates the config to serve, returning its path for reloads.
func loadConfig(configPath string) (config.Config, string, error) {
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

// openCache opens the instance's cache, encrypted under its key in the keyring. There
// is no daemon without it: every network writes into it and every client reads from
// it. While the keyring is locked (a boot with no prompter) or its key does not open
// the cache, kithd waits (waitForCache) rather than exit or start an empty cache. A
// nil cache with no error is a daemon stopped while it waited.
func openCache(ctx context.Context, log *slog.Logger, lock *daemon.Lock, storage domain.Storage) (*db.Cache, error) {
	cache, err := tryCache(ctx, log, storage)
	if locked := (*cacheLocked)(nil); errors.As(err, &locked) {
		cache, err = waitForCache(ctx, log, lock, storage, locked)
	}
	if err != nil || cache == nil {
		return nil, err
	}
	cache.UseLogger(log)
	if asideErr := cache.AsideFailed(); asideErr != nil {
		log.Warn("the previous cache could not be kept", "err", asideErr)
	}
	return cache, nil
}

// cacheLocked is a cache the keyring does not let kithd open yet; why says what to do.
type cacheLocked struct {
	why string
	err error
}

func (e *cacheLocked) Error() string { return e.why + " (" + e.err.Error() + ")" }
func (e *cacheLocked) Unwrap() error { return e.err }

// cacheKey is the key storage's cache is encrypted under, in its keyring, made on
// first use.
func cacheKey(storage domain.Storage) ([]byte, error) {
	key, err := session.LoadOrCreateStoreKey(storage.KeyringService, storage.Instance, "cache")
	if err != nil {
		return nil, fmt.Errorf("the cache's key: %w", err)
	}
	return key, nil
}

// tryCache opens the cache under its key in the keyring. A keyring that cannot be
// read, or a key that does not open the cache, is cacheLocked. A cache from before
// caches were encrypted is encrypted first, once, which is said: it takes a while.
func tryCache(ctx context.Context, log *slog.Logger, storage domain.Storage) (*db.Cache, error) {
	key, err := cacheKey(storage)
	switch {
	case errors.Is(err, session.ErrKeyringNotAnswering):
		return nil, &cacheLocked{why: "the cache is locked: the keyring is not answering; unlock it", err: err}
	case err != nil:
		return nil, &cacheLocked{why: "the cache is locked: unlock the keyring", err: err}
	}
	path := storage.CachePath()
	plain, _ := db.Plain(path) // a file that cannot be read fails in Open, saying why
	start := time.Now()
	if plain {
		log.Info("encrypting the cache, one time", "path", path, "size", fileSize(path))
	}
	cache, err := db.Open(ctx, path, key)
	if plain && err == nil {
		log.Info("the cache is encrypted", "took", time.Since(start).Round(100*time.Millisecond), "size", fileSize(path))
	}
	switch {
	case errors.Is(err, db.ErrWrongKey):
		return nil, &cacheLocked{
			why: "the keyring's key does not open the cache: restore the cache's key in the keyring, or move " +
				storage.CachePath() + " aside to start an empty one",
			err: err,
		}
	case err != nil:
		// A unit retries every few seconds, so fixing the cause is enough.
		return nil, fmt.Errorf("open the cache %s (free disk space or fix its permissions): %w", storage.CachePath(), err)
	}
	return cache, nil
}

// fileSize is the size of the database at path with its log, in MB, for the log.
func fileSize(path string) string {
	var n int64
	for _, f := range []string{path, path + "-wal"} {
		if info, err := os.Stat(f); err == nil {
			n += info.Size()
		}
	}
	return fmt.Sprintf("%.0f MB", float64(n)/1e6)
}

// How long kithd waits between tries at a cache it cannot open yet: cacheRetry at
// first, growing to cacheRetryMax.
const (
	cacheRetry    = 2 * time.Second
	cacheRetryMax = 30 * time.Second
)

// waitForCache tries the cache until it opens, answering on the socket meanwhile as a
// daemon not ready, saying why (daemon.ServeWaiting), so unlocking the keyring is all
// it takes. Nothing else starts until it opens. Stopped while waiting, it returns nil.
func waitForCache(ctx context.Context, log *slog.Logger, lock *daemon.Lock, storage domain.Storage, locked *cacheLocked) (*db.Cache, error) {
	ln, err := lock.Listen(ctx)
	if err != nil {
		return nil, fmt.Errorf("open socket: %w", err)
	}
	var why atomic.Pointer[string]
	say := func(l *cacheLocked) {
		s := l.Error()
		why.Store(&s)
	}
	say(locked)
	waitCtx, stopWaiting := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- daemon.ServeWaiting(waitCtx, ln, func() string { return *why.Load() }) }()
	defer func() {
		stopWaiting()
		if serr := <-served; serr != nil {
			log.Warn("answering while waiting for the cache stopped", "err", serr)
		}
	}()
	log.Error("waiting for the cache", "err", locked)
	for delay := cacheRetry; ; delay = min(2*delay, cacheRetryMax) {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(delay):
		}
		cache, err := tryCache(ctx, log, storage)
		var still *cacheLocked
		if !errors.As(err, &still) {
			if err == nil {
				log.Info("the cache is open")
			}
			return cache, err
		}
		if still.Error() != *why.Load() {
			log.Error("waiting for the cache", "err", still)
		}
		say(still)
	}
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
