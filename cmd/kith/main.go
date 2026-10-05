// Command kith is a terminal UI Matrix client.
//
// It holds no Matrix session and runs no sync: the kithd daemon owns those and this
// process talks to it over a unix socket (see ARCHITECTURE.md). There is deliberately
// no in-process fallback — two processes sharing one device's olm/megolm state corrupt
// it. The one thing the daemon cannot do is prompt, so `kith login` runs here.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/buildinfo"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/tui"
)

// readyTimeout covers a login and a first /sync against a possibly slow homeserver.
const readyTimeout = 60 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "login" {
		exitOn(runLogin(os.Args[2:]))
		return
	}

	showVersion := flag.Bool("version", false, "print version information and exit")
	configPath := flag.String("config", "", "path to config file (default: XDG config dir)")
	profile := flag.String("profile", "", "which [[profile]] account to run as (default: the first one)")
	clearCache := flag.Bool("clear-cache", false, "ask the daemon to empty the local cache and re-fetch from the server")
	restoreKeys := flag.Bool("restore-keys", false, "restore room keys from the server backup (prompts for the recovery key/passphrase) before starting")
	bootstrapKeys := flag.Bool("bootstrap-keys", false, "set this account up for room-key backup (prompts for the account password), print the recovery key and exit")
	exportKeys := flag.String("export-keys", "", "write this device's room keys to `file`, encrypted with a passphrase, and exit")
	importKeys := flag.String("import-keys", "", "add the room keys in `file` (a Matrix key export) before starting")
	printConfig := flag.Bool("print-config", false, "print the fully-documented default configuration and exit")
	addDict := flag.String("add-dictionary", "", "install a spelling dictionary by `tag` (\"list\" to see them) and exit")
	addFreq := flag.String("add-frequencies", "", "install word counts for a language by dictionary `tag` (\"list\" to see them) and exit; what `[spell] flag_rare_words` reads")
	addModelTag := flag.String("add-model", "", "install the local completion model by `tag` (\"list\" to see them) and exit; what `[complete.model]` reads")
	setModelKeyRef := flag.String("set-model-key", "", "store the language model's API key in the OS keyring under `ref` (what `[assist] key_ref` names) and exit; an empty answer removes it")
	agentLog := flag.Bool("agent-log", false, "print what an assistant has written on your behalf through kith-mcp — sent, queued, left as a draft, or refused — and exit")
	open := flag.String("open", "", "hand a matrix: or matrix.to `uri` to the running client — starting a terminal for it if none is running — and exit. This is what the desktop's matrix: handler calls")
	logLevel, logTarget := flag.String("log-level", "", "how much to log ("+logging.Levels+"); overrides $"+logging.EnvLevel+" and `[log] level`"),
		flag.String("log-target", "", "where to log ("+logging.Targets+"): the journal, $XDG_STATE_HOME/kith/"+logging.FileName+", or the journal when there is one; overrides $"+logging.EnvTarget+" and `[log] target`")
	follow, force := flag.String("follow", "", "start the client and go to this matrix `uri` once it knows where it is. Used by --open when it has to start a terminal"),
		flag.Bool("force", false, "take over from an kith already open on this account: it saves its drafts and closes")
	flag.Parse()

	// Local, exit-immediately operations run before anything needs a session: a
	// machine with no dictionaries is exactly one still being set up.
	switch {
	case *showVersion:
		fmt.Println(buildinfo.String("kith"))
	case *printConfig:
		fmt.Print(config.Annotated())
	case *addDict != "":
		exitOn(addDictionary(sharedStorage(*configPath, *profile).DataDir, *addDict))
	case *addFreq != "":
		exitOn(addFrequencies(sharedStorage(*configPath, *profile).DataDir, *addFreq))
	case *addModelTag != "":
		exitOn(addModel(sharedStorage(*configPath, *profile).DataDir, *addModelTag))
	case *setModelKeyRef != "":
		exitOn(setModelKey(sharedStorage(*configPath, *profile).KeyringService, *setModelKeyRef))
	case *agentLog:
		exitOn(showAgentLog(*configPath, *profile))
	case *open != "":
		exitOn(runOpen(*configPath, *profile, *open))
	default:
		exitOn(run(*configPath, *profile, startup{
			follow:        *follow,
			force:         *force,
			logLevel:      *logLevel,
			logTarget:     *logTarget,
			clearCache:    *clearCache,
			restoreKeys:   *restoreKeys,
			bootstrapKeys: *bootstrapKeys,
			exportKeys:    *exportKeys,
			importKeys:    *importKeys,
		}))
	}
}

