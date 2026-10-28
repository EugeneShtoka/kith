package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/EugeneShtoka/kith/internal/backup"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// subcommands are `kithd <name> …`, run in a terminal rather than as the daemon:
// `kith backup` and `kith restore` run these, since kithd owns the cache.
var subcommands = map[string]func([]string) error{
	"backup":  runBackup,
	"restore": runRestore,
}

// runBackup is `kith backup [--config path] [--no-media] [--new-key] [file]`: the cache,
// the config, the local records and the pictures, in one file encrypted to the backup
// key, which the first backup (or --new-key) makes and shows. kithd may keep running.
func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	noMedia := fs.Bool("no-media", false, "leave the picture cache out")
	renew := fs.Bool("new-key", false, "make a new backup key (when the one you have is lost), show it once, and back up to it")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kith backup [--config path] [--no-media] [--new-key] [file] — your history, config,\n"+
			"local records and pictures in one file encrypted to your backup key (default: a dated one\n"+
			"in [storage] export_dir). The first backup makes the key and shows it once; --new-key\n"+
			"replaces a lost one. Network sessions are not in it: after a restore, log in again.")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args) // ExitOnError
	if fs.NArg() > 1 {
		fs.Usage()
		return errors.New("one file at most")
	}
	cfg, path, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	storage, err := setup.StorageFor(cfg, path)
	if err != nil {
		return err
	}
	out := fs.Arg(0)
	if out == "" {
		out = filepath.Join(storage.ExportDir, "kith-backup-"+time.Now().Format("2006-01-02-1504")+".tar.age")
	}
	to, err := backupKey(cfg, storage, *renew)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(os.Stderr, "writing", out)
	key, err := cacheKey(storage)
	if err != nil {
		return fmt.Errorf("the cache's key (unlock the keyring): %w", err)
	}
	src := backup.Source{ConfigPath: path, Config: cfg, Storage: storage, CacheKey: key}
	if !*noMedia {
		src.MediaDir = storage.MediaDir()
	}
	made, err := backup.Create(ctx, src, out, to)
	if err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	printMade(made, *noMedia, to)
	return nil
}

// printMade says what a backup holds and left out, and where it went.
func printMade(made backup.Made, noMedia bool, to *backup.PublicKey) {
	var total int64
	for _, e := range made.Entries {
		name := e.Name
		if e.Count > 0 {
			name = fmt.Sprintf("%s (%d pictures)", e.Name, e.Count)
		}
		fmt.Printf("%10s  %s\n", size(e.Size), name)
		total += e.Size
	}
	for _, why := range made.Left {
		fmt.Println("left out:", why)
	}
	if noMedia {
		fmt.Println("left out: the pictures (--no-media)")
	}
	fmt.Printf("wrote %s (%s), encrypted to your backup key %s\n", made.Path, size(total), to)
	fmt.Println("Not in it: network sessions and keys (log in again after a restore), and what refills\n" +
		"itself: downloaded models and dictionaries.")
}

// errKeyKept is a --new-key the user did not confirm: the backup key stays as it was.
var errKeyKept = errors.New("kept the backup key you have; nothing changed and nothing was backed up")

// errKeyFromConfig is a --new-key while [backup] key names the key: kith makes no key
// over one the user chose.
var errKeyFromConfig = errors.New("`--new-key` makes nothing: your backup key is [backup] key in the config. " +
	"To change it, put another public key (age1…) there; `age-keygen` makes a pair, " +
	"`age-keygen -y` prints a private key's public half")

// backupKey is the backup key for cfg at storage: [backup] key when set, never made or
// replaced here. Otherwise, when there is none yet, or renew asks for a new one, it
// makes one, shows its private half and waits until it is kept before keeping the
// public half (a key whose private half nobody saw would lock backups no one can open).
func backupKey(cfg config.Config, storage domain.Storage, renew bool) (*backup.PublicKey, error) {
	to, fromConfig, err := backup.KeyFor(cfg, storage)
	if fromConfig {
		if renew {
			return nil, errKeyFromConfig
		}
		return to, err //nolint:wrapcheck // backup words it
	}
	had := !errors.Is(err, backup.ErrNoKey)
	if had && !renew {
		return to, err //nolint:wrapcheck // backup words it
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) { // #nosec G115 -- a file descriptor fits an int
		if renew {
			return nil, errors.New("`kith backup --new-key` shows the new key once: run it in a terminal")
		}
		return nil, errors.New("no backup key yet: run `kith backup` in a terminal once; it makes the key and shows it to you")
	}
	return makeKey(storage, had, bufio.NewReader(os.Stdin))
}

// makeKey makes storage's backup key from what the user answers on in. Replacing one
// (had) is confirmed first; the new public half replaces the old only once the private
// half is kept, so an interrupted run leaves the old key in place.
func makeKey(storage domain.Storage, had bool, in *bufio.Reader) (*backup.PublicKey, error) {
	if had {
		fmt.Fprint(os.Stderr, replaceKeyText(storage))
		line, err := in.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read the answer: %w", err)
		}
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			return nil, errKeyKept
		}
	}
	id, err := backup.NewKey()
	if err != nil {
		return nil, err //nolint:wrapcheck // backup words it
	}
	fmt.Fprint(os.Stderr, newKeyText(id))
	if err := keepKeyFile(id, in); err != nil {
		return nil, err
	}
	if err := backup.SaveKey(storage, id.Recipient()); err != nil {
		return nil, err //nolint:wrapcheck // backup words it
	}
	return id.Recipient(), nil
}

