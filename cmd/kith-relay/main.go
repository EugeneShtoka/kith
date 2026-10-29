// Command kith-relay is the desktop window's link to kithd. The window starts it as a
// child and reaches kithd only through it: it forwards BackendService to the local
// kithd's socket and answers DeviceService itself, on a socket of its own that only this
// user can open. It prints that socket's path once listening, and exits when its stdin
// closes, so it never outlives the window.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/EugeneShtoka/kith/internal/buildinfo"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// readHeaderTimeout bounds a request's headers; bodies (streams) are unbounded.
const readHeaderTimeout = 30 * time.Second

func main() { os.Exit(start()) }

// start runs kith-relay and returns its exit status, so deferred cleanup runs first.
func start() int {
	showVersion := flag.Bool("version", false, "print version information and exit")
	configPath := flag.String("config", "", "path to config file (default: XDG config dir)")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.String("kith-relay"))
		return 0
	}
	log := logging.New(os.Stderr, logging.Options{})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log, *configPath, os.Stdin, os.Stdout); err != nil {
		log.Error("kith-relay stopped", "err", err)
		return 1
	}
	return 0
}

// run serves the window until ctx ends or stdin closes, having printed the socket's
// path to stdout.
func run(ctx context.Context, log *slog.Logger, configPath string, stdin io.Reader, stdout io.Writer) error {
	storage, configPath, err := loadAccount(log, configPath)
	if err != nil {
		return err
	}
	socket, err := daemon.SocketPath(storage)
	if err != nil {
		return fmt.Errorf("finding the daemon's socket: %w", err)
	}
	ln, dir, err := listen(ctx, storage.RuntimeDir)
	if err != nil {
		return err
	}
	defer func() {
		if rerr := os.RemoveAll(dir); rerr != nil {
			log.Warn("removing the relay's socket", "err", rerr)
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Stdin closing is the window gone, crashed or not.
	go func() {
		_, _ = io.Copy(io.Discard, stdin) // its error ends it as EOF does
		cancel()
	}()
	srv := &http.Server{Handler: newHandler(socket, configPath, log), ReadHeaderTimeout: readHeaderTimeout}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	if _, err := fmt.Fprintln(stdout, ln.Addr().String()); err != nil {
		cancel()
		_ = srv.Close() // the write failure is the report
		<-served
		return fmt.Errorf("telling the window the socket: %w", err)
	}
	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	// The window is gone: no stream it held has a reader left to drain for.
	_ = srv.Close() // ignored: Serve's own error says the same
	<-served
	return nil
}

// listen opens the relay's socket in a directory of its own under the runtime dir,
// owner-only as kithd's is, so the relay of a second window never meets it.
func listen(ctx context.Context, runtimeDir string) (net.Listener, string, error) {
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return nil, "", fmt.Errorf("creating the runtime directory: %w", err)
	}
	dir, err := os.MkdirTemp(runtimeDir, "relay-")
	if err != nil {
		return nil, "", fmt.Errorf("creating the relay's directory: %w", err)
	}
	ln, err := daemon.Listen(ctx, filepath.Join(dir, "relay.sock"))
	if err != nil {
		_ = os.RemoveAll(dir) // the listen failure is the report
		return nil, "", fmt.Errorf("the relay's socket: %w", err)
	}
	return ln, dir, nil
}

// loadAccount reads the config for where kithd's socket is, as kith-notify does, after
// moving its device settings out (config.SplitDevice), and returns it with the config's
// path. Which profile the window runs in is DeviceService's, read on each call.
func loadAccount(log *slog.Logger, configPath string) (storage domain.Storage, configFile string, err error) {
	path := configPath
	if path == "" {
		resolved, perr := config.Path()
		if perr != nil {
			return domain.Storage{}, "", fmt.Errorf("finding the config: %w", perr)
		}
		path = resolved
	}
	if _, serr := config.SplitDevice(path, config.DevicePath(path)); serr != nil {
		return domain.Storage{}, "", fmt.Errorf("reading %s: %w", path, serr)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return domain.Storage{}, "", fmt.Errorf("reading %s: %w", path, err)
	}
	if warning := config.ModeWarning(path); warning != "" {
		log.Warn(warning)
	}
	if err = setup.Validate(cfg); err != nil {
		return domain.Storage{}, "", fmt.Errorf("reading %s: %w", path, err)
	}
	storage, err = setup.StorageFor(cfg, path)
	if err != nil {
		return domain.Storage{}, "", err
	}
	return storage, path, nil
}