// startup is the one-off work flags ask for around the session.
type startup struct {
	follow        string // a matrix URI the TUI should go to first
	force         bool   // take the seat from the window that has it
	logLevel      string // --log-level; empty defers to $KITH_LOG_LEVEL and the config
	logTarget     string // --log-target; empty defers to $KITH_LOG_TARGET and the config
	clearCache    bool
	restoreKeys   bool
	bootstrapKeys bool
	// Client-side paths: the daemon runs under ProtectHome=read-only.
	exportKeys string
	importKeys string
}

// attached is a config's daemon, attached to: the config, where its files are, and
// the client.
type attached struct {
	path    string
	cfg     config.Config
	storage domain.Storage
	launch  daemon.Launch // how its daemon is started, and so restarted
	backend *daemon.Remote
	note    string
}

// attach loads the config and attaches to (or starts) the daemon, whatever is logged
// in: a network that is not says so in the daemon's status (see loggedOutNotice).
// backend is nil when a first-run default config was just written.
func attach(ctx context.Context, configPath, profile string, timeout time.Duration) (attached, error) {
	path, cfg, ready, err := loadConfig(configPath, profile)
	if err != nil || !ready {
		return attached{cfg: cfg}, err
	}
	storage, err := storageFor(cfg, path, profile)
	if err != nil {
		return attached{cfg: cfg}, err
	}
	return reach(ctx, path, cfg, storage, profile, timeout)
}

// storageFor is where the config keeps its files, with its instance recorded in the
// config when it names none (the daemon only ever reads it). A config that cannot be
// written (one a system manages) is left be: its instance is derived from its path.
func storageFor(cfg config.Config, path, profile string) (domain.Storage, error) {
	storage, err := setup.StorageFor(cfg, path, profile)
	if err != nil {
		return domain.Storage{}, err
	}
	if rerr := setup.RememberInstance(cfg, path); rerr != nil &&
		!errors.Is(rerr, fs.ErrPermission) && !errors.Is(rerr, syscall.EROFS) {
		fmt.Fprintln(os.Stderr, "kith:", rerr, "(the instance is derived from the file's path meanwhile)")
	}
	return storage, nil
}

// loggedOutNotice says which network accounts are not logged in, and how to log them
// in; "" when all are.
func loggedOutNotice(rows []daemon.NetworkStatus) string {
	var out []string
	for _, row := range rows {
		if row.Phase == daemon.PhaseLoggedOut {
			out = append(out, fmt.Sprintf("%s %s is logged out: %s", row.Network, row.Account, row.Detail))
		}
	}
	return strings.Join(out, " · ")
}

// reach attaches to (or starts) the daemon for a loaded config, logged in or not:
// what the login commands do, as the daemon is what logs a network in.
func reach(ctx context.Context, path string, cfg config.Config, storage domain.Storage, profile string, timeout time.Duration) (attached, error) {
	// The packaged units serve only the default config in the default directories;
	// any other gets a unit of its own, naming the config.
	launch := daemon.Launch{ConfigPath: path, Profile: profile, OwnConfig: !isDefaultConfig(path) || !inDefaultDirs(storage)}
	backend, note, err := daemon.Ensure(ctx, storage, launch, timeout)
	if err != nil {
		return attached{cfg: cfg}, fmt.Errorf("attach to kithd: %w", err)
	}
	if cfg.Display.Media.CacheDir == "" {
		cfg.Display.Media.CacheDir = storage.MediaDir() // [storage] cache_dir
	}
	return attached{path: path, cfg: cfg, storage: storage, launch: launch, backend: backend, note: note}, nil
}

// reachForLogin loads the config and reaches its daemon; backend is nil when a
// first-run default config was just written.
func reachForLogin(ctx context.Context, configPath, profile string) (attached, error) {
	path, cfg, ready, err := loadConfig(configPath, profile)
	if err != nil || !ready {
		return attached{cfg: cfg}, err
	}
	storage, err := storageFor(cfg, path, profile)
	if err != nil {
		return attached{cfg: cfg}, err
	}
	return reach(ctx, path, cfg, storage, profile, readyTimeout)
}

