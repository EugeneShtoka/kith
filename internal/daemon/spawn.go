package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/human"
)

// defaultUnit is the packaged systemd user unit serving the default config.
const defaultUnit = "kithd.service"

// daemonBinary is looked up on PATH when systemd is not available.
const daemonBinary = "kithd"

const readyPoll = 100 * time.Millisecond

// spawnTimeout bounds asking for the daemon to start, not its startup.
const spawnTimeout = 10 * time.Second

// Attach returns a client once the daemon at socket reports ready (listening precedes
// the first sync). It never starts a daemon.
func Attach(ctx context.Context, socket string, timeout time.Duration) (*Remote, error) {
	r := NewRemote(socket)
	if err := waitReady(ctx, r, timeout); err != nil {
		return nil, err
	}
	return r, nil
}

// Launch is how a daemon for a config is started. OwnConfig is a config the packaged
// units do not serve; its daemon gets its own unit, or is started directly, with
// --config.
type Launch struct {
	ConfigPath string
	OwnConfig  bool
}

// Ensure returns a client attached to the instance's daemon, starting one if needed;
// note is non-empty when one was started (nothing was notified while it was down).
func Ensure(ctx context.Context, storage domain.Storage, launch Launch, timeout time.Duration) (r *Remote, note string, err error) {
	socket, err := SocketPath(storage)
	if err != nil {
		return nil, "", err
	}

	// Listening but not ready means someone else started it: wait without claiming to
	// have started it.
	if _, serr := statusProbe(ctx, NewRemote(socket)); serr == nil {
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

// spawn starts the daemon so it outlives this process: via systemd first, a detached
// exec as fallback, never as a child of the client.
func spawn(ctx context.Context, storage domain.Storage, launch Launch) error {
	// Not canceled with the client (a half-issued start should finish), but bounded.
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spawnTimeout)
	defer cancel()

	if _, err := exec.LookPath("systemctl"); err != nil {
		return detach(daemonBinary, detachArgs(storage, launch))
	}
	unit, err := systemdStart(startCtx, storage, launch)
	if err == nil {
		return nil
	}
	// The unit may be missing or systemd may have no user session: fall back, keeping
	// systemd's reason.
	if derr := detach(daemonBinary, detachArgs(storage, launch)); derr != nil {
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
	// #nosec G204 -- unit is defaultUnit or ownUnitName(instance), restricted to
	// [A-Za-z0-9._-]; no shell, no separators.
	if out, err := exec.CommandContext(ctx, "systemctl", "--user", "start", unit).CombinedOutput(); err != nil {
		return unit, fmt.Errorf("%w (%s)", err, trimOutput(out))
	}
	return unit, nil
}

// LogFlag is the kithd flag naming a log file instead of stderr.
const LogFlag = "--log-file"

// DaemonLogPath is where a daemon started without systemd logs when there is no
// journal (or `[log] target` is "file"): kithd.log in the state directory.
func DaemonLogPath(storage domain.Storage) string {
	return storage.LogPath("kithd")
}

// detachArgs is the detached daemon's argv after the binary: logging to
// DaemonLogPath, never the client's terminal, and naming a non-default config.
func detachArgs(storage domain.Storage, launch Launch) []string {
	args := []string{LogFlag, DaemonLogPath(storage)}
	if launch.OwnConfig && launch.ConfigPath != "" {
		args = append(args, "--config", launch.ConfigPath)
	}
	return args
}

// readiness is what one Status probe learned: ready, the sync loop's last failure, and
// what a daemon that cannot start waits for.
type readiness struct {
	ready            bool
	lastErr, waiting string
}

// statusProbe asks the daemon whether it is ready, bounded: Remote has no client
// timeout (streams stay open), so a wedged daemon would hang the caller.
func statusProbe(ctx context.Context, r *Remote) (readiness, error) {
	probe, cancel := context.WithTimeout(ctx, reattachProbeTimeout)
	defer cancel()
	resp, err := call(probe, "status", r.c.Status, &v1.StatusRequest{})
	if err != nil {
		return readiness{}, err
	}
	return readiness{ready: resp.GetReady(), lastErr: resp.GetLastError(), waiting: resp.GetWaiting()}, nil
}

// waitReady polls Status until the daemon is ready, ctx ends, or timeout expires. A
// daemon waiting for someone to act (api.ErrDaemonWaiting) ends it at once, saying why.
func waitReady(ctx context.Context, r *Remote, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr string
	for {
		st, err := statusProbe(ctx, r)
		switch {
		case err == nil && st.ready:
			return nil
		case err == nil && st.waiting != "":
			return fmt.Errorf("%w: %s", api.ErrDaemonWaiting, st.waiting)
		case err == nil && st.lastErr != "":
			// A transient sync failure may resolve.
			lastErr = st.lastErr
		}
		if ctx.Err() != nil {
			return fmt.Errorf("daemon: waiting for %s: %w", daemonBinary, ctx.Err())
		}
		if time.Now().After(deadline) {
			if lastErr != "" {
				return fmt.Errorf("daemon: %s did not become ready in %s: %s", daemonBinary, human.Duration(timeout), lastErr)
			}
			return fmt.Errorf("daemon: %s did not become ready in %s", daemonBinary, human.Duration(timeout))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon: waiting for %s: %w", daemonBinary, ctx.Err())
		case <-time.After(readyPoll):
		}
	}
}

// waitingPoll is how often a client sitting out a waiting daemon asks again.
const waitingPoll = time.Second

// startGap bounds how long a daemon that stopped waiting may not answer: between its
// waiting socket and its real one it opens the cache and starts the networks.
const startGap = 30 * time.Second

// WaitOut waits while the instance's daemon waits for someone to act
// (api.ErrDaemonWaiting), telling say each reason it gives, and returns once it no
// longer does: started, or gone for longer than a start takes. Only ctx ends it early,
// with ctx's error.
func WaitOut(ctx context.Context, storage domain.Storage, say func(why string)) error {
	socket, err := SocketPath(storage)
	if err != nil {
		return err
	}
	r := NewRemote(socket)
	said := ""
	var silent time.Time // since when the daemon has not answered
	for {
		st, err := statusProbe(ctx, r)
		switch {
		case ctx.Err() != nil:
			return fmt.Errorf("daemon: waiting out %s: %w", daemonBinary, ctx.Err())
		case err == nil && st.waiting == "":
			return nil
		case err == nil:
			silent = time.Time{}
			if st.waiting != said {
				said = st.waiting
				say(said)
			}
		case silent.IsZero():
			silent = time.Now()
		case time.Since(silent) > startGap:
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon: waiting out %s: %w", daemonBinary, ctx.Err())
		case <-time.After(waitingPoll):
		}
	}
}

// startedNote is the warning after auto-spawning, with the autostart hint while unit
// is not enabled.
func startedNote(ctx context.Context, unit string) string {
	note := "kithd was not running — started it. " +
		"Anything that arrived while it was down was not notified."
	if !unitEnabled(ctx, unit) {
		note += "\n" + autostartHint(unit)
	}
	return note
}

// unitEnabled reports whether the user unit is enabled; any failure counts as not.
func unitEnabled(ctx context.Context, unit string) bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "is-enabled", unit).Output() // #nosec G204 -- see systemdStart
	return err == nil && trimOutput(out) == "enabled"
}

