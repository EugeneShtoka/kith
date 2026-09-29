package audio_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/audio"
)

// stub plays mpv's end of the control socket.
type stub struct {
	t     *testing.T
	conn  net.Conn
	lines chan string
}

// newStub wires a session to a stub; the reader starts first because net.Pipe is
// unbuffered.
func newStub(t *testing.T, speed float64) (*audio.Session, *stub) {
	t.Helper()
	client, server := net.Pipe()
	s := &stub{t: t, conn: server, lines: make(chan string, 32)}
	go func() {
		scanner := bufio.NewScanner(server)
		for scanner.Scan() {
			select {
			case s.lines <- scanner.Text():
			default:
			}
		}
		close(s.lines)
	}()
	session := audio.Attach(client, speed)
	t.Cleanup(func() { session.Close(); _ = server.Close() })
	return session, s
}

// next is the next command the session sent, decoded.
func (s *stub) next() []any {
	s.t.Helper()
	select {
	case line, ok := <-s.lines:
		if !ok {
			s.t.Fatal("the session closed its side of the socket")
		}
		var sent struct {
			Command []any `json:"command"`
		}
		if err := json.Unmarshal([]byte(line), &sent); err != nil {
			s.t.Fatalf("session sent %q, which is not a command: %v", line, err)
		}
		return sent.Command
	case <-time.After(10 * time.Second):
		s.t.Fatal("the session sent nothing")
		return nil
	}
}

// push sends one property-change, the way mpv reports a value it was asked to watch.
func (s *stub) push(name string, data any) {
	s.t.Helper()
	line, err := json.Marshal(map[string]any{"event": "property-change", "name": name, "data": data})
	if err != nil {
		s.t.Fatalf("encode %s: %v", name, err)
	}
	if _, err := s.conn.Write(append(line, '\n')); err != nil {
		s.t.Fatalf("push %s: %v", name, err)
	}
}

// skipObservations consumes the observe_property calls a new session opens with.
func (s *stub) skipObservations() {
	s.t.Helper()
	for range 5 {
		if cmd := s.next(); cmd[0] != "observe_property" {
			s.t.Fatalf("a new session should only observe first, got %v", cmd)
		}
	}
}

// eventually waits for ok to hold on the session's state.
func eventually(t *testing.T, what string, ok func(audio.State) bool, s *audio.Session) audio.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := s.State(); ok(st) {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (state: %+v)", what, s.State())
	return audio.State{}
}

// A session observes exactly the values the bar draws.
func TestSessionObservesWhatTheBarDraws(t *testing.T) {
	t.Parallel()

	_, player := newStub(t, 1)
	want := []string{"time-pos", "duration", "pause", "speed", "eof-reached"}
	for i, property := range want {
		cmd := player.next()
		if len(cmd) != 3 || cmd[0] != "observe_property" || cmd[2] != property {
			t.Fatalf("observation %d: got %v, want observe_property for %q", i, cmd, property)
		}
	}
}

// What mpv reports becomes what State says.
func TestSessionFoldsWhatThePlayerReports(t *testing.T) {
	t.Parallel()

	session, player := newStub(t, 1)
	player.skipObservations()

	player.push("duration", 34.5)
	player.push("time-pos", 12.25)
	player.push("speed", 1.5)
	player.push("pause", true)

	st := eventually(t, "the reported state", func(s audio.State) bool {
		return s.Duration > 0 && s.Position > 0 && s.Paused
	}, session)
	if got := st.Position.Round(time.Millisecond); got != 12250*time.Millisecond {
		t.Errorf("position %v, want 12.25s", got)
	}
	if got := st.Duration.Round(time.Millisecond); got != 34500*time.Millisecond {
		t.Errorf("duration %v, want 34.5s", got)
	}
	if st.Speed != 1.5 {
		t.Errorf("speed %v, want 1.5", st.Speed)
	}
}