// isDefaultConfig reports whether path is the config file kith reads by default,
// which is the one the systemd units start kithd with.
func isDefaultConfig(path string) bool {
	def, err := config.Path()
	if err != nil {
		return false
	}
	a, aerr := filepath.Abs(path)
	b, berr := filepath.Abs(def)
	return aerr == nil && berr == nil && a == b
}

// inDefaultDirs reports whether storage keeps its files where the packaged units let
// the daemon write: the directories kith uses when [storage] names none.
func inDefaultDirs(storage domain.Storage) bool {
	def, err := setup.StorageDirs(config.Config{})
	return err == nil && storage.DataDir == def.DataDir && storage.StateDir == def.StateDir &&
		storage.CacheDir == def.CacheDir && storage.RuntimeDir == def.RuntimeDir
}

// sharedStorage is where the config's shared files go (dictionaries, models, API
// keys), read without writing anything; the defaults when there is no config.
func sharedStorage(configPath, profile string) domain.Storage {
	cfg := config.Config{}
	path := configPath
	if path == "" {
		path, _ = config.Path()
	}
	if loaded, err := config.Load(path); err == nil {
		if chosen, perr := loaded.Profile(profile); perr == nil {
			cfg = chosen
		}
	}
	storage, err := setup.StorageDirs(cfg)
	if err != nil {
		storage, _ = setup.StorageDirs(config.Config{})
	}
	return storage
}

// runKeyJobs does what the key flags ask before the interface opens, reporting whether
// the run ends with it. Bootstrap and export end the run: the TUI would scroll away a
// recovery key printed only once, and an export asks nothing of the session afterwards.
func runKeyJobs(ctx context.Context, backend *daemon.Remote, user string, jobs startup) (bool, error) {
	if jobs.bootstrapKeys {
		return true, bootstrapKeyBackup(ctx, backend, user)
	}
	if jobs.restoreKeys {
		if err := restoreKeyBackup(ctx, backend, user); err != nil {
			return true, err
		}
	}
	if jobs.exportKeys != "" {
		return true, exportRoomKeys(ctx, backend, user, jobs.exportKeys)
	}
	if jobs.importKeys != "" {
		if err := importRoomKeys(ctx, backend, jobs.importKeys); err != nil {
			return true, err
		}
	}
	return false, nil
}

func run(configPath, profile string, jobs startup) error {
	// SIGTERM and SIGHUP (the terminal closing) end the program like an interrupt, so
	// the drafts are written on the way out (tui.Run) and a playing voice note stops.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	at, err := attach(ctx, configPath, profile, readyTimeout)
	if err != nil || at.backend == nil {
		return err
	}
	path, cfg, backend := at.path, at.cfg, at.backend
	defer backend.Stop()
	if at.note != "" {
		fmt.Fprintln(os.Stderr, "kith:", at.note)
	}
	warnAboutAgentScope(ctx, backend, cfg)
	// One window per daemon (api.Seat), taken before anything the TUI would do. The
	// two runs that end with a printout need no window.
	if !jobs.bootstrapKeys && jobs.exportKeys == "" {
		if serr := backend.TakeSeat(ctx, jobs.force, whereAmI()); errors.Is(serr, api.ErrSeatTaken) {
			return fmt.Errorf("%w; close it, or run kith --force to take over", serr)
		} else if serr != nil {
			return fmt.Errorf("take the seat: %w", serr)
		}
	}

	if jobs.clearCache {
		if cerr := backend.ClearCache(ctx); cerr != nil {
			return fmt.Errorf("clear cache: %w", cerr)
		}
	}
	if done, kerr := runKeyJobs(ctx, backend, cfg.User, jobs); done || kerr != nil {
		return kerr
	}
	log, closeLog, err := openLog(jobs, cfg.Log, profile, at.storage)
	if err != nil {
		return err
	}
	defer closeLog()
	log.Info("kith started", "user", cfg.User, "version", buildinfo.String("kith"))
	notice := ""
	if rows, nerr := backend.Networks(ctx); nerr == nil {
		notice = loggedOutNotice(rows)
		if len(rows) == 0 {
			notice = "no account yet — :login sets one up"
		}
	} else {
		log.Warn("read which networks are logged in failed", "err", nerr)
	}
	if err := tui.Run(ctx, tui.RunOptions{
		Backend: backend, Notifications: backend, Schedules: backend,
		Config: cfg, ConfigPath: path, Me: cfg.User, Log: log, Follow: jobs.follow, Notice: notice,
		RestartDaemon: func(ctx context.Context) error {
			return daemon.Restart(ctx, at.storage, at.launch, readyTimeout)
		},
	}); errors.Is(err, tui.ErrOpenedElsewhere) {
		log.Info("kith closed: opened in another window")
		fmt.Fprintln(os.Stderr, "kith: opened in another window")
	} else if err != nil {
		log.Error("kith stopped", "err", err)
		return fmt.Errorf("run tui: %w", err)
	}
	return nil
}

