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

// RunOptions configures Run.
type RunOptions struct {
	Backend api.Backend
	// Notifications and Schedules are daemon-only services, not api.Backend roles.
	Notifications Notifications
	Schedules     Schedules
	Config        config.Config
	// ConfigPath is the bridge's config, whose changes kithd writes.
	ConfigPath string
	// Device is this device's settings, and DevicePath their file, where in-app changes
	// to them are written.
	Device     config.Device
	DevicePath string
	// Log is the client's log, a file since the terminal is the program's.
	Log *slog.Logger
	// Follow is a matrix URI to open at start, or "".
	Follow string
	// Notice is the status line's standing text at start ("" for none): what is logged out.
	Notice string
	// RestartDaemon restarts the daemon and returns once it answers (for :login enabling a network
	// the daemon lacks); nil when unavailable.
	RestartDaemon func(ctx context.Context) error
	// Secrets is the keyring commands' secrets are kept in; nil when there is none.
	Secrets Secrets
	// Profiles switches the [[profile]] the window runs in, starting in Profile ("" the
	// first); nil cannot switch. Presence is told what the window is doing; nil is not.
	Profiles Profiles
	Profile  string
	Presence Presence
}

// Run starts the Bubble Tea program bound to ctx and blocks until it exits.
func Run(ctx context.Context, opts RunOptions) error {
	backend, cfg, dev, log := opts.Backend, opts.Config, opts.Device, opts.Log
	// Commands end with the program, so an abandoned save stops retrying while flushAtExit runs.
	ctx, endCommands := context.WithCancel(ctx)
	defer endCommands()
	// Bubble Tea pushes the Kitty keyboard protocol; an incomplete teardown leaves the shell
	// unable to read arrow keys. Popping a clean stack is a no-op.
	defer restoreKeyboard()

	// A cache that will not open is reported, not fatal.
	cache, cacheErr := openMediaCache(dev.Display.Media, log)

	model := New(ctx, backend, cfg.Display, dev.Display).
		WithLogger(log).
		WithDevice(opts.DevicePath, dev).
		WithNotifications(opts.Notifications).
		WithSchedules(opts.Schedules).
		WithConfigFile(opts.ConfigPath, cfg).
		WithCache(cache).
		WithRestart(opts.RestartDaemon).
		WithSecrets(opts.Secrets).
		WithProfiles(opts.Profiles, opts.Profile).
		WithPresence(opts.Presence)
	if cacheErr != nil {
		model = model.sayErr("no media cache", cacheErr)
	}
	if opts.Follow != "" {
		model = model.WithFollow(opts.Follow)
	}
	if opts.Notice != "" {
		// Standing text: it stays until something else is going on.
		model = model.doing(opts.Notice)
	}
	// The rules only drive the status badge; the daemon decides and delivers. Installed regardless
	// of `enabled`, which also gives the model `me`.
	rules, err := setup.NotificationRules(cfg.Notifications)
	if err != nil {
		return err
	}
	model = model.WithRules(rules, dev.Notifications.Enabled)
	// The frame rate is the client's cost at rest; see config.DeviceDisplay.FPS.
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithFPS(dev.Display.FrameRate()))
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

// restoreKeyboard pops the Kitty keyboard-protocol entry Bubble Tea pushed, writing only when
// stdout is a terminal.
func restoreKeyboard() {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		_, _ = fmt.Fprint(os.Stdout, "\x1b[<u") // nothing to do if the terminal is gone
	}
}