// A null time-pos does not reset a known position to zero.
func TestUnknownPositionIsNotZero(t *testing.T) {
	t.Parallel()

	session, player := newStub(t, 1)
	player.skipObservations()

	player.push("time-pos", 9.0)
	eventually(t, "a position", func(s audio.State) bool { return s.Position > 0 }, session)
	player.push("time-pos", nil)
	player.push("speed", 2.0) // proves the null was read

	st := eventually(t, "the speed change", func(s audio.State) bool { return s.Speed == 2 }, session)
	if st.Position == 0 {
		t.Error("a null time-pos overwrote a known position")
	}
}

// A negative position (mpv while demuxing) reads as zero.
func TestNegativePositionReadsAsTheStart(t *testing.T) {
	t.Parallel()

	session, player := newStub(t, 1)
	player.skipObservations()

	player.push("duration", 10.0)
	player.push("time-pos", -0.25)
	st := eventually(t, "the duration", func(s audio.State) bool { return s.Duration > 0 }, session)
	if st.Position < 0 {
		t.Errorf("position %v, want no less than zero", st.Position)
	}
}

// Pressing play at the end replays from the start.
func TestPlayFromTheEndReplays(t *testing.T) {
	t.Parallel()

	session, player := newStub(t, 1)
	player.skipObservations()

	player.push("eof-reached", true)
	eventually(t, "the end of the file", func(s audio.State) bool { return s.Done }, session)

	go func() { _ = session.TogglePause() }()
	if cmd := player.next(); len(cmd) != 3 || cmd[0] != "seek" || cmd[2] != "absolute" {
		t.Fatalf("got %v, want a seek to the start", cmd)
	}
	if cmd := player.next(); len(cmd) != 3 || cmd[0] != "set_property" || cmd[1] != "pause" || cmd[2] != false {
		t.Fatalf("got %v, want the player unpaused", cmd)
	}
}

// Pause is sent as the opposite of what mpv last said, not as a blind toggle.
func TestPauseSendsTheOppositeOfTheKnownState(t *testing.T) {
	t.Parallel()

	session, player := newStub(t, 1)
	player.skipObservations()

	player.push("pause", true)
	eventually(t, "the paused state", func(s audio.State) bool { return s.Paused }, session)

	go func() { _ = session.TogglePause() }()
	if cmd := player.next(); cmd[2] != false {
		t.Fatalf("got %v, want pause set to false", cmd)
	}
}

// Seeking back from the end clears Done immediately.
func TestSeekingBackFromTheEndClearsIt(t *testing.T) {
	t.Parallel()

	session, player := newStub(t, 1)
	player.skipObservations()

	player.push("eof-reached", true)
	eventually(t, "the end of the file", func(s audio.State) bool { return s.Done }, session)

	go func() { _ = session.Seek(-10 * time.Second) }()
	if cmd := player.next(); cmd[0] != "seek" || cmd[2] != "relative" {
		t.Fatalf("got %v, want a relative seek", cmd)
	}
	if session.State().Done {
		t.Error("seeking back from the end should mean the file is not over")
	}
}

// Out-of-range speeds are clamped, not refused.
func TestSpeedIsClamped(t *testing.T) {
	t.Parallel()

	session, player := newStub(t, 1)
	player.skipObservations()

	go func() { _ = session.SetSpeed(99) }()
	if cmd := player.next(); cmd[2] != audio.MaxSpeed {
		t.Fatalf("got %v, want the speed clamped to %v", cmd, audio.MaxSpeed)
	}
	go func() { _ = session.SetSpeed(0) }()
	if cmd := player.next(); cmd[2] != audio.MinSpeed {
		t.Fatalf("got %v, want the speed clamped to %v", cmd, audio.MinSpeed)
	}
}

// Every control tolerates a nil session.
func TestNilSessionIsInert(t *testing.T) {
	t.Parallel()

	var none *audio.Session
	if st := none.State(); st != (audio.State{}) {
		t.Errorf("a nil session should have no state, got %+v", st)
	}
	for _, call := range []func() error{
		none.TogglePause,
		func() error { return none.Seek(time.Second) },
		func() error { return none.SetSpeed(2) },
	} {
		if err := call(); err != nil {
			t.Errorf("a control on a nil session should do nothing, got %v", err)
		}
	}
	none.Close()
}