// replaceKeyText warns what a new backup key leaves behind, and asks.
func replaceKeyText(storage domain.Storage) string {
	return "This replaces your backup key. Backups made so far stay encrypted to the old key: only\n" +
		"the old key opens them, and kith cannot re-encrypt them. kithd's scheduled backups switch to\n" +
		"the new key from the next one; the older ones in " + storage.BackupsDir() + "\n" +
		"still need the old key.\n" +
		"Make a new backup key? [y/N]: "
}

// newKeyText shows a new backup key's private half, the one time it is shown.
func newKeyText(id *backup.Key) string {
	return "This is your backup key. It is the only way to open your kith backups, here or on a new\n" +
		"machine, and kith keeps no copy of it. Keep it somewhere other than this machine: a\n" +
		"password manager, or written down.\n\n" +
		"    " + id.String() + "\n\n" +
		"Every backup from now on, by hand or kithd's scheduled one, is encrypted to it. If it is\n" +
		"ever lost, `kith backup --new-key` makes a new one; backups made before then still\n" +
		"need this one.\n"
}

// keepKeyFile offers to save the key to a file, until one is saved or the user says it
// is kept (an empty answer).
func keepKeyFile(id *backup.Key, in *bufio.Reader) error {
	for {
		fmt.Fprint(os.Stderr, "Save it to a file too? Type a path, or press Enter once you have kept it: ")
		line, err := in.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read the answer: %w", err)
		}
		where := strings.TrimSpace(line)
		if where == "" {
			return nil
		}
		if strings.HasPrefix(where, "~/") {
			if home, herr := os.UserHomeDir(); herr == nil {
				where = filepath.Join(home, where[2:])
			}
		}
		body := "# kith backup key, made " + time.Now().Format("2006-01-02") + "\n# public key: " +
			id.Recipient().String() + "\n" + id.String() + "\n"
		f, err := os.OpenFile(filepath.Clean(where), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, err = f.WriteString(body)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not save it there:", err)
			continue
		}
		fmt.Fprintf(os.Stderr, "Saved to %s. Keep a copy somewhere other than this machine as well.\n", where)
		return nil
	}
}

// runRestore is `kith restore [--config path] [--replace] [--key-file path] <file>`.
func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file to restore (default: XDG config dir)")
	replace := fs.Bool("replace", false, "set aside the config, cache and records already there (renamed *.before-restore-<time>), instead of refusing")
	keyFile := fs.String("key-file", "", "read the backup key from this file instead of asking for it")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kith restore [--config path] [--replace] [--key-file path] <file> — put a `kith backup`\n"+
			"back: the config, then the cache, records and pictures where it keeps them on this machine. It\n"+
			"asks for your backup key (AGE-SECRET-KEY-1…). Stop kithd first; log in again after.")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args) // ExitOnError
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("which backup?")
	}
	path := *configPath
	if path == "" {
		resolved, err := config.Path()
		if err != nil {
			return fmt.Errorf("resolve config path: %w", err)
		}
		path = resolved
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	got, err := backup.Restore(ctx, fs.Arg(0), path, *replace, func() (*backup.Key, error) {
		return readKey(*keyFile)
	}, cacheKey)
	if errors.Is(err, backup.ErrDaemonRunning) {
		return fmt.Errorf("%w\n(systemctl --user stop kithd.service, or kithd-<instance>.service for a config of its own; or quit the kithd you started)", err)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", fs.Arg(0), err)
	}
	printRestored(got, *configPath)
	return nil
}

// printRestored says where a restore put each file, what it set aside or left, and
// which accounts to log in to again (with --config path when one was given).
func printRestored(got backup.Restored, configPath string) {
	fmt.Println("restored:")
	for _, f := range got.Files {
		fmt.Println("  " + f)
	}
	if len(got.Aside) > 0 {
		fmt.Println("set aside (remove once the restored history looks right):")
		for _, f := range got.Aside {
			fmt.Println("  " + f)
		}
	}
	if got.Pictures+got.PicturesThere > 0 {
		fmt.Printf("pictures: %d put back in %s (%d there already)\n", got.Pictures, got.PicturesFolder, got.PicturesThere)
	}
	for _, why := range got.Left {
		fmt.Println("left:", why)
	}
	fmt.Println("Backups here go on to the same backup key.")
	if len(got.Logins) > 0 {
		fmt.Println("A backup holds no sessions; log in to each account again:")
		flags := ""
		if configPath != "" {
			flags = " --config " + configPath
		}
		for _, l := range got.Logins {
			fmt.Printf("  kith login%s %s %s\n", flags, l.Network, l.Account)
		}
	}
}

// readKey is the private backup key: from file when one is named, else asked at the
// terminal without echo.
func readKey(file string) (*backup.Key, error) {
	if file != "" {
		body, err := os.ReadFile(file) // #nosec G304 -- the key file this user named
		if err != nil {
			return nil, fmt.Errorf("the backup key: %w", err)
		}
		return backup.ParseKey(string(body)) //nolint:wrapcheck // backup words it
	}
	fd := int(os.Stdin.Fd()) // #nosec G115 -- a file descriptor fits an int
	if !term.IsTerminal(fd) {
		return nil, errors.New("stdin is not a terminal; run it interactively to enter the backup key, or give --key-file")
	}
	fmt.Fprint(os.Stderr, "Backup key (AGE-SECRET-KEY-1…): ")
	raw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("read the backup key: %w", err)
	}
	return backup.ParseKey(string(raw)) //nolint:wrapcheck // backup words it
}

// size is n bytes for a person: "471.2 MB".
func size(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, exp := float64(n)/unit, 0
	for value >= unit && exp < 3 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", value, "kMGT"[exp])
}
