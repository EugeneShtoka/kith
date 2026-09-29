// Package audio plays one attachment at a time with pause, seek and speed control.
//
// It decodes nothing: mpv (or VLC) runs headless and is driven over its control
// socket, because a fire-and-forget player cannot pause, seek or change speed.
package audio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// defaultCommands are tried in order when the config names no player. mpv's flags make
// it an engine: no window, no terminal, no user config, and alive at end of file.
var defaultCommands = [][]string{
	{
		"mpv",
		"--no-video",
		"--no-terminal",
		"--no-config",
		"--keep-open=yes",
		"--audio-pitch-correction=yes",
	},
	{"vlc"},
}

// Speed bounds.
const (
	MinSpeed = 0.25
	MaxSpeed = 4.0
)

// State is everything a player bar draws.
type State struct {
	// Duration is zero until the player knows it.
	Position, Duration time.Duration
	Speed              float64
	// Paused is the player's own flag, not a guess made when the key was pressed.
	Paused bool
	// Done is end of file or the player exiting; neither is an error.
	Done bool
}

// Session is one running player. Every method tolerates a nil receiver and a closed
// session: a control that arrives after the end is a no-op.
type Session struct {
	conn   net.Conn
	cmd    *exec.Cmd
	dir    string // holds the control socket; removed on close
	speaks protocol
	stop   chan struct{} // ends the poll loop; nil for a player that pushes

	// writeMu orders writes on conn. Not mu: the reader and State need mu while a
	// write may wait on a player that stopped reading.
	writeMu sync.Mutex

	mu     sync.Mutex
	st     State
	closed bool
	// pending are queries sent and not yet answered, oldest first: VLC's replies
	// carry no names.
	pending []string
}

// Play starts command on path and returns the session controlling it. speed is
// applied at startup so the note does not begin at 1×.
func Play(ctx context.Context, command []string, path string, speed float64) (*Session, error) {
	program, err := Resolve(command)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "kith-audio-")
	if err != nil {
		return nil, fmt.Errorf("audio: no directory for the control socket: %w", err)
	}
	socket := filepath.Join(dir, "ipc")

	speaks := dialectOf(program[0])
	args := append([]string{}, program[1:]...)
	args = append(args, speaks.flags(socket, speed)...)
	args = append(args, "--", path)
	// #nosec G204 -- program comes from config as a command and its flags, never a shell
	cmd := exec.CommandContext(ctx, program[0], args...)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err = cmd.Start(); err != nil {
		_ = os.RemoveAll(dir) // cleanup on the error path; the start failure is the report
		return nil, fmt.Errorf("audio: could not run %s: %w", program[0], err)
	}
	// Reaped here so waitForSocket can notice an immediate exit.
	exited := make(chan struct{})
	// The exit status is not reported: a player that dies early shows up as
	// waitForSocket's error, and one killed by Close is expected to exit non-zero.
	go func() { _ = cmd.Wait(); close(exited) }()

	conn, err := waitForSocket(ctx, socket, exited)
	if err != nil {
		// Cleanup on the error path; waitForSocket's error is the report.
		_ = cmd.Process.Kill()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return attachTo(conn, State{Speed: clampSpeed(speed)}, speaks, cmd, dir), nil
}

// dialectOf picks the control language by program name; unknown names (usually a
// wrapper script) are assumed to be mpv.
func dialectOf(program string) protocol {
	switch name := strings.ToLower(filepath.Base(program)); {
	case name == "cvlc", name == "nvlc", strings.HasPrefix(name, "vlc"):
		return vlcRC{}
	default:
		return mpvIPC{}
	}
}

// Resolve is the command to run: the configured one as written, or the first
// installed default.
func Resolve(command []string) ([]string, error) {
	if err := controllable(); err != nil {
		return nil, err
	}
	if len(command) > 0 {
		if _, err := exec.LookPath(command[0]); err != nil {
			return nil, fmt.Errorf("audio: cannot run %q: %w", command[0], err)
		}
		return command, nil
	}
	names := make([]string, 0, len(defaultCommands))
	for _, candidate := range defaultCommands {
		if _, err := exec.LookPath(candidate[0]); err == nil {
			return candidate, nil
		}
		names = append(names, candidate[0])
	}
	return nil, fmt.Errorf("audio: no player found — install one of %s, "+
		"or name yours in [display.media.audio] player", strings.Join(names, " or "))
}

// socketWait is how long the player is given to create its control socket.
const socketWait = 5 * time.Second

