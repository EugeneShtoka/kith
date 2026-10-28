package llamacpp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/human"
)

// server is a spawned llama-server. Stopping signals its whole process group (a job
// object on Windows) so it dies with us.
type server struct {
	cmd  *exec.Cmd
	base string // URL origin
	http *http.Client
	// exited is closed by the reaper; cmd.ProcessState is safe to read only after it.
	exited chan struct{}
	// tree is what stop signals: nothing on unix (pgid == pid), a job object on Windows.
	tree tree

	mu   sync.Mutex
	last time.Time
}

// start spawns a server and waits until it answers. ctx bounds only the wait; the
// server outlives it.
func start(ctx context.Context, settings Settings) (*server, error) {
	port := settings.Port
	if port == 0 {
		free, err := freePort(ctx)
		if err != nil {
			return nil, err
		}
		port = free
	}

	// Spawn and reap on one goroutine locked to its OS thread for the child's life:
	// PR_SET_PDEATHSIG fires when the forking thread exits, and Go retires idle threads.
	type spawn struct {
		cmd  *exec.Cmd
		tree tree
		err  error
	}
	started, exited := make(chan spawn, 1), make(chan struct{})
	// #nosec G118 -- the server must outlive ctx, which bounds only the startup wait
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(exited)

		// #nosec G204 -- the command and the model are the operator's own config
		cmd := exec.CommandContext(context.Background(), settings.Command, args(settings, port)...)
		cmd.SysProcAttr = groupAttr()
		dieWithParent(cmd.SysProcAttr)
		if err := cmd.Start(); err != nil {
			started <- spawn{err: err}
			return
		}
		t := adopt(cmd)
		defer t.release()
		started <- spawn{cmd: cmd, tree: t}
		// The exit is observed through exited; an early death surfaces in wait or the
		// next prediction.
		_ = cmd.Wait()
	}()

	got := <-started
	if got.err != nil {
		<-exited // it ends once it has reported
		if errors.Is(got.err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrNoServer, settings.Command)
		}
		return nil, fmt.Errorf("llamacpp: start %s: %w", settings.Command, got.err)
	}

	srv := &server{
		cmd:    got.cmd,
		tree:   got.tree,
		base:   "http://127.0.0.1:" + strconv.Itoa(port),
		http:   &http.Client{},
		exited: exited,
	}
	srv.touch()
	if err := srv.wait(ctx, orDefault(settings.Startup, startupDefault)); err != nil {
		_ = srv.stop() // always nil; the startup failure is the report
		return nil, err
	}
	return srv, nil
}

// args is the command line: loopback only (it reads drafts), one slot (requests are
// serial and slots divide the context), no web UI.
func args(settings Settings, port int) []string {
	window := settings.Context
	if window <= 0 {
		window = contextDefault
	}
	out := []string{
		"--model", settings.Model,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--ctx-size", strconv.Itoa(window),
		"--parallel", "1",
		"--no-webui",
	}
	if settings.Threads > 0 {
		out = append(out, "--threads", strconv.Itoa(settings.Threads))
	}
	return out
}

// wait polls /health until the server answers, exits, or limit passes.
func (s *server) wait(ctx context.Context, limit time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.healthy(ctx) {
			return nil
		}
		select {
		case <-s.died():
			return fmt.Errorf("llamacpp: %s exited during startup: %s",
				s.cmd.Path, s.cmd.ProcessState)
		case <-ctx.Done():
			return fmt.Errorf("llamacpp: %s did not become ready within %s: %w",
				s.cmd.Path, human.Duration(limit), ctx.Err())
		case <-ticker.C:
		}
	}
}

// healthy is one /health probe; 503 means still loading.
func (s *server) healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url("/health"), nil)
	if err != nil {
		return false
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// stop sends SIGTERM, then SIGKILL after two seconds. It always returns nil.
func (s *server) stop() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	if err := s.terminate(); err != nil {
		//nolint:nilerr // a signal to a process that is already gone is a stop that worked
		return nil
	}
	select {
	case <-s.died():
	case <-time.After(2 * time.Second):
		s.kill()
	}
	return nil
}

// died closes when the process is gone; nil (blocks forever) for one never started.
func (s *server) died() <-chan struct{} { return s.exited }

func (s *server) touch() {
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
}

func (s *server) idleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.last)
}

func (s *server) url(path string) string { return s.base + path }

// freePort asks the OS for an unused loopback port. Racy until llama-server binds it,
// but a fixed port would collide with a hard-killed earlier run.
func freePort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("llamacpp: find a free port: %w", err)
	}
	defer func() { _ = listener.Close() }()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("llamacpp: find a free port: not a TCP listener")
	}
	return addr.Port, nil
}
