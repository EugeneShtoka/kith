package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ownUnitName is the systemd user unit kith writes for a non-default config, one per
// instance. The instance is restricted to [A-Za-z0-9._-] (setup.StorageFor).
func ownUnitName(storage domain.Storage) string {
	return "kithd-" + storage.Instance + ".service"
}

// unitName is the unit serving launch: kithd.service for the default config, the
// generated one otherwise.
func (l Launch) unitName(storage domain.Storage) string {
	if l.OwnConfig {
		return ownUnitName(storage)
	}
	return defaultUnit
}

// errUnsafeUnitPath is a path that cannot be written into a unit as one word.
var errUnsafeUnitPath = errors.New("daemon: path cannot be written into a systemd unit")

// unitPath is p as one word of a unit file. Relative paths and ones with whitespace,
// quotes, backslash, "$" or control characters are refused, not quoted: each directive
// splits words its own way. "%" is doubled. /tmp and /var/tmp are refused: PrivateTmp=
// hides them from the daemon.
func unitPath(p string) (string, error) {
	if !filepath.IsAbs(p) || underTmp(p) || strings.ContainsFunc(p, func(r rune) bool {
		return r <= ' ' || r == 0x7f || strings.ContainsRune(`"'\$`, r)
	}) {
		return "", fmt.Errorf("%w: %q", errUnsafeUnitPath, p)
	}
	return strings.ReplaceAll(p, "%", "%%"), nil
}

// underTmp reports whether p is in a directory PrivateTmp= replaces.
func underTmp(p string) bool {
	p = filepath.Clean(p)
	for _, tmp := range []string{"/tmp", "/var/tmp"} {
		if p == tmp || strings.HasPrefix(p, tmp+"/") {
			return true
		}
	}
	return false
}

// ownUnit is the unit for a non-default config: kithd with that config,
// writing only its directories, under the same sandbox as
// packaging/systemd/kithd.service (a test keeps them equal).
func ownUnit(storage domain.Storage, kithd string, launch Launch) (string, error) {
	words := make(map[string]string, 6)
	for name, p := range map[string]string{
		"kithd": kithd, "config": launch.ConfigPath,
		"data": storage.DataDir, "state": storage.StateDir,
		"cache": storage.CacheDir, "runtime": storage.RuntimeDir,
	} {
		w, err := unitPath(p)
		if err != nil {
			return "", err
		}
		words[name] = w
	}
	start := words["kithd"] + " --config " + words["config"]
	// kithd writes the config file atomically, beside itself.
	configDir, err := unitPath(filepath.Dir(launch.ConfigPath))
	if err != nil {
		return "", err
	}
	writable := []string{words["data"], words["state"], words["cache"], words["runtime"], configDir}
	// Scheduled backups go to [backup] dir (else <export_dir>/backups), which may lie
	// outside the folders above.
	backups := storage.BackupsDir()
	if filepath.IsAbs(backups) && !under(backups, storage.DataDir, storage.StateDir, storage.CacheDir, storage.RuntimeDir) {
		w, err := unitPath(backups)
		if err != nil {
			return "", err
		}
		writable = append(writable, w)
	}
	dirs := strings.Join(writable, " ")

	return fmt.Sprintf(ownUnitTemplate, words["config"], start, dirs, dirs), nil
}

// under reports whether p is one of dirs or inside one.
func under(p string, dirs ...string) bool {
	p = filepath.Clean(p)
	for _, d := range dirs {
		d = filepath.Clean(d)
		if p == d || strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// ownUnitTemplate is packaging/systemd/kithd.service with the config's ExecStart and
// directories. RuntimeDirectory= only reaches below %t, so it is created like the others.
const ownUnitTemplate = `# Written by kith for its config %s, and rewritten when that config's
# directories change. Edits here are overwritten.

[Unit]
Description=kith daemon for its own config
Documentation=https://github.com/EugeneShtoka/kith
After=graphical-session.target
Wants=graphical-session.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=%s
ExecStartPre=+mkdir -p %s
Restart=on-failure
RestartSec=5s
PassEnvironment=WAYLAND_DISPLAY DISPLAY DBUS_SESSION_BUS_ADDRESS
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=%s
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictNamespaces=true
MemoryDenyWriteExecute=true
UMask=0077
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
SystemCallFilter=@system-service
SystemCallFilter=~@privileged
SystemCallErrorNumber=EPERM
SystemCallArchitectures=native
CapabilityBoundingSet=
LockPersonality=true
PrivateDevices=true
ProtectClock=true
ProtectHostname=true
ProtectKernelLogs=true
RestrictRealtime=true
KeyringMode=private
BindReadOnlyPaths=-/tmp/.X11-unix

[Install]
WantedBy=default.target
`

// writeUnit writes content to dir/name unless already there, reporting whether it
// changed (so systemd must reload). The file is replaced atomically.
func writeUnit(dir, name, content string) (changed bool, err error) {
	path := filepath.Join(dir, name)
	if old, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(old, []byte(content)) { // #nosec G304 -- dir is the systemd user unit directory, name is ownUnitName
		return false, nil
	}
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return false, fmt.Errorf("daemon: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return false, fmt.Errorf("daemon: write %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("daemon: write %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return false, fmt.Errorf("daemon: write %s: %w", path, err)
	}
	if err = os.Chmod(tmp.Name(), 0o644); err != nil { // #nosec G302 -- a unit file is not secret, and systemd reads it as any other
		return false, fmt.Errorf("daemon: write %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return false, fmt.Errorf("daemon: write %s: %w", path, err)
	}
	return true, nil
}

// installOwnUnit writes the config's unit and has systemd reload it when changed,
// returning the unit's name.
func installOwnUnit(ctx context.Context, storage domain.Storage, launch Launch) (string, error) {
	kithd, err := exec.LookPath(daemonBinary)
	if err != nil {
		return "", fmt.Errorf("daemon: %s is not on PATH: %w", daemonBinary, err)
	}
	if kithd, err = filepath.Abs(kithd); err != nil {
		return "", fmt.Errorf("daemon: %s: %w", daemonBinary, err)
	}
	if launch.ConfigPath, err = filepath.Abs(launch.ConfigPath); err != nil {
		return "", fmt.Errorf("daemon: config %s: %w", launch.ConfigPath, err)
	}
	content, err := ownUnit(storage, kithd, launch)
	if err != nil {
		return "", err
	}
	name := ownUnitName(storage)
	return name, installUnit(ctx, name, content)
}

// installUnit writes a user unit and has systemd reload it when changed.
func installUnit(ctx context.Context, name, content string) error {
	changed, err := writeUnit(filepath.Join(xdg.ConfigHome, "systemd", "user"), name, content)
	if err != nil {
		return err
	}
	if changed {
		if out, rerr := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").CombinedOutput(); rerr != nil {
			return fmt.Errorf("daemon: systemctl --user daemon-reload: %w (%s)", rerr, trimOutput(out))
		}
	}
	return nil
}