// waitForSocket dials the control socket once the player has made it, or gives up
// early if the player exits or ctx ends.
func waitForSocket(ctx context.Context, socket string, exited <-chan struct{}) (net.Conn, error) {
	var dialer net.Dialer
	deadline := time.Now().Add(socketWait)
	for {
		if conn, err := dialer.DialContext(ctx, "unix", socket); err == nil {
			return conn, nil
		}
		select {
		case <-exited:
			return nil, errors.New("audio: the player exited before it could be controlled")
		default:
		}
		if time.Now().After(deadline) {
			return nil, errors.New("audio: the player never opened its control socket")
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("audio: gave up waiting for the player: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// observed are the mpv properties watched; index+1 is the observation ID.
var observed = []string{"time-pos", "duration", "pause", "speed", "eof-reached"}

// attachTo wires a session to an open control connection. Separate from Play so the
// protocol can be tested against a stub.
func attachTo(conn net.Conn, initial State, speaks protocol, cmd *exec.Cmd, dir string) *Session {
	s := &Session{conn: conn, st: initial, speaks: speaks, cmd: cmd, dir: dir}
	speaks.begin(s)
	go s.read()
	if every := speaks.poll(); every > 0 {
		s.stop = make(chan struct{})
		// Passed rather than re-read: Close clears the field.
		go s.pollLoop(s.stop, every)
	}
	return s
}

// pollLoop asks the player where it is until the session ends or the socket fails.
func (s *Session) pollLoop(stop <-chan struct{}, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := s.speaks.ask(s); err != nil {
				// The socket is gone, so the player is: the bar stops moving, and the
				// next control the user presses returns this same failure to them.
				return
			}
		}
	}
}

// State is what the player is doing right now.
func (s *Session) State() State {
	if s == nil {
		return State{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// TogglePause plays or pauses; from the end it replays from the start.
func (s *Session) TogglePause() error {
	if s == nil {
		return nil
	}
	return s.speaks.pause(s)
}

// Seek moves by d. Seeking back from the end clears Done at once, so the bar does not
// keep reading "done" until the player reports otherwise.
func (s *Session) Seek(d time.Duration) error {
	if s == nil {
		return nil
	}
	if d < 0 {
		s.mu.Lock()
		s.st.Done = false
		s.mu.Unlock()
	}
	return s.speaks.seek(s, d)
}

// SetSpeed plays at f times normal, clamped to [MinSpeed, MaxSpeed].
func (s *Session) SetSpeed(f float64) error {
	if s == nil {
		return nil
	}
	return s.speaks.speed(s, f)
}

// Close stops the player and removes the socket. Safe to call twice.
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed, s.st.Done = true, true
	conn, cmd, dir, stop := s.conn, s.cmd, s.dir, s.stop
	s.stop = nil
	s.mu.Unlock()

	if stop != nil {
		close(stop)
	}
	// Teardown, all best effort: the player may have exited already (then quit,
	// Close and Kill fail harmlessly), and a leftover socket dir is in a temp dir.
	// Asked to quit before being killed, so it closes the audio device cleanly.
	_ = s.speaks.quit(s)
	if conn != nil {
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill() // Play's goroutine reaps it
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// sendJSON writes one mpv IPC command.
func (s *Session) sendJSON(command ...any) error {
	line, err := json.Marshal(struct {
		Command []any `json:"command"`
	}{command})
	if err != nil {
		return fmt.Errorf("audio: encode %v: %w", command, err)
	}
	return s.write(string(line))
}

// writeTimeout bounds one control write: a player that stops reading its socket
// fails the control instead of holding the UI's key press.
const writeTimeout = 2 * time.Second

// write puts one line on the control socket. conn is set once, at attach.
func (s *Session) write(line string) error {
	if s.conn == nil {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// A connection without deadlines only loses the bound, not the write.
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := s.conn.Write([]byte(line + "\n")); err != nil {
		// Part of the line may be out, and a queued question would take the next
		// answer: nothing said after this can be trusted, so the session ends here
		// (the reader sees the close and marks it done; later writes fail at once).
		_ = s.conn.Close()
		return fmt.Errorf("audio: send %q: %w", line, err)
	}
	return nil
}

// maxLine caps one control line so a misbehaving player cannot grow our memory.
const maxLine = 1 << 16

// read folds everything the player says into the state until the connection ends.
func (s *Session) read() {
	scanner := bufio.NewScanner(s.conn)
	scanner.Buffer(make([]byte, 0, 4096), maxLine)
	for scanner.Scan() {
		s.speaks.absorb(s, scanner.Bytes())
	}
	s.mu.Lock()
	s.st.Done = true
	s.mu.Unlock()
}

// seconds turns float seconds into a Duration, never negative (mpv reports a small
// negative position while demuxing).
func seconds(f float64) time.Duration {
	if f <= 0 {
		return 0
	}
	return time.Duration(f * float64(time.Second))
}

func clampSpeed(f float64) float64 { return min(max(f, MinSpeed), MaxSpeed) }

// Format renders a duration as a player shows one: "0:34", "12:05", "1:02:03".
func Format(d time.Duration) string {
	total := int(max(d, 0).Round(time.Second).Seconds())
	hours, minutes, secs := total/3600, (total/60)%60, total%60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%d:%02d", minutes, secs)
}

// FormatSpeed renders a multiplier without trailing zeros: "1×", "1.5×", "1.25×".
func FormatSpeed(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".") + "×"
}
