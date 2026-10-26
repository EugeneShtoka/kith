package signal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/api/signal/v1/signalv1connect"
)

// How kithd keeps the helper: a heartbeat every beatEvery, each answered within
// beatTimeout; maxMisses unanswered in a row and the helper is killed (one slow answer
// is not a hang). The helper gives up on kithd after 10 s without a beat.
const (
	beatEvery   = 2 * time.Second
	beatTimeout = 2 * time.Second
	maxMisses   = 3
	// startTimeout bounds opening (and migrating) the key store.
	startTimeout = time.Minute
)

// A helper that ends is started again after a wait that doubles from minBackoff to
// maxBackoff, so one that dies at once does not spin; one that ran stableRun counts
// as having worked, and the wait starts over.
const (
	minBackoff = time.Second
	maxBackoff = time.Minute
	stableRun  = time.Minute
)

// helperName is the helper's binary.
const helperName = "kith-signal"

var (
	errHung         = errors.New("kith-signal stopped answering")
	errNotInstalled = errors.New("kith-signal is not installed: put it beside kithd or on $PATH")
)

// helper is a running kith-signal that has opened its key store.
type helper struct {
	client signalv1connect.SignalServiceClient
	// devices are the linked devices' ACIs; states their connections, as the helper says.
	mu      sync.Mutex
	devices map[string]bool
	states  map[string]connState
}

func (h *helper) has(aci string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.devices[aci]
}

func (h *helper) add(aci string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.devices[aci] = true
}

func (h *helper) remove(aci string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.devices, aci)
	delete(h.states, aci)
}

// noStoreKey is why the helper is not run without the store's key.
const noStoreKey = "the store's key could not be had from the keyring (kithd's log says why)"

// supervisor runs kith-signal until stopped, starting it again whenever it ends.
type supervisor struct {
	stop context.CancelFunc
	done chan struct{}
}

// superviseNow runs the helper when started with an account, and ends it when there is
// none.
func (a *Adapter) superviseNow() {
	a.mu.Lock()
	want := a.started && !a.stopped && len(a.accounts) > 0
	running := a.supervisor != nil
	if want && !running {
		ctx, stop := context.WithCancel(a.run)
		s := &supervisor{stop: stop, done: make(chan struct{})}
		a.supervisor = s
		a.mu.Unlock()
		go a.supervise(ctx, s)
		return
	}
	a.mu.Unlock()
	if !want && running {
		a.stopSupervising()
	}
}

// stopSupervising kills the helper, if it runs, and waits until it is gone.
func (a *Adapter) stopSupervising() {
	a.mu.Lock()
	s := a.supervisor
	a.supervisor = nil
	a.mu.Unlock()
	if s != nil {
		s.stop()
		<-s.done
	}
}