// Closing twice is fine.
func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	session, player := newStub(t, 1)
	player.skipObservations()
	session.Close()
	session.Close()
	if !session.State().Done {
		t.Error("a closed session is not playing anything")
	}
}

// The clock a player draws.
func TestFormat(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00"},
		{-time.Second, "0:00"},
		{34 * time.Second, "0:34"},
		{95 * time.Second, "1:35"},
		{3723 * time.Second, "1:02:03"},
	} {
		if got := audio.Format(tc.in); got != tc.want {
			t.Errorf("Format(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A speed reads as a setting, not a measurement: "1.5×", never "1.50×".
func TestFormatSpeed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   float64
		want string
	}{
		{1, "1×"},
		{1.5, "1.5×"},
		{1.25, "1.25×"},
		{2, "2×"},
	} {
		if got := audio.FormatSpeed(tc.in); got != tc.want {
			t.Errorf("FormatSpeed(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The configured command is taken as written; an empty one is the default player.
func TestResolve(t *testing.T) {
	t.Parallel()

	got, err := audio.Resolve(nil)
	if err != nil {
		t.Skipf("no default player installed: %v", err)
	}
	if got[0] != "mpv" || len(got) < 2 {
		t.Errorf("Resolve(nil) = %v, want mpv and its flags", got)
	}
	if _, err := audio.Resolve([]string{"kith-no-such-player"}); err == nil {
		t.Error("a player that is not installed should be reported, not run")
	} else if !strings.Contains(err.Error(), "kith-no-such-player") {
		t.Errorf("the error should name the player, got %v", err)
	}
}

// eof-reached is followed both ways (mpv flips it while seeking near the end).
func TestTheEndIsFollowedInBothDirections(t *testing.T) {
	t.Parallel()

	session, player := newStub(t, 1)
	player.skipObservations()

	player.push("eof-reached", true)
	eventually(t, "the end of the file", func(s audio.State) bool { return s.Done }, session)
	player.push("eof-reached", false)
	eventually(t, "playback resuming", func(s audio.State) bool { return !s.Done }, session)
}

// A player that stops reading its socket fails the control after a bound, and the
// state stays readable meanwhile: the UI reads it every frame.
func TestAPlayerThatStopsReadingDoesNotHoldTheSession(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe() // unbuffered: once nothing reads server, writes block
	// Take what the session says as it attaches, then stop reading, as a hung player does.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_ = server.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, _ = io.Copy(io.Discard, server)
	}()
	session := audio.Attach(client, 1)
	<-drained
	t.Cleanup(func() { _ = server.Close(); session.Close() })

	sent := make(chan error, 1)
	go func() { sent <- session.TogglePause() }()
	time.Sleep(50 * time.Millisecond) // the write is now parked on the pipe

	read := make(chan struct{})
	go func() {
		_ = session.State()
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("State() blocked behind a control write the player never read")
	}
	select {
	case err := <-sent:
		if err == nil {
			t.Error("a control nobody read reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the control write never gave up")
	}
}

// After a control write gives up, the stream may hold half a command, and (for VLC) a
// question whose answer would be read as the next one's. The session ends there:
// it reports done, and the next control fails at once instead of waiting out another
// timeout.
func TestAFailedControlWriteEndsTheSession(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_ = server.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, _ = io.Copy(io.Discard, server)
	}()
	session := audio.Attach(client, 1)
	<-drained
	t.Cleanup(func() { _ = server.Close(); session.Close() })

	if err := session.TogglePause(); err == nil {
		t.Fatal("a control nobody read reported success")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !session.State().Done {
		if time.Now().After(deadline) {
			t.Fatal("the session goes on after a control write failed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	began := time.Now()
	if err := session.TogglePause(); err == nil {
		t.Error("a control after the session ended reported success")
	}
	if took := time.Since(began); took > 500*time.Millisecond {
		t.Errorf("the next control took %s; it should fail at once", took)
	}
}