// whereAmI is this window's whereabouts, for another refused the seat: the process,
// its terminal (Linux names it through /proc), the machine, and now.
func whereAmI() domain.SeatHolder {
	where := domain.SeatHolder{PID: os.Getpid(), Since: time.Now()}
	if tty, err := os.Readlink("/proc/self/fd/0"); err == nil && strings.HasPrefix(tty, "/dev/") {
		where.TTY = tty
	}
	where.Host, _ = os.Hostname()
	return where
}

// openLog opens the client's log: the journal (SYSLOG_IDENTIFIER kith, or
// kith-<profile>) or $XDG_STATE_HOME/kith/kith.log (1 MB, one old copy kept),
// as `[log] target` says. The terminal belongs to the TUI, so nothing may go to
// stderr while it runs. It also becomes slog's (and so the log package's) default,
// so a library that logs does not draw over the screen.
func openLog(jobs startup, configured config.Log, profile string, storage domain.Storage) (*slog.Logger, func(), error) {
	level, err := logging.Resolve(jobs.logLevel, configured.Level)
	if err != nil {
		return nil, nil, fmt.Errorf("log level: %w", err)
	}
	target, err := logging.ResolveTarget(jobs.logTarget, configured.Target)
	if err != nil {
		return nil, nil, fmt.Errorf("log target: %w", err)
	}
	log, closeLog := openLogTo(logging.Destination{
		Target:     target,
		Identifier: logging.Identifier("kith", profile),
		File:       filepath.Join(storage.StateDir, logging.FileName),
		Level:      level,
	})
	return log, closeLog, nil
}

// openLogTo opens d and makes it slog's default. A log that will not open is
// reported here, before the TUI starts, and the run goes on without one.
func openLogTo(d logging.Destination) (*slog.Logger, func()) {
	sink, err := logging.Open(d)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kith: no log:", err)
		log := logging.Discard()
		slog.SetDefault(log)
		return log, func() {}
	}
	slog.SetDefault(sink.Logger)
	return sink.Logger, func() {
		if cerr := sink.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, "kith: close log file:", cerr)
		}
	}
}

// runLogin logs the config's Matrix user in through the daemon, starting it if need
// be: the daemon saves the session in the OS keyring and starts Matrix on it. The
// password is never written to disk or argv.
func runLogin(args []string) error {
	if len(args) > 0 && args[0] == "whatsapp" {
		return runWhatsAppLogin(args[1:])
	}
	if len(args) > 0 && args[0] == "slack" {
		return runSlackLogin(args[1:])
	}
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	profile := fs.String("profile", "", "which [[profile]] account to log in (default: the first one)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse login flags: %w", err)
	}

	_, cfg, ready, err := loadConfig(*configPath, *profile)
	if err != nil || !ready {
		return err
	}
	if !cfg.HasMatrix() {
		return errors.New("set `homeserver` and `user` in the config to log in to Matrix " +
			"(for WhatsApp, run `kith login whatsapp`; for Slack, `kith login slack`)")
	}
	password, err := readSecret(fmt.Sprintf("Password for %s", cfg.User))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	at, err := reachForLogin(ctx, *configPath, *profile)
	if err != nil || at.backend == nil {
		return err
	}
	defer at.backend.Stop()
	in, err := at.backend.LoginMatrix(ctx, password)
	if errors.Is(err, api.ErrNetworkOff) {
		return errors.New("kithd was started before the config named a Matrix account; " +
			"restart it (`systemctl --user restart kithd`, or stop it and run kith) and log in again")
	}
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if in.Started {
		fmt.Printf("kith: logged in as %s (device %s); Matrix is syncing now.\n", in.UserID, in.DeviceID)
	} else {
		fmt.Printf("kith: logged in as %s (device %s). kithd was already running Matrix on an older "+
			"session, so it uses this one from its next start.\n", in.UserID, in.DeviceID)
	}
	return nil
}

