package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/media"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// RunOptions is what the program runs with.
type RunOptions struct {
	Backend api.Backend
	// Notifications and Schedules are daemon-only services, not api.Backend roles.
	Notifications Notifications
	Schedules     Schedules
	Config        config.Config
	// ConfigPath is where in-app setting changes are written.
	ConfigPath string
	// Log is the client's log (a file: the terminal is the program's).
	Log *slog.Logger
	// Follow is a matrix URI to open at start, or "".
	Follow string
	// Notice is the status line's standing text at start ("" for none): what is logged out.
	Notice string
	// RestartDaemon restarts the daemon and returns once it answers again: :login
	// turning on a network the daemon was started without. nil when it cannot.
	RestartDaemon func(ctx context.Context) error
}

// Run starts the Bubble Tea program bound to ctx and blocks until it exits.
func Run(ctx context.Context, opts RunOptions) error {
	backend, cfg, log := opts.Backend, opts.Config, opts.Log
	// The program's commands end with it: a save abandoned at exit must not go on
	// retrying while the last write (flushAtExit) runs.
	ctx, endCommands := context.WithCancel(ctx)
	defer endCommands()
	// Bubble Tea pushes the Kitty keyboard protocol; an incomplete teardown leaves the
	// shell unable to read arrow keys. Popping a clean stack is a no-op.
	defer restoreKeyboard()

	// A cache that will not open is reported, not fatal.
	cache, cacheErr := openMediaCache(cfg.Display.Media, log)

	model := New(ctx, backend, cfg.Display).
		WithLogger(log).
		WithKeys(cfg.Keys).
		WithNotifications(opts.Notifications).
		WithSchedules(opts.Schedules).
		WithConfigFile(opts.ConfigPath, cfg).
		WithCache(cache).
		WithRestart(opts.RestartDaemon)
	if cacheErr != nil {
		model = model.sayErr("no media cache", cacheErr)
	}
	if opts.Follow != "" {
		model = model.WithFollow(opts.Follow)
	}
	if opts.Notice != "" {
		// Standing, not an event: it stays true until something else is going on.
		model = model.doing(opts.Notice)
	}
	// The rules only describe (the status badge); the daemon decides and delivers.
	// Installed whatever `enabled` says, which also gives the model `me`.
	rules, err := setup.NotificationRules(cfg.Notifications)
	if err != nil {
		return err
	}
	model = model.WithRules(rules, cfg.Notifications.Enabled)
	// The frame rate is the client's cost at rest; see config.Display.FPS.
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithFPS(cfg.Display.FrameRate()))
	final, err := program.Run()
	// A playing voice note is an external process that quitting does not cancel.
	last, ok := final.(Model)
	if ok {
		last.player.close()
		endCommands()
		flushAtExit(last, log)
	}
	if err != nil {
		return fmt.Errorf("tui: run program: %w", err)
	}
	if ok && last.link.seatLost {
		return ErrOpenedElsewhere
	}
	return nil
}

// openMediaCache opens the client's picture cache, trimming it to size.
func openMediaCache(cfg config.Media, log *slog.Logger) (*media.Cache, error) {
	if !cfg.Caching() && len(cfg.Rules) == 0 {
		// Off everywhere, including per-place rules.
		return nil, nil
	}
	dir := cfg.CacheDir
	if dir != "" {
		expanded, err := expandHome(dir)
		if err != nil {
			return nil, err
		}
		dir = expanded
	}
	cache, err := media.New(dir, int64(cfg.CacheMaxMB)<<20)
	if err != nil {
		return nil, fmt.Errorf("tui: open media cache: %w", err)
	}
	// Best effort: a cache that cannot be tidied is still a cache.
	if _, terr := cache.Trim(); terr != nil && log != nil {
		log.Warn("trim media cache failed", "op", "trim media cache", "err", terr)
	}
	return cache, nil
}

// restoreKeyboard pops one entry off the terminal's Kitty keyboard-protocol
// stack, undoing the enhancement Bubble Tea requests at startup. It writes only
// when stdout is a real terminal, so redirected output stays clean.
func restoreKeyboard() {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		_, _ = fmt.Fprint(os.Stdout, "\x1b[<u") // nothing to do if the terminal is gone
	}
}
