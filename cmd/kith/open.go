package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/launch"
)

// openTimeout is shorter than readyTimeout: somebody clicked a link and is waiting.
const openTimeout = 20 * time.Second

// runOpen (`kith --open <uri>`, the desktop's matrix: handler) delivers a URI to a
// running client via the daemon, or starts a terminal running one. It never draws.
func runOpen(configPath, profile, uri string) error {
	if _, ok := domain.ParsePlace(uri); !ok {
		return fmt.Errorf("not a Matrix link: %s", uri)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	at, err := attach(ctx, configPath, profile, openTimeout)
	if err != nil || at.backend == nil {
		return err
	}
	cfg, backend := at.cfg, at.backend
	defer backend.Stop()

	delivered, err := backend.Follow(ctx, uri)
	if err != nil {
		return fmt.Errorf("hand over %s: %w", uri, err)
	}
	if delivered {
		return nil
	}
	return openInTerminal(ctx, cfg.Terminal, uri)
}

// openInTerminal starts a terminal running a client with --follow. Passing the URI on
// the command line avoids the race of broadcasting before the new client attaches.
func openInTerminal(ctx context.Context, configured, uri string) error {
	term, err := terminalFor(configured)
	if err != nil {
		return err
	}
	// Absolute path: the terminal may not inherit this process's PATH.
	self, err := os.Executable()
	if err != nil {
		self = "kith"
	}
	how, err := launch.Run(ctx, term, []string{self, "--follow", uri})
	if err != nil {
		return fmt.Errorf("open a terminal for %s: %w", uri, err)
	}
	fmt.Fprintf(os.Stderr, "kith: opened %s in a new %s\n", uri, how)
	return nil
}

// terminalFor resolves the configured terminal, or the first found. An unknown name
// is used as a plain `<name> -e` command.
func terminalFor(configured string) (launch.Terminal, error) {
	if configured == "" {
		term, found := launch.Detect()
		if !found {
			return launch.Terminal{}, fmt.Errorf("%w: set `terminal` in the config", launch.ErrNoTerminal)
		}
		return term, nil
	}
	if term, known := launch.Named(configured); known {
		return term, nil
	}
	if _, err := exec.LookPath(configured); err != nil {
		return launch.Terminal{}, fmt.Errorf("terminal %q: %w", configured, err)
	}
	return launch.Terminal{Name: configured, Window: []string{configured, "-e"}}, nil
}
