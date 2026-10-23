package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// configReloadDebounce absorbs the burst most editors emit around a save (write +
// chmod, or atomic rename + create). Short enough that a `kith config` ... edit
// is still felt within a key press, long enough that the daemon reloads once
// per save, not three times.
const configReloadDebounce = 200 * time.Millisecond

// WatchConfig calls reload after any write to path, until ctx is done. Watches
// the file's parent directory, not the file itself, because the vim/helix/VS Code
// save-via-rename dance replaces the file and leaves an inode watcher pointed at
// nothing. Reload errors are logged, not propagated: a bad edit should not take
// kithd down, just leave the previous config in force.
func WatchConfig(ctx context.Context, path string, reload func(context.Context) error, log *slog.Logger) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("daemon: open the config watcher: %w", err)
	}
	dir, name := filepath.Split(filepath.Clean(path))
	if dir == "" {
		dir = "."
	}
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("daemon: watch the config directory %s: %w", dir, err)
	}
	go runConfigWatcher(ctx, watcher, name, reload, log)
	return nil
}

// runConfigWatcher owns the watcher and the debounce timer until ctx ends.
func runConfigWatcher(ctx context.Context, watcher *fsnotify.Watcher, filename string, reload func(context.Context) error, log *slog.Logger) {
	defer func() {
		if err := watcher.Close(); err != nil {
			log.Warn("close the config watcher", "err", err)
		}
	}()
	// A single reusable timer avoids goroutine churn: Reset handles the common
	// debounce case, Stop + drain the shutdown case.
	timer := time.NewTimer(configReloadDebounce)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if touchesConfig(ev, filename) {
				armed = armTimer(timer, armed)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			// A dropped event or permission flap is not a reason to stop watching;
			// log and keep going.
			if !errors.Is(err, context.Canceled) {
				log.Warn("config watcher reported an error", "err", err)
			}
		case <-timer.C:
			armed = false
			if err := reload(ctx); err != nil {
				log.Warn("reload the config after a hand edit", "err", err)
				continue
			}
			log.Info("reloaded the config after a hand edit")
		}
	}
}

// touchesConfig reports whether ev names the config file in a way that could change
// its contents (an attribute-only change does not).
func touchesConfig(ev fsnotify.Event, filename string) bool {
	if filepath.Base(ev.Name) != filename {
		return false
	}
	return ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0
}

// armTimer resets the debounce timer, draining its channel first if a prior
// event already armed it. Returns true: the timer is armed.
func armTimer(timer *time.Timer, armed bool) bool {
	if armed && !timer.Stop() {
		<-timer.C
	}
	timer.Reset(configReloadDebounce)
	return true
}
