package audio_test

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/audio"
)

// vlcStub is the other end of the RC socket: it collects command lines and answers
// with bare numbers, as VLC does.
type vlcStub struct {
	t     *testing.T
	conn  net.Conn
	lines chan string
}

func newVLCStub(t *testing.T, speed float64) (*audio.Session, *vlcStub) {
	t.Helper()
	client, server := net.Pipe()
	stub := &vlcStub{t: t, conn: server, lines: make(chan string, 64)}
	go func() {
		scanner := bufio.NewScanner(server)
		for scanner.Scan() {
			select {
			case stub.lines <- strings.TrimSpace(scanner.Text()):
			default:
			}
		}
		close(stub.lines)
	}()
	session := audio.AttachVLC(client, speed)
	t.Cleanup(func() { session.Close(); _ = server.Close() })
	return session, stub
}

// next is the next command line the session sent.
func (s *vlcStub) next() string {
	s.t.Helper()
	select {
	case line, ok := <-s.lines:
		if !ok {
			s.t.Fatal("the session closed its side of the socket")
		}
		return line
	case <-time.After(10 * time.Second):
		s.t.Fatal("the session sent nothing")
		return ""
	}
}

// waitFor reads until a command with prefix arrives, skipping the background polling.
func (s *vlcStub) waitFor(prefix string) string {
	s.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				s.t.Fatalf("socket closed while waiting for %q", prefix)
			}
			if strings.HasPrefix(line, prefix) {
				return line
			}
		case <-deadline:
			s.t.Fatalf("never sent %q", prefix)
			return ""
		}
	}
}

// answer replies the way VLC does: the value alone, with its prompt in front.
func (s *vlcStub) answer(values ...string) {
	s.t.Helper()
	for _, v := range values {
		if _, err := s.conn.Write([]byte("> " + v + "\n")); err != nil {
			s.t.Fatalf("stub could not answer: %v", err)
		}
	}
}

// VLC is polled, and its unnamed answers are matched to questions in order.
func TestVLCPollsForPositionAndMatchesAnswersToQuestions(t *testing.T) {
	t.Parallel()

	session, stub := newVLCStub(t, 1.0)
	if got := stub.waitFor("get_time"); got != "get_time" {
		t.Fatalf("first query = %q", got)
	}
	if got := stub.next(); got != "get_length" {
		t.Fatalf("second query = %q, want get_length", got)
	}
	if got := stub.next(); got != "is_playing" {
		t.Fatalf("third query = %q, want is_playing", got)
	}
	stub.answer("42", "180", "1")

	deadline := time.After(2 * time.Second)
	for {
		st := session.State()
		if st.Position == 42*time.Second && st.Duration == 180*time.Second {
			if st.Paused {
				t.Error("a playing note reads as paused")
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("state = %+v, want the answers folded in", st)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// VLC's seek is absolute, computed from the last reported position.
func TestVLCSeeksByComputingTheTarget(t *testing.T) {
	t.Parallel()

	session, stub := newVLCStub(t, 1.0)
	stub.waitFor("get_time")
	stub.next()
	stub.next()
	stub.answer("60", "180", "1")
	waitUntil(t, func() bool { return session.State().Position == 60*time.Second })

	if err := session.Seek(-10 * time.Second); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if got := stub.waitFor("seek"); got != "seek 50" {
		t.Errorf("sent %q, want an absolute seek to 50", got)
	}
	if err := session.Seek(-5 * time.Minute); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if got := stub.waitFor("seek"); got != "seek 0" {
		t.Errorf("sent %q, want the seek clamped at the beginning", got)
	}
}

// The controls speak VLC's own words, not mpv's.
func TestVLCControlsUseItsOwnCommands(t *testing.T) {
	t.Parallel()

	session, stub := newVLCStub(t, 1.0)
	if err := session.TogglePause(); err != nil {
		t.Fatalf("TogglePause: %v", err)
	}
	if got := stub.waitFor("pause"); got != "pause" {
		t.Errorf("sent %q, want VLC's own toggle", got)
	}
	if err := session.SetSpeed(1.5); err != nil {
		t.Fatalf("SetSpeed: %v", err)
	}
	if got := stub.waitFor("rate"); !strings.HasPrefix(got, "rate 1.5") {
		t.Errorf("sent %q, want a rate command", got)
	}
	if speed := session.State().Speed; speed != 1.5 {
		t.Errorf("speed = %v, want the bar to show what was asked for", speed)
	}
}

// Stopped is not finished: a note paused in the middle must not read as done.
func TestVLCTellsStoppedFromFinished(t *testing.T) {
	t.Parallel()

	session, stub := newVLCStub(t, 1.0)
	stub.waitFor("get_time")
	stub.next()
	stub.next()
	stub.answer("30", "180", "0") // stopped, but a long way from the end
	waitUntil(t, func() bool { return session.State().Position == 30*time.Second })
	if session.State().Done {
		t.Error("a note paused in the middle reads as finished")
	}

	stub.waitFor("get_time")
	stub.next()
	stub.next()
	stub.answer("180", "180", "0") // stopped at the end
	waitUntil(t, func() bool { return session.State().Done })
}

func TestTheProgramNameDecidesTheProtocol(t *testing.T) {
	t.Parallel()

	for program, want := range map[string]string{
		"mpv": "mpv", "/usr/bin/mpv": "mpv", "mpv-wrapper": "mpv",
		"vlc": "vlc", "/usr/bin/vlc": "vlc", "cvlc": "vlc", "nvlc": "vlc",
		"ffplay": "mpv", // unknown: assume the one whose flags it would understand
	} {
		if got := audio.Dialect(program); got != want {
			t.Errorf("Dialect(%q) = %q, want %q", program, got, want)
		}
	}
}

func waitUntil(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !done() {
		select {
		case <-deadline:
			t.Fatal("the state never arrived")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
