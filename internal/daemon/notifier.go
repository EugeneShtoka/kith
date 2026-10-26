package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// notifierBinary shows kithd's notifications on this machine (cmd/kith-notify).
const notifierBinary = "kith-notify"

// notifierUnit is the packaged systemd user unit running it for the default config.
const notifierUnit = "kith-notify.service"

// ownNotifierUnitName is the unit kith writes for a non-default config's kith-notify.
func ownNotifierUnitName(storage domain.Storage) string {
	return "kith-notify-" + storage.Instance + ".service"
}

// NotifierLogPath is where a kith-notify started without systemd logs when there is no
// journal.
func NotifierLogPath(storage domain.Storage) string { return storage.LogPath(notifierBinary) }

// StartNotifier asks for kith-notify to run for the instance: via systemd first, a
// detached exec as fallback, never as a child of the client. kith-notify holds a
// single-instance lock, so asking while one runs shows nothing twice.
func StartNotifier(ctx context.Context, storage domain.Storage, launch Launch) error {
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spawnTimeout)
	defer cancel()
	args := []string{LogFlag, NotifierLogPath(storage)}
	if launch.OwnConfig && launch.ConfigPath != "" {
		args = append(args, "--config", launch.ConfigPath)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return detach(notifierBinary, args)
	}
	unit, err := startNotifierUnit(startCtx, storage, launch)
	if err == nil {
		return nil
	}
	// The unit may be missing (an install without it) or systemd may have no user
	// session: fall back, keeping systemd's reason.
	if derr := detach(notifierBinary, args); derr != nil {
		return fmt.Errorf("daemon: start %s: %w; and %w", unit, err, derr)
	}
	return nil
}

// startNotifierUnit starts launch's kith-notify unit, writing it first when the config
// is its own.
func startNotifierUnit(ctx context.Context, storage domain.Storage, launch Launch) (string, error) {
	unit := notifierUnit
	if launch.OwnConfig {
		unit = ownNotifierUnitName(storage)
		if err := installOwnNotifierUnit(ctx, unit, launch); err != nil {
			return unit, err
		}
	}
	// #nosec G204 -- unit is notifierUnit or ownNotifierUnitName(instance), restricted
	// to [A-Za-z0-9._-]; no shell, no separators.
	if out, err := exec.CommandContext(ctx, "systemctl", "--user", "start", unit).CombinedOutput(); err != nil {
		return unit, fmt.Errorf("%w (%s)", err, trimOutput(out))
	}
	return unit, nil
}

// installOwnNotifierUnit writes the unit running kith-notify with launch's config.
func installOwnNotifierUnit(ctx context.Context, unit string, launch Launch) error {
	binary, err := exec.LookPath(notifierBinary)
	if err != nil {
		return fmt.Errorf("daemon: %s is not on PATH: %w", notifierBinary, err)
	}
	if binary, err = filepath.Abs(binary); err != nil {
		return fmt.Errorf("daemon: %s: %w", notifierBinary, err)
	}
	config, err := filepath.Abs(launch.ConfigPath)
	if err != nil {
		return fmt.Errorf("daemon: config %s: %w", launch.ConfigPath, err)
	}
	content, err := ownNotifierUnit(binary, config)
	if err != nil {
		return err
	}
	return installUnit(ctx, unit, content)
}

// ownNotifierUnit is packaging/systemd/kith-notify.service running binary with config
// (a test keeps them equal).
func ownNotifierUnit(binary, config string) (string, error) {
	words := make([]string, 0, 2)
	for _, p := range []string{binary, config} {
		w, err := unitPath(p)
		if err != nil {
			return "", err
		}
		words = append(words, w)
	}
	return fmt.Sprintf(ownNotifierUnitTemplate, words[1], words[0]+" --config "+words[1]), nil
}

const ownNotifierUnitTemplate = `# Written by kith for its config %s, and rewritten when that config's
# kith-notify moves. Edits here are overwritten.

[Unit]
Description=kith notifications on this desktop, for its own config
Documentation=https://github.com/EugeneShtoka/kith
After=graphical-session.target
Wants=graphical-session.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5s
PassEnvironment=WAYLAND_DISPLAY DISPLAY DBUS_SESSION_BUS_ADDRESS
NoNewPrivileges=true

[Install]
WantedBy=default.target
`
