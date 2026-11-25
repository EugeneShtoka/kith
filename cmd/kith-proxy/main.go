// Command kith-proxy is the link every client on a machine that joined a bridge
// elsewhere has to it: kith, kith-mcp, kith-notify and the desktop's relay dial its
// socket when it answers, and it forwards BackendService to the bridge with this
// device's key, so the bridge sees one client per machine whatever runs. It runs as a
// user service; where the device file names no bridge, or another kith-proxy holds the
// lock, it exits at once.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EugeneShtoka/kith/internal/buildinfo"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/keystore"
	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// readHeaderTimeout bounds a request's headers; bodies (streams) are unbounded.
const readHeaderTimeout = 30 * time.Second

func main() { os.Exit(start()) }

// start runs kith-proxy and returns its exit status, so deferred cleanup runs first.
func start() int {
	showVersion := flag.Bool("version", false, "print version information and exit")
	configPath := flag.String("config", "", "path to config file (default: XDG config dir)")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.String("kith-proxy"))
		return 0
	}
	log := logging.New(os.Stderr, logging.Options{})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, log, *configPath); err != nil {
		log.Error("kith-proxy stopped", "err", err)
		return 1
	}
	return 0
}

// run serves the machine's clients until ctx ends. It returns nil at once when there is
// nothing for it to do: no bridge joined, or another kith-proxy serving.
func run(ctx context.Context, log *slog.Logger, configPath string) error {
	path := configPath
	if path == "" {
		resolved, err := config.Path()
		if err != nil {
			return fmt.Errorf("finding the config: %w", err)
		}
		path = resolved
	}
	b, joined, err := setup.JoinedBridge(path)
	if err != nil {
		return err
	}
	if !joined {
		log.Info("this machine joined no bridge: kith-proxy has nothing to forward")
		return nil
	}
	storage, allowFile, err := setup.ClientStorage(path)
	if err != nil {
		return err
	}
	lock, held, err := daemon.AcquireProxy(storage)
	if err != nil {
		return fmt.Errorf("kith-proxy's lock: %w", err)
	}
	if !held {
		log.Info("another kith-proxy serves this machine")
		return nil
	}
	defer func() {
		if rerr := lock.Release(); rerr != nil {
			log.Warn("releasing kith-proxy's lock", "err", rerr)
		}
	}()
	return serve(ctx, log, path, b, storage, allowFile, lock)
}

// serve forwards to the bridge b on lock's socket until ctx ends, dialing it with this
// machine's device key (storage's).
func serve(ctx context.Context, log *slog.Logger, path string, b config.Bridge, storage domain.Storage, allowFile bool, lock *daemon.Lock) error {
	cert, err := keystore.ClientCertificate(storage, allowFile)
	if err != nil {
		return fmt.Errorf("dialing bridge %s: %w", b.Name, err)
	}
	// shortcut: a key the bridge rotates while this runs is followed at the next start,
	// as kith-notify's is; the bridge answers on the old one for 30 days meanwhile.
	if b, err = daemon.FollowRotation(ctx, cert, config.DevicePath(path), b); err != nil {
		return fmt.Errorf("following bridge %s's new key: %w", b.Name, err)
	}
	up, err := daemon.BridgeUpstream(cert, b)
	if err != nil {
		return fmt.Errorf("dialing bridge %s: %w", b.Name, err)
	}
	ln, err := lock.Listen(ctx)
	if err != nil {
		return fmt.Errorf("kith-proxy's socket: %w", err)
	}
	mux := http.NewServeMux()
	daemon.HandleBackend(mux, up.Forward(log))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	log.Info("forwarding to the bridge", "bridge", b.Name, "socket", lock.Socket())
	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	// Each client's streams end with it. A client chose the proxy as it started, so it
	// reopens them on this socket: answered again once the proxy is restarted, as a unit
	// restart or a deploy does, and unavailable until the client restarts if it never is.
	_ = srv.Close() // ignored: Serve's own error says the same
	<-served
	return nil
}
