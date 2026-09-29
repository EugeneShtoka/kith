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
)

// server is a spawned llama-server and the address it answers on. It is not ready
// until /health says so, and it must die with us: the whole process group (job
// object on Windows) is signaled.
type server struct {
	cmd *exec.Cmd
	// base is the server's URL origin (scheme, host and port).
	base string
	http *http.Client
	// exited is closed by the reaper. Reading cmd.ProcessState is only safe after it.
	exited chan struct{}
	// tree is what stop signals: nothing on unix (pgid == pid), a job object on Windows.
	tree tree

	mu   sync.Mutex
	last time.Time
	now  func() time.Time // replaced in tests
}

// start spawns a server and waits until it answers. ctx bounds only the wait: the
// server outlives the request that started it, ending via Close or the idle watch.
func start(ctx context.Context, settings Settings) (*server, error) {
	port := settings.Port
	if port == 0 {
		free, err := freePort(ctx)
		if err != nil {
			return nil, err
		}
		port = free
	}

	// Spawned and reaped from one goroutine locked to its OS thread for the child's
	// life: PR_SET_PDEATHSIG fires when the forking thread exits, and Go retires idle
	// threads. The reaper also prevents zombies.
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
		// The exit is observed through exited; a server we stop exits non-zero, and
		// one that dies early fails wait (or the next prediction) with its own error.
		_ = cmd.Wait()
	}()

	got := <-started
	if got.err != nil {
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
		now:    time.Now,
	}
	srv.touch()
	if err := srv.wait(ctx, orDefault(settings.Startup, startupDefault)); err != nil {
		_ = srv.stop() // always nil; the startup failure is the report
		return nil, err
	}
	return srv, nil
}

// args is the server command line: loopback only (it reads drafts), one slot (requests
// are serial and slots divide the context), no web UI.
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
				s.cmd.Path, limit, ctx.Err())
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

// stop sends SIGTERM, then SIGKILL after two seconds. A server that never spawned or
// is already gone counts as stopped.
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
	s.last = s.now()
	s.mu.Unlock()
}

func (s *server) idleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Sub(s.last)
}

func (s *server) url(path string) string { return s.base + path }

// freePort asks the OS for an unused loopback port. The window before llama-server
// binds it is racy, but a fixed port would collide with a previous hard-killed run.
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
