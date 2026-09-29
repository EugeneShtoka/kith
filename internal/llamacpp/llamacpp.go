// Package llamacpp reads a local language model as a next-token distribution.
//
// A keyboard ranks what comes next rather than chatting, so this asks llama-server's
// `/completion` for `n_probs` instead of prompting a chat model. It is a leaf (depguard
// `llamacpp-is-a-leaf`): a prompt and a count in, words out.
//
// The server is large to hold and slow to load relative to one answer, so it is lazy
// (spawned on first ask) and idle-stopped.
package llamacpp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Settings is everything needed to run one local model. Zero values mean defaults.
type Settings struct {
	// Command is the llama-server program, on PATH or as a path.
	Command string
	// Model is the path to the .gguf weights.
	Model string
	// Port on loopback; 0 picks a free one.
	Port int
	// Threads for the server; 0 leaves llama-server's default.
	Threads int
	// Context is the token window (contextDefault). Kept small: the KV cache is most of
	// the cost beyond the weights.
	Context int
	// Idle before the server is stopped (idleDefault); negative never stops it.
	Idle time.Duration
	// Startup bounds the wait for /health (startupDefault).
	Startup time.Duration
	// Timeout bounds one prediction (timeoutDefault).
	Timeout time.Duration
}

// ErrNotConfigured is returned when there is no model to read: a state, not a failure.
var ErrNotConfigured = errors.New("llamacpp: no model configured")

// ErrNoServer is returned when the server program is not installed.
var ErrNoServer = errors.New("llamacpp: llama-server is not installed")

// Predictor is one model, spawned when first asked and stopped when it goes quiet.
// Safe for concurrent use.
type Predictor struct {
	mu       sync.Mutex
	settings Settings
	running  *server // nil when nothing is spawned; replaced, never restarted
	closed   bool    // no lazy spawn after Close
}

// NewPredictor holds the settings and spawns nothing; nothing is validated either,
// since "not installed" is an ordinary state.
func NewPredictor(s Settings) *Predictor { return &Predictor{settings: s} }

// Available reports whether weights are named and the server program exists.
func (p *Predictor) Available() bool { return p.Why() == nil }

// Why is Available's reason, or nil: ErrNotConfigured or ErrNoServer, which have
// different fixes.
func (p *Predictor) Why() error {
	if p == nil {
		return ErrNotConfigured
	}
	p.mu.Lock()
	settings := p.settings
	p.mu.Unlock()
	if strings.TrimSpace(settings.Model) == "" {
		return ErrNotConfigured
	}
	if _, err := exec.LookPath(settings.Command); err != nil {
		return fmt.Errorf("%w: %s not found on PATH", ErrNoServer, settings.Command)
	}
	return nil
}

// NextWords ranks up to n words the model expects after prompt, best first.
//
// The trailing space is stripped: a space is its own token, so the word boundary must
// fall in the next token — "check the " yields digits, "check the" yields words.
func (p *Predictor) NextWords(ctx context.Context, prompt string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	prompt = strings.TrimRight(prompt, " \t")
	if prompt == "" {
		return nil, nil
	}
	srv, timeout, err := p.serve(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Extra tokens because punctuation, fragments and case duplicates are filtered out.
	probs, err := srv.distribution(ctx, prompt, n+probeSpare)
	if err != nil {
		// A dead server is forgotten so the next ask spawns a fresh one.
		if gone(ctx, srv, err) {
			p.forget(srv)
		}
		return nil, err
	}
	return words(probs, n), nil
}

// gone reports whether a failed ask means the server is dead: its process exited, or
// the connection failed for a reason other than this ask's own deadline. A slow answer
// keeps it (a cold first prompt on a slow CPU outlasts the timeout, and a respawn costs
// seconds more), and so does a reply that was not a prediction.
func gone(ctx context.Context, srv *server, err error) bool {
	select {
	case <-srv.died():
		return true
	default:
	}
	var reply replyErr
	return ctx.Err() == nil && !errors.As(err, &reply)
}

const probeSpare = 8

// Close stops the server, if one is running.
func (p *Predictor) Close() error {
	p.mu.Lock()
	srv := p.running
	p.running, p.closed = nil, true
	p.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.stop()
}

// serve returns a running server, spawning one if needed. The lock is held across the
// spawn so concurrent asks never start two servers.
func (p *Predictor) serve(ctx context.Context) (*server, time.Duration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, 0, ErrNotConfigured
	}
	settings := p.settings
	timeout := orDefault(settings.Timeout, timeoutDefault)
	if p.running != nil {
		p.running.touch()
		return p.running, timeout, nil
	}
	if strings.TrimSpace(settings.Model) == "" {
		return nil, 0, ErrNotConfigured
	}
	srv, err := start(ctx, settings)
	if err != nil {
		return nil, 0, err
	}
	p.running = srv
	go p.watchIdle(srv, idleOr(settings.Idle))
	return srv, timeout, nil
}

// forget stops srv unless it has already been replaced — a late failure must not kill
// the fresh server that took its place.
func (p *Predictor) forget(srv *server) {
	p.mu.Lock()
	if p.running != srv {
		p.mu.Unlock()
		return
	}
	p.running = nil
	p.mu.Unlock()
	_ = srv.stop() // stop never fails (a process already gone is stopped)
}

// watchIdle stops srv once it has gone unused for idle, checking at idle/4 (≥1s).
// It ends with the server.
func (p *Predictor) watchIdle(srv *server, idle time.Duration) {
	if idle <= 0 {
		return
	}
	ticker := time.NewTicker(max(idle/4, time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-srv.died():
			return
		case <-ticker.C:
			if srv.idleFor() < idle {
				continue
			}
			p.forget(srv)
			return
		}
	}
}

// idleOr fills in the default for zero and leaves negative ("never stop") alone.
func idleOr(d time.Duration) time.Duration {
	if d == 0 {
		return idleDefault
	}
	return d
}

// orDefault is d, or def when d is not positive.
func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

const (
	startupDefault = 20 * time.Second
	timeoutDefault = time.Second
	idleDefault    = 10 * time.Minute
	contextDefault = 1024
)
