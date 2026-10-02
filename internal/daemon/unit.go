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

// ownUnitName is the systemd user unit kith writes for a config other than the
// default one: one per instance, so a profile of that config has its own.
// The instance is restricted to [A-Za-z0-9._-] (setup.StorageFor), a valid unit name.
func ownUnitName(storage domain.Storage) string {
	return "kithd-" + storage.Instance + ".service"
}

// unitName is the unit that serves launch: the packaged kithd.service or
// kithd@<profile>.service for the default config, the generated one for any other.
func (l Launch) unitName(storage domain.Storage) string {
	if l.OwnConfig {
		return ownUnitName(storage)
	}
	return unitFor(l.Profile)
}

// errUnsafeUnitPath is a path that cannot be written into a unit as one word.
var errUnsafeUnitPath = errors.New("daemon: path cannot be written into a systemd unit")

// unitPath is p as one word of a unit file. A relative path, or one with whitespace,
// quotes, a backslash, "$" or a control character, is refused rather than quoted:
// each directive splits and unescapes words its own way. "%" (a specifier) is doubled.
// A path under /tmp or /var/tmp is refused too: PrivateTmp= hides it from the daemon.
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

// ownUnit is the unit for a config other than the default one: kithd started with
// that config (and profile), allowed to write only that config's directories, under
// the same sandbox as packaging/systemd/kithd.service (kept equal by a test).
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
	if launch.Profile != "" {
		start += " --profile " + launch.Profile
	}
	dirs := strings.Join([]string{words["data"], words["state"], words["cache"], words["runtime"]}, " ")

	return fmt.Sprintf(ownUnitTemplate, words["config"], start, dirs, dirs), nil
}

// ownUnitTemplate is packaging/systemd/kithd.service with the config's own
// ExecStart and directories. Its runtime directory is created like the others
// (RuntimeDirectory= only reaches below %t).
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

// writeUnit writes content to dir/name unless it is already there, reporting
// whether it changed (and systemd must reload). The file is replaced whole, never
// left half-written for a daemon-reload to read.
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

// installOwnUnit writes the config's unit to the systemd user unit directory and
// has systemd reread it when it changed, returning the unit's name.
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
	changed, err := writeUnit(filepath.Join(xdg.ConfigHome, "systemd", "user"), name, content)
	if err != nil {
		return "", err
	}
	if changed {
		if out, rerr := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").CombinedOutput(); rerr != nil {
			return "", fmt.Errorf("daemon: systemctl --user daemon-reload: %w (%s)", rerr, trimOutput(out))
		}
	}
	return name, nil
}
