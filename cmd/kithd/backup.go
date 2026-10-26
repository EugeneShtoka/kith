package main

import (
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
	"github.com/EugeneShtoka/kith/internal/setup"
)

// subcommands are `kithd <name> …`, run in a terminal rather than as the daemon:
// `kith backup` and `kith restore` run these, since kithd owns the cache.
var subcommands = map[string]func([]string) error{
	"backup":  runBackup,
	"restore": runRestore,
}

// runBackup is `kith backup [--config path] [file]`: the cache, the config and the
// local records, in one passphrase-encrypted file. kithd may keep running.
func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default: XDG config dir)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kith backup [--config path] [file] — your history, config and local records in one\n"+
			"passphrase-encrypted file (default: a dated one in [storage] export_dir). Network sessions\n"+
			"are not in it: after a restore, log in again.")
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
	pass, err := readNewPassphrase()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintln(os.Stderr, "writing", out)
	made, err := backup.Create(ctx, backup.Source{ConfigPath: path, Config: cfg, Storage: storage}, out, pass)
	if err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	var total int64
	for _, e := range made.Entries {
		fmt.Printf("%10s  %s\n", size(e.Size), e.Name)
		total += e.Size
	}
	for _, why := range made.Left {
		fmt.Println("left out:", why)
	}
	fmt.Printf("wrote %s (%s)\n", made.Path, size(total))
	fmt.Println("Not in it: network sessions and keys (log in again after a restore), and what refills\n" +
		"itself: downloaded models and dictionaries, the picture cache.")
	return nil
}

// runRestore is `kith restore [--config path] [--replace] <file>`.
func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file to restore (default: XDG config dir)")
	replace := fs.Bool("replace", false, "set aside the config, cache and records already there (renamed *.before-restore-<time>), instead of refusing")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kith restore [--config path] [--replace] <file> — put a `kith backup` back: the config,\n"+
			"then the cache and records where it keeps them on this machine. Stop kithd first; log in again after.")
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
	got, err := backup.Restore(ctx, fs.Arg(0), path, *replace, func() (string, error) {
		return readPassphrase("Passphrase of the backup")
	})
	if errors.Is(err, backup.ErrDaemonRunning) {
		return fmt.Errorf("%w\n(systemctl --user stop kithd.service, or kithd-<instance>.service for a config of its own; or quit the kithd you started)", err)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", fs.Arg(0), err)
	}
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
	for _, why := range got.Left {
		fmt.Println("left:", why)
	}
	if len(got.Logins) > 0 {
		fmt.Println("A backup holds no sessions; log in to each account again:")
		flags := ""
		if *configPath != "" {
			flags = " --config " + *configPath
		}
		for _, l := range got.Logins {
			fmt.Printf("  kith login%s %s %s\n", flags, l.Network, l.Account)
		}
	}
	return nil
}

// readNewPassphrase asks for a backup's passphrase twice: a mistyped one would only
// show when the backup is needed.
func readNewPassphrase() (string, error) {
	pass, err := readPassphrase("Passphrase for the backup")
	if err != nil {
		return "", err
	}
	again, err := readPassphrase("Once more")
	if err != nil {
		return "", err
	}
	if again != pass {
		return "", errors.New("the passphrases differ")
	}
	return pass, nil
}

// readPassphrase asks for a non-empty passphrase at the terminal, without echo.
func readPassphrase(prompt string) (string, error) {
	fd := int(os.Stdin.Fd()) // #nosec G115 -- a file descriptor fits an int
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal; run it interactively to enter the passphrase")
	}
	fmt.Fprintf(os.Stderr, "%s: ", prompt)
	raw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read the passphrase: %w", err)
	}
	pass := strings.TrimRight(string(raw), "\r\n")
	if pass == "" {
		return "", errors.New("an empty passphrase")
	}
	return pass, nil
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