// supervise runs the helper again and again until ctx ends.
func (a *Adapter) supervise(ctx context.Context, s *supervisor) {
	defer close(s.done)
	if len(a.key) == 0 {
		// The helper cannot open the store without it; the cached chats are still served.
		a.goneDown(noStoreKey, true)
		return
	}
	ln, err := listen(ctx, a.paths.Socket)
	if err != nil {
		a.log.Error("Signal is off: kith-signal's socket will not open", "path", a.paths.Socket, "err", err)
		a.goneDown("its socket will not open: "+err.Error(), true)
		return
	}
	defer func() { _ = ln.Close() }() // nothing more is served on it
	backoff := minBackoff
	for {
		began := time.Now()
		err := a.runHelper(ctx, ln)
		if ctx.Err() != nil {
			a.goneDown("stopped", false)
			return
		}
		if time.Since(began) >= stableRun {
			backoff = minBackoff
		}
		a.log.Warn("kith-signal ended; starting it again", "err", err, "in", backoff)
		if errors.Is(err, errNotInstalled) {
			a.goneDown(err.Error(), true)
		} else {
			a.goneDown("kith-signal stopped ("+err.Error()+"); starting it again", false)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

// goneDown marks the helper down for why (failed: it cannot come up until something
// changes), and says so.
func (a *Adapter) goneDown(why string, failed bool) {
	a.mu.Lock()
	if a.helper != nil {
		a.helper = nil
		a.up = make(chan struct{})
	}
	a.held = nil // the helper that held them is gone: Signal sends them again
	a.down, a.failed = why, failed
	a.mu.Unlock()
	a.announceAll()
}

// cameUp marks h up, and says so.
func (a *Adapter) cameUp(h *helper) {
	a.mu.Lock()
	a.helper = h
	close(a.up)
	a.mu.Unlock()
	a.announceAll()
}

// runHelper starts kith-signal on ln and keeps it until it ends, stops answering, or
// ctx ends; it is killed then if it is still there, and gone when this returns.
func (a *Adapter) runHelper(ctx context.Context, ln *net.UnixListener) error {
	path, err := findHelper()
	if err != nil {
		return err
	}
	cmd, output, err := a.spawn(path, ln)
	if err != nil {
		return err
	}
	life, end := context.WithCancelCause(ctx)
	exited := make(chan struct{})
	go func() {
		werr := cmd.Wait()
		end(fmt.Errorf("kith-signal exited: %w", exitError(werr)))
		close(exited)
	}()
	defer func() {
		end(nil)
		_ = cmd.Process.Kill() // gone already, or hung: either way it ends here
		<-exited
		output.flush()
	}()

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", a.paths.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := signalv1connect.NewSignalServiceClient(&http.Client{Transport: transport}, "http://kith-signal")
	go beat(life, client, end)

	starting, cancel := context.WithTimeout(life, startTimeout)
	resp, err := client.Start(starting, connect.NewRequest(&signalv1.StartRequest{Key: a.key}))
	cancel()
	if err != nil {
		if cause := context.Cause(life); cause != nil {
			return cause //nolint:wrapcheck // says itself what ended it
		}
		return fmt.Errorf("kith-signal did not start: %w", err)
	}
	h := &helper{client: client, devices: map[string]bool{}, states: map[string]connState{}}
	for _, d := range resp.Msg.GetDevices() {
		h.devices[d.GetAci()] = true
	}
	a.log.Info("kith-signal is up", "path", path, "linked", len(h.devices))
	a.cameUp(h)
	go a.receive(life, h)
	<-life.Done()
	return context.Cause(life) //nolint:wrapcheck // says itself what ended it
}

// spawn starts the helper at path, handing it a copy of ln (ln stays kithd's), its
// output going into kithd's log.
func (a *Adapter) spawn(path string, ln *net.UnixListener) (*exec.Cmd, *lineLog, error) {
	socket, err := ln.File()
	if err != nil {
		return nil, nil, fmt.Errorf("hand kith-signal its socket: %w", err)
	}
	defer func() { _ = socket.Close() }()              // the helper has its own
	cmd := exec.Command(path, "-store", a.paths.Store) //nolint:gosec,noctx // our own helper, killed by runHelper rather than by a context
	cmd.ExtraFiles = []*os.File{socket}
	output := &lineLog{log: a.log.With("component", helperName)}
	cmd.Stderr = output
	cmd.Stdout = output
	dieWithKithd(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s: %w", path, err)
	}
	return cmd, output, nil
}

// beat sends the helper a heartbeat every beatEvery until life ends, and ends it with
// errHung after maxMisses unanswered in a row.
func beat(life context.Context, client signalv1connect.SignalServiceClient, end context.CancelCauseFunc) {
	ticker := time.NewTicker(beatEvery)
	defer ticker.Stop()
	misses := 0
	for {
		answering, cancel := context.WithTimeout(life, beatTimeout)
		_, err := client.Heartbeat(answering, connect.NewRequest(&signalv1.HeartbeatRequest{}))
		cancel()
		if life.Err() != nil {
			return
		}
		if err == nil {
			misses = 0
		} else if misses++; misses >= maxMisses {
			end(errHung)
			return
		}
		select {
		case <-life.Done():
			return
		case <-ticker.C:
		}
	}
}

// findHelper is kith-signal beside the running kithd, else on $PATH.
func findHelper() (string, error) {
	if self, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(self), helperName)
		if info, err := os.Stat(beside); err == nil && info.Mode().IsRegular() {
			return beside, nil
		}
	}
	path, err := exec.LookPath(helperName)
	if err != nil {
		return "", errNotInstalled
	}
	return path, nil
}

// listen opens the helper's socket owner-only. A socket left there is an earlier
// kithd's: this instance's lock is kithd's, so nobody else serves it.
func listen(ctx context.Context, path string) (*net.UnixListener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove the old socket: %w", err)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close() // the chmod failure is the report
		return nil, fmt.Errorf("make the socket owner-only: %w", err)
	}
	unix, ok := ln.(*net.UnixListener)
	if !ok {
		_ = ln.Close() // not a socket the helper can be handed
		return nil, errors.New("not a unix socket")
	}
	return unix, nil
}

// exitError says how the helper ended.
func exitError(err error) error {
	if err == nil {
		return errors.New("on its own")
	}
	return err
}

// lineLog writes the helper's output into kithd's log, a line at a time, at the
// level the line names (its own log is slog's text format).
type lineLog struct {
	log *slog.Logger
	mu  sync.Mutex
	buf []byte
}

func (l *lineLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		line, rest, ok := strings.Cut(string(l.buf), "\n")
		if !ok {
			break
		}
		l.buf = []byte(rest)
		l.line(line)
	}
	return len(p), nil
}

func (l *lineLog) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) > 0 {
		l.line(string(l.buf))
		l.buf = nil
	}
}

func (l *lineLog) line(line string) {
	if line = strings.TrimSpace(line); line == "" {
		return
	}
	level := slog.LevelInfo
	switch {
	case strings.Contains(line, "level=ERROR"):
		level = slog.LevelError
	case strings.Contains(line, "level=WARN"):
		level = slog.LevelWarn
	case strings.Contains(line, "level=DEBUG"):
		level = slog.LevelDebug
	}
	l.log.Log(context.Background(), level, line)
}
