package spell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// The engine runs as one long-lived child. A dead engine must never take the composer
// down: every failure marks the checker dead and later calls return ErrDead.

// ErrDead is returned once the engine has stopped.
var ErrDead = errors.New("spell: the checker is not running")

// Checker is a running engine. The mutex is held across write and read: the protocol
// is a dialog on one pair of pipes.
type Checker struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
	// outPipe is out's pipe, closed by die to end a read a surviving child holds up.
	outPipe io.Closer
	// reading closes when the in-flight reply reader returns; nil when none runs.
	reading <-chan struct{}
	dead    error
}

// Start launches the engine for avail, with personal as the "add to dictionary" file.
// ctx is the engine's lifetime, not a single check's.
func Start(ctx context.Context, avail Availability, personal string) (*Checker, error) {
	if !avail.Ready() {
		return nil, fmt.Errorf("spell: %w", ErrDead)
	}
	args := []string{"-a", "-d", strings.Join(avail.Tags(), ",")}
	if personal != "" && touch(personal) == nil {
		args = append(args, "-p", personal)
	}
	return start(ctx, avail.Engine.Path, args, avail.Dirs())
}

// touch creates the personal dictionary if missing: hunspell silently saves nothing to
// a -p file that does not exist. On failure the caller just drops -p.
func touch(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- the personal dictionary under kith's data directory
	if err != nil {
		return fmt.Errorf("spell: personal dictionary: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("spell: personal dictionary: %w", err)
	}
	return nil
}

// start is Start with the command line decided, for stub engines in tests. dicPath
// must be passed explicitly: an engine that cannot resolve `-d name` exits silently,
// and kith's own data directory is not on any default search path. The banner is
// consumed here so it is not read as the first answer.
func start(ctx context.Context, path string, args []string, dicPath []string) (*Checker, error) {
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204,G702 -- configured program and dictionary tags as argv, never a shell
	if len(dicPath) > 0 {
		// The last DICPATH wins, and it includes any inherited one.
		cmd.Env = append(os.Environ(), "DICPATH="+strings.Join(dicPath, string(os.PathListSeparator)))
	}
	ownGroup(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("spell: open stdin: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("spell: open stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spell: start %s: %w", path, err)
	}
	c := &Checker{cmd: cmd, in: in, out: bufio.NewReader(out), outPipe: out}
	if err := c.readBanner(ctx); err != nil {
		_ = c.Close() // always nil; the missing banner is the report
		return nil, fmt.Errorf("spell: %s produced no banner: %w", path, err)
	}
	return c, nil
}

// bannerTimeout bounds the wait for the engine's first line. The caller starts the
// engine while holding its own lock, so an engine that never speaks must not hang it.
const bannerTimeout = 5 * time.Second

// readBanner consumes the engine's banner line, giving up after bannerTimeout or ctx.
// The engine itself runs on ctx (exec.CommandContext), so the wait has its own timer.
func (c *Checker) readBanner(ctx context.Context) error {
	banner := make(chan error, 1)
	reading := make(chan struct{})
	c.reading = reading // Close, on the timeout path, kills the engine and waits for it
	go func() {
		defer close(reading)
		_, err := c.out.ReadString('\n')
		banner <- err
	}()
	wait, cancel := context.WithTimeout(ctx, bannerTimeout)
	defer cancel()
	select {
	case err := <-banner:
		<-reading
		c.reading = nil
		return err
	case <-wait.Done():
		return fmt.Errorf("none within %s: %w", bannerTimeout, wait.Err())
	}
}

// Check asks the engine about one word. A check that outlives ctx kills the engine
// rather than leaving a reader parked on the pipe.
func (c *Checker) Check(ctx context.Context, word string) (Verdict, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead != nil {
		return Verdict{}, c.dead
	}
	if _, err := io.WriteString(c.in, escape(word)+"\n"); err != nil {
		return Verdict{}, c.die(err)
	}

	type reply struct {
		lines []string
		err   error
	}
	done := make(chan reply, 1)
	reading := make(chan struct{})
	c.reading = reading
	defer func() { c.reading = nil }()
	go func() {
		defer close(reading)
		var lines []string
		for {
			line, err := c.out.ReadString('\n')
			if err != nil {
				done <- reply{err: err}
				return
			}
			// A blank line ends the answer.
			if line = strings.TrimRight(line, "\r\n"); line == "" {
				done <- reply{lines: lines}
				return
			}
			lines = append(lines, line)
		}
	}()

	select {
	case <-ctx.Done():
		// Killing the process unblocks the parked reader. die's result is the same
		// cause, which the return below already carries.
		_ = c.die(ctx.Err())
		return Verdict{}, fmt.Errorf("spell: checking %q: %w", word, ctx.Err())
	case r := <-done:
		if r.err != nil {
			return Verdict{}, c.die(r.err)
		}
		return parseVerdict(r.lines), nil
	}
}

// Add puts a word in the personal dictionary; Ignore accepts it for this session.
// Neither reads a reply: commands produce none.
func (c *Checker) Add(word string) error    { return c.command("*", word) }
func (c *Checker) Ignore(word string) error { return c.command("@", word) }

// Save writes the personal dictionary out (called after each Add).
func (c *Checker) Save() error { return c.command("#", "") }

func (c *Checker) command(verb, word string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead != nil {
		return c.dead
	}
	if _, err := io.WriteString(c.in, verb+word+"\n"); err != nil {
		return c.die(err)
	}
	return nil
}

// Live reports whether the engine is still answering.
func (c *Checker) Live() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead == nil
}

// Close stops the engine. Idempotent.
func (c *Checker) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead != nil {
		return nil
	}
	_ = c.die(ErrDead) // an asked-for stop is not a failure to report
	return nil
}

// die records why the engine stopped and kills it. The caller holds the mutex.
func (c *Checker) die(cause error) error {
	if c.dead != nil {
		return c.dead
	}
	c.dead = fmt.Errorf("%w: %w", ErrDead, cause)
	// Teardown of a process being killed: each step may fail because it already
	// exited, and cause (returned) is what went wrong.
	_ = c.in.Close()
	if c.cmd.Process == nil {
		return c.dead
	}
	killEngine(c.cmd)
	// Reader first, then Wait: Wait closes the pipes the reader is using. Closing the
	// read end ends the read even if something the engine started still holds the
	// write end (a wrapper whose child survived).
	if c.reading != nil {
		_ = c.outPipe.Close()
		<-c.reading
	}
	_ = c.cmd.Wait()
	return c.dead
}
