// Command kithd is kith's daemon: it owns the networks' sessions (Matrix's /sync loop
// and E2EE crypto store, the WhatsApp linked devices) and the cache, and serves them
// to clients over a unix socket. It is the sole owner of that state (an exclusive lock
// precedes every store), and runs with no terminal open so notifications keep working.
// It runs logged in to nothing too: `kith login` logs a network in through it.
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
	"github.com/EugeneShtoka/kith/internal/modelsetup"
	"github.com/EugeneShtoka/kith/internal/route"
	"github.com/EugeneShtoka/kith/internal/schedule"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/slack"
	"github.com/EugeneShtoka/kith/internal/telegram"
	"github.com/EugeneShtoka/kith/internal/whatsapp"
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
	var storage domain.Storage
	if err == nil {
		storage, err = setup.StorageFor(cfg, path, *profile)
	}
	if err == nil {
		err = run(log, level, *logLevel, cfg, path, storage)
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
func run(log *slog.Logger, level *slog.LevelVar, flagLevel string, cfg config.Config, path string, storage domain.Storage) error {
	if warning := config.ModeWarning(path); warning != "" {
		log.Warn(warning)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal starts shutdown; restoring the default lets a second one end a
	// shutdown that is stuck.
	context.AfterFunc(ctx, stop)

	if cfg.HasMatrix() {
		log = log.With("user", cfg.User)
	}
	relevel, err := settleLevel(log, level, flagLevel, cfg.Log.Level)
	if err != nil {
		return err
	}

	lock, held, err := daemon.Acquire(storage)
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

	saved, err := prepareInstance(cfg, storage)
	if err != nil {
		return err
	}

	cache, err := openCache(ctx, log, storage.CachePath(), cfg.User)
	if err != nil {
		return err
	}
	backend := newServed(ctx, cache, log, cfg, storage, saved)
	// Registered before configure, so what it starts is stopped on every return.
	defer func() {
		if !handlersLive {
			backend.Stop()
			backend.Close(log)
			closeCache(log, cache)
		}
	}()
	configure(log, backend, cfg, storage)

	warnAboutAgentScope(ctx, log, backend, cfg)

	err = serve(ctx, log, relevel, lock, backend, cfg, path)
	if handlersLive = errors.Is(err, daemon.ErrHandlersRunning); handlersLive {
		log.Error("leaving the stores open: handlers were still running at exit", "err", err)
	}
	return err
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
func warnAboutAgentScope(ctx context.Context, log *slog.Logger, places setup.AgentPlaces, cfg config.Config) {
	ctx, cancel := context.WithTimeout(ctx, agentScopeTimeout)
	defer cancel()
	for _, warning := range setup.AgentWarnings(ctx, places, setup.PlacesOf(cfg), cfg.Agent) {
		log.Warn("agent scope: " + warning)
	}
}

// served is the daemon's api.Backend: the networks behind one router for everything
// that reaches a network, and the local service for what the cache and the engines
// answer. A method both offered would make it ambiguous and fail to compile, so none
// is served twice. matrix is the Matrix adapter itself, for what only kithd does
// with it (the session, encryption, the sync callbacks); nil when the config names
// no Matrix account.
type served struct {
	*route.Router
	*local.Service
	matrix *matrixAdapter
	// dataDir and schedulePath are this instance's ([storage]).
	dataDir, schedulePath string
	// whatsapp and its store are nil unless [whatsapp] is enabled and the store opened.
	whatsapp      *whatsapp.Adapter
	whatsappStore *whatsapp.Store
	// slack is nil unless [slack] is enabled.
	slack *slack.Adapter
	// telegram is nil unless the config has a [[telegram.account]].
	telegram *telegram.Adapter
}

var _ api.Backend = served{}

// newServed builds the adapters (Matrix when the config names an account, starting
// from saved), the router over them and the service over one cache, the service
// hearing what each adapter caches.
func newServed(ctx context.Context, cache *db.Cache, log *slog.Logger, cfg config.Config, storage domain.Storage, saved domain.Session) served {
	// A nil *matrixAdapter in the map would be a Matrix that is there.
	var adapter *matrixAdapter
	adapters := map[domain.Protocol]route.Adapter{}
	if cfg.HasMatrix() {
		adapter = newMatrixAdapter(cache, log, matrixAccount{
			homeserver: cfg.Homeserver, user: cfg.User, allowTokenFile: cfg.AllowTokenFile,
			crypto: cryptoPlace{path: storage.CryptoPath(), keys: session.StoreFor(storage, cfg.User)},
		}, saved)
		adapters[domain.ProtocolMatrix] = adapter
	}
	wa, waStore := openWhatsApp(ctx, cache, log, cfg, storage.WhatsAppPath())
	if wa != nil {
		adapters[domain.ProtocolWhatsApp] = wa
	}
	sl := openSlack(cache, log, cfg, storage)
	if sl != nil {
		adapters[domain.ProtocolSlack] = sl
	}
	tg := openTelegram(cache, log, cfg, storage)
	if tg != nil {
		adapters[domain.ProtocolTelegram] = tg
	}
	router := route.New(adapters)
	service := local.New(cache, router)
	service.UseLogger(log)
	if adapter != nil {
		adapter.OnCached(service.MessageCached, service.RoomChanged)
	}
	if wa != nil {
		wa.OnCached(service.MessageCached, service.RoomChanged)
	}
	if sl != nil {
		sl.OnCached(service.MessageCached, service.RoomChanged)
	}
	return served{
		Router: router, Service: service, matrix: adapter, whatsapp: wa, whatsappStore: waStore, slack: sl, telegram: tg,
		dataDir: storage.DataDir, schedulePath: storage.SchedulePath(),
	}
}

// openWhatsApp is the WhatsApp adapter when [whatsapp] is enabled, over its session
// store. A store that will not open leaves WhatsApp off, logged, rather than Matrix
// down with it.
func openWhatsApp(ctx context.Context, cache *db.Cache, log *slog.Logger, cfg config.Config, path string) (*whatsapp.Adapter, *whatsapp.Store) {
	if !cfg.WhatsApp.Enabled {
		return nil, nil
	}
	store, err := whatsapp.OpenStore(ctx, path, whatsapp.NewStoreLogger(log))
	if err != nil {
		log.Error("WhatsApp is off: its store will not open", "path", path, "err", err)
		return nil, nil
	}
	return whatsapp.New(cache, store, whatsAppAccounts(cfg), log), store
}

// whatsAppAccounts is [[whatsapp.account]] as the adapter takes it.
func whatsAppAccounts(cfg config.Config) []whatsapp.Account {
	accounts := make([]whatsapp.Account, 0, len(cfg.WhatsApp.Accounts))
	for _, a := range cfg.WhatsApp.Accounts {
		accounts = append(accounts, whatsapp.Account{Name: a.Name, Digits: a.Digits()})
	}
	return accounts
}

// whatsAppLink is what pairs WhatsApp accounts: nil, not a nil adapter, when WhatsApp
// is off, so the handler can tell.
func (s served) whatsAppLink() api.WhatsAppLink {
	if s.whatsapp == nil {
		return nil
	}
	return s.whatsapp
}

// slackSignIn is what signs Slack in: nil, not a nil adapter, when [slack] is off, so
// the handler can tell.
func (s served) slackSignIn() api.SlackSignIn {
	if s.slack == nil {
		return nil
	}
	return s.slack
}

// telegramLogin is what logs Telegram in: nil, not a nil adapter, when the daemon runs
// no Telegram account, so the handler can tell.
func (s served) telegramLogin() api.TelegramLogin {
	if s.telegram == nil {
		return nil
	}
	return s.telegram
}

// matrixLogin is what logs Matrix in: nil, not a nil adapter, when the config names
// no Matrix account, so the handler can tell.
func (s served) matrixLogin() api.MatrixLogin {
	if s.matrix == nil {
		return nil
	}
	return s.matrix
}

// Close stops the local engines and closes the WhatsApp store (after Stop, which
// disconnected its accounts).
func (s served) Close(log *slog.Logger) {
	s.Service.Close()
	closeWhatsAppStore(log, s.whatsappStore)
}

// closeWhatsAppStore closes the WhatsApp store (nil: there was none), logging a failure.
func closeWhatsAppStore(log *slog.Logger, store *whatsapp.Store) {
	if store == nil {
		return
	}
	if err := store.Close(); err != nil {
		log.Warn("close the WhatsApp store failed", "err", err)
	}
}

// configure applies the config to the backend; internal/matrix and internal/local
// read no config themselves.
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
	if backend.matrix != nil {
		backend.matrix.KeepDeleted(cfg.Display.Deleted.Keep())
		backend.matrix.UseIdentities(context.Background(), identityGroups(cfg))
	}
	if backend.whatsapp != nil {
		backend.whatsapp.KeepDeleted(cfg.Display.Deleted.Keep())
	}
	if backend.slack != nil {
		backend.slack.KeepDeleted(cfg.Display.Deleted.Keep())
	}
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

// connectAndSync runs every network: each finishes its own connecting, then syncs.
// SyncFault drops errors caused by our own shutdown, so a clean stop exits 0.
func connectAndSync(ctx context.Context, router *route.Router) error {
	if err := daemon.SyncFault(ctx, router.Start(ctx)); err != nil {
		return fmt.Errorf("networks: %w", err)
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
	backend served,
	cfg config.Config,
	configPath string,
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
	// Before Serve answers anyone: a client polling Status must not see a daemon
	// that waits for nothing yet.
	w.state.Expect(expected(ctx, log, backend)...)
	wg.Go(func() { w.streams.Run(ctx, backend) })
	wg.Go(func() { w.notifications.Run(ctx, w.streams) })
	wg.Go(func() { w.refresher.Run(ctx) })
	if w.backups != nil {
		wg.Go(func() { w.backups.Run(ctx) })
	}
	wg.Go(func() {
		syncErr = connectAndSync(ctx, backend.Router)
		if syncErr != nil {
			// Clients see this as the daemon's state; the journal needs it at once,
			// not only when the process exits.
			log.Error("sync stopped", "err", syncErr)
		}
		w.state.Failed(syncErr)
	})

	scheduler, cutoff := startScheduler(ctx, log, cfg, backend, backend.schedulePath)
	defer scheduler.Stop()

	log.Info("serving", "socket", lock.Socket(), "version", buildinfo.String("kithd"))
	err = daemon.Serve(ctx, ln, &daemon.Daemon{
		Backend:       backend,
		Streams:       w.streams,
		State:         w.state,
		Notifications: w.notifications,
		Scheduler:     scheduler,
		WhatsApp:      backend.whatsAppLink(),
		Matrix:        backend.matrixLogin(),
		Slack:         backend.slackSignIn(),
		Telegram:      backend.telegramLogin(),
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
	backend served,
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
		backend.UseCompletionModel(modelsetup.CompletionModel(reloaded.Complete.Model, backend.dataDir))
		if backend.matrix != nil {
			backend.matrix.UseIdentities(ctx, identityGroups(reloaded))
		}
		backend.UsePlaces(setup.PlacesOf(reloaded))
		if backend.whatsapp != nil && reloaded.WhatsApp.Enabled {
			backend.whatsapp.UseAccounts(ctx, whatsAppAccounts(reloaded))
		}
		if backend.slack != nil && reloaded.Slack.Enabled {
			backend.slack.UseAccounts(slackAccounts(reloaded))
		}
		if backend.telegram != nil {
			backend.telegram.UseAccounts(telegramAccounts(reloaded))
		}
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
func newWorkers(log *slog.Logger, cfg config.Config, backend served) (*workers, error) {
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
	notifications.UseSelves(backend.Me)
	refresher := daemon.NewRefresher(backend, notifications.InvalidateScope)
	refresher.UseLogger(log)

	// Readiness is "what the daemon started with has synced", not "socket open": a
	// cold cache looks like no rooms (see serve's Expect).
	state := daemon.NewState()
	var backups *daemon.KeyBackup
	if m := backend.matrix; m != nil {
		m.OnRoomsChanged(refresher.Changed)
		// Logged in after the startup refresh: refresh again, now with Matrix.
		m.onLoggedIn = func() {
			refresher.Changed()
			// The startup sweep ran before the session took: back up what it missed.
			if backups != nil {
				backups.Soon()
			}
		}
		m.report = func(phase daemon.Phase, detail string) {
			state.Report(matrixStatus(m, phase, detail), time.Now())
		}
		m.OnSynced(func(t time.Time) {
			state.Report(matrixStatus(m, daemon.PhaseOnline, ""), t)
			notifications.Synced(t)
		})
		backups = daemon.NewKeyBackup(m, func(level slog.Level, line string) {
			log.Log(context.Background(), level, "key backup: "+line, "op", "key backup")
		})
	}
	if wa := backend.whatsapp; wa != nil {
		// Not the refresher: it refreshes every network, WhatsApp's own refresh included.
		wa.OnRoomsChanged(notifications.InvalidateScope)
		wa.OnLink(func(account whatsapp.Account, link whatsapp.Link, detail string) {
			state.Report(whatsAppStatus(account, link, detail), time.Now())
		})
	}
	if sl := backend.slack; sl != nil {
		sl.OnRoomsChanged(notifications.InvalidateScope)
		sl.OnSession(func(account slack.Account, s slack.Session, detail string) {
			state.Report(slackStatus(account, s, detail), time.Now())
		})
	}
	if tg := backend.telegram; tg != nil {
		tg.OnSession(func(account telegram.Account, s telegram.Session, detail string) {
			state.Report(telegramStatus(account, s, detail), time.Now())
		})
	}
	return &workers{
		notifications: notifications,
		refresher:     refresher,
		state:         state,
		streams:       daemon.NewStreams(),
		backups:       backups,
	}, nil
}

// matrixStatus is the Matrix account's row in the daemon's status.
func matrixStatus(m *matrixAdapter, phase daemon.Phase, detail string) daemon.NetworkStatus {
	return daemon.NetworkStatus{Network: string(domain.ProtocolMatrix), Account: m.account.user, Phase: phase, Detail: detail}
}

// whatsAppStatus is a WhatsApp account's row in the daemon's status.
func whatsAppStatus(account whatsapp.Account, link whatsapp.Link, detail string) daemon.NetworkStatus {
	phase := daemon.PhaseLoggedOut
	switch link {
	case whatsapp.Connecting:
		phase = daemon.PhaseConnecting
	case whatsapp.Connected:
		phase = daemon.PhaseOnline
	case whatsapp.Unlinked:
	}
	return daemon.NetworkStatus{Network: string(domain.ProtocolWhatsApp), Account: account.Name, Phase: phase, Detail: detail}
}

// expected is every account the daemon starts with a session, for State.Expect: the
// Matrix account when a session was saved, and each linked WhatsApp account. One that
// cannot be read is not waited for.
func expected(ctx context.Context, log *slog.Logger, backend served) []daemon.NetworkStatus {
	var out []daemon.NetworkStatus
	if m := backend.matrix; m != nil && m.saved.AccessToken != "" {
		out = append(out, matrixStatus(m, daemon.PhaseConnecting, ""))
	}
	if wa := backend.whatsapp; wa != nil {
		linked, err := wa.Linked(ctx)
		if err != nil {
			log.Warn("read which WhatsApp accounts are linked failed", "err", err)
		}
		for _, account := range linked {
			out = append(out, whatsAppStatus(account, whatsapp.Connecting, ""))
		}
	}
	if sl := backend.slack; sl != nil {
		signed, err := sl.SignedIn()
		if err != nil {
			log.Warn("read which Slack accounts are signed in failed", "err", err)
		}
		for _, account := range signed {
			out = append(out, slackStatus(account, slack.Connecting, ""))
		}
	}
	if tg := backend.telegram; tg != nil {
		in, err := tg.LoggedIn()
		if err != nil {
			log.Warn("read which Telegram accounts are logged in failed", "err", err)
		}
		for _, account := range in {
			out = append(out, telegramStatus(account, telegram.Connecting, ""))
		}
	}
	return out
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
	if err := setup.Validate(cfg); err != nil {
		return config.Config{}, "", err
	}
	return cfg, path, nil
}

// prepareInstance makes the instance's directories and reads the saved Matrix session
// (zero when there is none, or no Matrix). No session is no error: the daemon serves
// what it has, and `kith login` starts Matrix through it.
func prepareInstance(cfg config.Config, storage domain.Storage) (domain.Session, error) {
	if err := makeStorageDirs(storage); err != nil {
		return domain.Session{}, err
	}
	if !cfg.HasMatrix() {
		return domain.Session{}, nil
	}
	return savedSession(cfg, session.StoreFor(storage, cfg.User))
}

// makeStorageDirs creates the instance's directories, private to this user: the
// stores create their files, not the directories they live in. The runtime one is
// the lock's (daemon.Acquire).
func makeStorageDirs(storage domain.Storage) error {
	for _, dir := range []string{storage.DataDir, storage.StateDir, storage.CacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// openCache opens the instance's cache, or returns nil with a warning: without it the
// backend falls through to the network.
func openCache(ctx context.Context, log *slog.Logger, path, user string) (*db.Cache, error) {
	cache, err := db.Open(ctx, path)
	if err != nil {
		// Every network writes into it and every client reads from it: there is no
		// daemon without it. A unit retries every few seconds, so fixing the cause
		// (disk space, permissions) is enough.
		return nil, fmt.Errorf("open the cache %s (free disk space or fix its permissions): %w", path, err)
	}
	cache.UseLogger(log)
	cache.UseAccount(user)
	if asideErr := cache.AsideFailed(); asideErr != nil {
		log.Warn("the previous cache could not be kept", "err", asideErr)
	}
	return cache, nil
}

// startScheduler starts the send-later queue before Serve, so overdue messages go out
// without waiting for a client. The returned atomic holds the overdue cutoff so a
// reload can change it.
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
