package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// defaultUnit is the packaged systemd user unit serving the default config.
const defaultUnit = "kithd.service"

// daemonBinary is the executable looked up on PATH when systemd is not available.
const daemonBinary = "kithd"

// readyPoll is how often a starting daemon is asked whether it is ready.
const readyPoll = 100 * time.Millisecond

// spawnTimeout bounds asking for the daemon to start, not its startup.
const spawnTimeout = 10 * time.Second

// Attach returns a client attached to the daemon at socket once it reports ready
// (not merely listening: that precedes the first sync). It never starts a daemon.
func Attach(ctx context.Context, socket string, timeout time.Duration) (*Remote, error) {
	r := NewRemote(socket)
	if err := waitReady(ctx, r, timeout); err != nil {
		return nil, err
	}
	return r, nil
}

// Launch is how a daemon for a config is started: the config file and the profile
// it serves. OwnConfig is a config the packaged units do not serve (another file, or
// the default one keeping its files elsewhere): its daemon gets a unit of its own,
// or is started directly, with --config.
type Launch struct {
	ConfigPath string
	Profile    string
	OwnConfig  bool
}

// Ensure returns a client attached to the instance's daemon, starting one if none is
// running. note is non-empty when one had to be started and says what that cost
// (nothing was notified while it was down).
func Ensure(ctx context.Context, storage domain.Storage, launch Launch, timeout time.Duration) (r *Remote, note string, err error) {
	socket, err := SocketPath(storage)
	if err != nil {
		return nil, "", err
	}

	// A daemon that is listening but not yet ready was started by someone else:
	// wait for it without claiming to have started it.
	if _, _, serr := statusProbe(ctx, NewRemote(socket)); serr == nil {
		attached, aerr := Attach(ctx, socket, timeout)
		return attached, "", aerr
	}

	if serr := spawn(ctx, storage, launch); serr != nil {
		return nil, "", serr
	}
	attached, err := Attach(ctx, socket, timeout)
	if err != nil {
		return nil, "", err
	}
	return attached, startedNote(ctx, launch.unitName(storage)), nil
}

// spawn starts the daemon so that it outlives this process: through systemd first
// (the packaged unit for the default config, a unit written for any other), a
// detached exec as fallback, never a child of the client.
func spawn(ctx context.Context, storage domain.Storage, launch Launch) error {
	// Not canceled with the client (a half-issued start should finish), but bounded.
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spawnTimeout)
	defer cancel()

	if _, err := exec.LookPath("systemctl"); err != nil {
		return detach(detachArgs(storage, launch))
	}
	unit, err := systemdStart(startCtx, storage, launch)
	if err == nil {
		return nil
	}
	// The unit may not be installed, or systemd may not run a user session: fall
	// back, keeping systemd's reason.
	if derr := detach(detachArgs(storage, launch)); derr != nil {
		return fmt.Errorf("daemon: start %s: %w; and %w", unit, err, derr)
	}
	return nil
}

// systemdStart starts launch's unit, writing it first when it is the config's own.
func systemdStart(ctx context.Context, storage domain.Storage, launch Launch) (unit string, err error) {
	unit = launch.unitName(storage)
	if launch.OwnConfig {
		if unit, err = installOwnUnit(ctx, storage, launch); err != nil {
			return ownUnitName(storage), err
		}
	}
	// #nosec G204 -- unit is unitFor(profile) or ownUnitName(instance), each
	// restricted to [A-Za-z0-9._-] — no shell, no separators.
	if out, err := exec.CommandContext(ctx, "systemctl", "--user", "start", unit).CombinedOutput(); err != nil {
		return unit, fmt.Errorf("%w (%s)", err, trimOutput(out))
	}
	return unit, nil
}

// LogFlag is the kithd flag naming a log file instead of stderr.
const LogFlag = "--log-file"

// DaemonLogPath is where a daemon started without systemd logs when there is no
// journal to log to (or `[log] target` is "file"): kithd.log in the state directory,
// or kithd-<profile>.log for a profile. A systemd unit logs to the journal.
func DaemonLogPath(storage domain.Storage, profile string) string {
	if profile != "" {
		return storage.LogPath("kithd-" + profile)
	}
	return storage.LogPath("kithd")
}

// detachArgs is the daemon's argv (after the binary) when detached: its log goes
// to DaemonLogPath, never to the terminal of the client that started it, and a
// config other than the default one is named.
func detachArgs(storage domain.Storage, launch Launch) []string {
	args := []string{LogFlag, DaemonLogPath(storage, launch.Profile)}
	if launch.OwnConfig && launch.ConfigPath != "" {
		args = append(args, "--config", launch.ConfigPath)
	}
	if launch.Profile != "" {
		args = append(args, "--profile", launch.Profile)
	}
	return args
}

// unitFor is the systemd unit serving a profile: the template instance
// kithd@<profile> (profile arrives as %i), or the plain unit.
func unitFor(profile string) string {
	if profile == "" {
		return defaultUnit
	}
	return "kithd@" + profile + ".service"
}

// statusProbe asks the daemon whether it is ready, bounded: Remote has no client
// timeout (streams stay open), so a wedged daemon would otherwise hang the caller.
func statusProbe(ctx context.Context, r *Remote) (ready bool, lastErr string, err error) {
	probe, cancel := context.WithTimeout(ctx, reattachProbeTimeout)
	defer cancel()
	ready, _, lastErr, err = r.Status(probe)
	return ready, lastErr, err
}

// waitReady polls Status until the daemon is ready, ctx ends, or timeout expires.
func waitReady(ctx context.Context, r *Remote, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr string
	for {
		ready, daemonErr, err := statusProbe(ctx, r)
		switch {
		case err == nil && ready:
			return nil
		case err == nil && daemonErr != "":
			// Keep waiting: a transient sync failure may resolve.
			lastErr = daemonErr
		}
		if ctx.Err() != nil {
			return fmt.Errorf("daemon: waiting for %s: %w", daemonBinary, ctx.Err())
		}
		if time.Now().After(deadline) {
			if lastErr != "" {
				return fmt.Errorf("daemon: %s did not become ready in %s: %s", daemonBinary, timeout, lastErr)
			}
			return fmt.Errorf("daemon: %s did not become ready in %s", daemonBinary, timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon: waiting for %s: %w", daemonBinary, ctx.Err())
		case <-time.After(readyPoll):
		}
	}
}

// startedNote is the warning shown after auto-spawning, with the autostart hint
// for as long as unit is not enabled.
func startedNote(ctx context.Context, unit string) string {
	note := "kithd was not running — started it. " +
		"Anything that arrived while it was down was not notified."
	if !unitEnabled(ctx, unit) {
		note += "\n" + autostartHint(unit)
	}
	return note
}

// unitEnabled reports whether the systemd user unit is enabled; any failure counts
// as not enabled.
func unitEnabled(ctx context.Context, unit string) bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "is-enabled", unit).Output() // #nosec G204 -- see systemdStart
	return err == nil && trimOutput(out) == "enabled"
}

// trimOutput reduces command output to one trimmed line.
func trimOutput(out []byte) string {
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}