// loadConfig resolves, loads, selects the profile and validates the config. On first
// run it writes a documented default and returns ready=false.
func loadConfig(configPath, profile string) (path string, cfg config.Config, ready bool, err error) {
	path = configPath
	if path == "" {
		if path, err = config.Path(); err != nil {
			return "", config.Config{}, false, fmt.Errorf("resolve config path: %w", err)
		}
	}
	if created, cerr := config.WriteDefaultIfMissing(path); cerr != nil {
		return "", config.Config{}, false, fmt.Errorf("write default config: %w", cerr)
	} else if created {
		fmt.Printf("kith: wrote a default config to %s; set an account up inside kith with :login.\n", path)
	}
	cfg, err = config.Load(path)
	if err != nil {
		return "", config.Config{}, false, fmt.Errorf("load config: %w", err)
	}
	if warning := config.ModeWarning(path); warning != "" {
		fmt.Fprintln(os.Stderr, "kith:", warning)
	}
	cfg, err = cfg.Profile(profile)
	if err != nil {
		return "", config.Config{}, false, fmt.Errorf("select profile: %w", err)
	}
	if err := setup.Validate(cfg); err != nil {
		return "", config.Config{}, false, fmt.Errorf("load config: %w", err)
	}
	return path, cfg, true, nil
}

// restoreKeyBackup imports the server-side room-key backup. The secret is read here
// (only a terminal can) and crosses the 0600 socket to the daemon (only it owns the
// crypto store).
func restoreKeyBackup(ctx context.Context, backend api.Keys, user string) error {
	secret, err := readSecret(fmt.Sprintf("Recovery key or passphrase for %s", user))
	if err != nil {
		return err
	}
	count, err := backend.RestoreKeyBackup(ctx, secret)
	switch {
	case errors.Is(err, api.ErrNoEncryption):
		return errors.New("encryption is not enabled for this device, so there is nowhere to import keys to")
	case errors.Is(err, api.ErrNoKeyBackup):
		return errors.New("this account has no room-key backup on the server to restore from")
	case errors.Is(err, api.ErrBadRecoveryKey):
		return errors.New("that recovery key or passphrase is not correct")
	case err != nil:
		return fmt.Errorf("restore keys: %w", err)
	}
	fmt.Fprintf(os.Stderr, "kith: restored %d room keys from backup; reopen rooms to see decrypted history\n", count)
	return nil
}

// bootstrapKeyBackup sets an account up for room-key backup and prints the recovery
// key. It refuses when setup already exists: a second run would mint a new
// cross-signing identity and orphan the first recovery key.
func bootstrapKeyBackup(ctx context.Context, backend api.Keys, user string) error {
	fmt.Fprintln(os.Stderr,
		"kith: this sets this account's encryption up for the first time: cross-signing,\n"+
			"secret storage and a room-key backup, with a recovery key printed once at the end.\n"+
			"If the account already has any of that, nothing is changed and this will say so.")
	password, err := readSecret(fmt.Sprintf("Account password for %s", user))
	if err != nil {
		return err
	}
	made, err := backend.BootstrapKeyBackup(ctx, password)
	switch {
	case errors.Is(err, api.ErrNoEncryption):
		return errors.New("encryption is not enabled for this device, so there is nothing to set up")
	case errors.Is(err, api.ErrKeyBackupExists):
		return errors.New("this account already has secret storage or a key backup; " +
			"use --restore-keys with the existing recovery key instead. " +
			"Setting up again would replace the account's cross-signing identity and its recovery key")
	case errors.Is(err, api.ErrBadPassword):
		return errors.New("that account password is not correct")
	case err != nil:
		return fmt.Errorf("bootstrap key backup: %w", err)
	}
	printRecoveryKey(made)
	return nil
}