func trimOutput(out []byte) string {
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}

// ErrNoRestart means systemd does not run the daemon here (no user session, or
// started by hand), so this client cannot restart it.
var ErrNoRestart = errors.New("daemon: kithd is not run by systemd here, so it cannot be restarted from kith — stop it and start kith again")

// Restart restarts the instance's daemon through systemd and waits until it is
// listening (not necessarily synced), which is what a login needs. Newly enabled
// networks start only with the daemon.
func Restart(ctx context.Context, storage domain.Storage, launch Launch, timeout time.Duration) error {
	socket, err := SocketPath(storage)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return ErrNoRestart
	}
	unit := launch.unitName(storage)
	restartCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spawnTimeout)
	defer cancel()
	// #nosec G204 -- see systemdStart.
	if err := exec.CommandContext(restartCtx, "systemctl", "--user", "is-active", "--quiet", unit).Run(); err != nil {
		return ErrNoRestart // not run by systemd: a restart would start a second daemon
	}
	// #nosec G204 -- as above.
	if out, err := exec.CommandContext(restartCtx, "systemctl", "--user", "restart", unit).CombinedOutput(); err != nil {
		return fmt.Errorf("daemon: restart %s: %w (%s)", unit, err, trimOutput(out))
	}
	return waitListening(ctx, NewRemote(socket), timeout)
}

// waitListening polls Status until the daemon answers, ctx ends, or timeout expires.
func waitListening(ctx context.Context, r *Remote, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := statusProbe(ctx, r)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon: kithd did not answer within %s of restarting: %w", human.Duration(timeout), err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // the caller's own
		case <-time.After(readyPoll):
		}
	}
}