// printRecoveryKey shows the only copy of the recovery key — first, before any
// partial-failure note, since the key cannot be recovered and the failure can be retried.
func printRecoveryKey(made domain.KeyBackup) {
	fmt.Fprintln(os.Stderr, "\nkith: your recovery key — write it down now. "+
		"It is shown once, stored nowhere, and is the only way back into this account's\n"+
		"encrypted history if this device is lost:")
	fmt.Fprintf(os.Stderr, "\n    %s\n\n", made.RecoveryKey)
	if made.Incomplete != "" {
		fmt.Fprintf(os.Stderr,
			"kith: cross-signing and secret storage are set up, but the key backup is not finished: %s\n"+
				"The recovery key above is still the one that unlocks this account.\n", made.Incomplete)
		return
	}
	fmt.Fprintf(os.Stderr, "kith: key backup version %s created; %d room keys uploaded. "+
		"The daemon backs up the rest as they arrive.\n", made.Version, made.Uploaded)
}

// readSecret reads one hidden line from an interactive terminal.
func readSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal; run kith interactively to enter secrets")
	}
	fmt.Fprintf(os.Stderr, "%s: ", prompt)
	raw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read secret: %w", err)
	}
	secret := strings.TrimRight(string(raw), "\r\n")
	if secret == "" {
		return "", errors.New("empty input")
	}
	return secret, nil
}

// exportRoomKeys writes this device's room keys, encrypted with a passphrase, to path.
// The daemon produces the ciphertext; this process writes it (the daemon cannot).
func exportRoomKeys(ctx context.Context, backend api.Keys, user, path string) error {
	passphrase, err := readNewSecret(fmt.Sprintf("Passphrase to encrypt %s's room keys", user))
	if err != nil {
		return err
	}
	data, err := backend.ExportRoomKeys(ctx, passphrase)
	switch {
	case errors.Is(err, api.ErrNoEncryption):
		return errors.New("encryption is not enabled for this device, so it holds no room keys")
	case errors.Is(err, api.ErrNoRoomKeys):
		return errors.New("this device holds no room keys yet; nothing to export")
	case err != nil:
		return fmt.Errorf("export keys: %w", err)
	}
	// O_EXCL: never overwrite an existing file.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the export path this user typed at their own shell
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close() // the write error is the report
		return fmt.Errorf("write %s: %w", path, werr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("write %s: %w", path, cerr)
	}
	fmt.Fprintf(os.Stderr, "kith: wrote %s (%d bytes, mode 0600). Keep it as safely as the passphrase.\n", path, len(data))
	return nil
}

// importRoomKeys adds the sessions in a Matrix key export to this device's store.
func importRoomKeys(ctx context.Context, backend api.Keys, path string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- the path is one this user typed at their own shell
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	passphrase, err := readSecret(fmt.Sprintf("Passphrase for %s", path))
	if err != nil {
		return err
	}
	imported, total, err := backend.ImportRoomKeys(ctx, passphrase, data)
	switch {
	case errors.Is(err, api.ErrNoEncryption):
		return errors.New("encryption is not enabled for this device, so there is nowhere to import keys to")
	case errors.Is(err, api.ErrBadKeyFile):
		return errors.New("could not read that export: wrong passphrase, or the file is not a Matrix key export")
	case err != nil:
		return fmt.Errorf("import keys: %w", err)
	}
	fmt.Fprintf(os.Stderr, "kith: imported %d of %d room keys; reopen rooms to see decrypted history\n", imported, total)
	return nil
}

// readNewSecret reads a passphrase twice and requires a match: a mistyped export
// passphrase is otherwise only discovered when the keys are needed.
func readNewSecret(prompt string) (string, error) {
	first, err := readSecret(prompt)
	if err != nil {
		return "", err
	}
	again, err := readSecret("Again, to be sure")
	if err != nil {
		return "", err
	}
	if first != again {
		return "", errors.New("the two passphrases do not match; nothing was written")
	}
	return first, nil
}

// exitOn prints err and exits non-zero; nil is a no-op.
func exitOn(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "kith:", err)
	os.Exit(1)
}
