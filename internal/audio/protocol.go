package audio

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// protocol is one player's control language. mpv takes JSON and pushes every
// property change; VLC takes text lines, answers only what it is asked, and seeks
// absolutely — so the two share an interface, not an implementation.
type protocol interface {
	// flags start the player on socket at speed; Play appends the file.
	flags(socket string, speed float64) []string
	// begin runs once the connection is open.
	begin(s *Session)
	pause(s *Session) error
	seek(s *Session, d time.Duration) error
	speed(s *Session, f float64) error
	quit(s *Session) error
	// absorb folds one line from the player into the state.
	absorb(s *Session, line []byte)
	// poll is how often to ask for the position; zero for a player that pushes.
	poll() time.Duration
	ask(s *Session) error
}

// mpvIPC drives mpv over its JSON IPC socket.
type mpvIPC struct{}

func (mpvIPC) poll() time.Duration   { return 0 }
func (mpvIPC) ask(*Session) error    { return nil }
func (mpvIPC) quit(s *Session) error { return s.sendJSON("quit") }
func (mpvIPC) speed(s *Session, f float64) error {
	return s.sendJSON("set_property", "speed", clampSpeed(f))
}

func (mpvIPC) flags(socket string, speed float64) []string {
	return []string{
		"--input-ipc-server=" + socket,
		fmt.Sprintf("--speed=%.4f", clampSpeed(speed)),
	}
}

// begin observes the properties a bar draws, best effort.
func (mpvIPC) begin(s *Session) {
	for i, property := range observed {
		// A failed write means the socket is gone; the next control reports that.
		_ = s.sendJSON("observe_property", i+1, property)
	}
}

// pause toggles, and from the end replays from the start (mpv alone would sit there).
func (mpvIPC) pause(s *Session) error {
	st := s.State()
	if st.Done {
		if err := s.sendJSON("seek", 0, "absolute"); err != nil {
			return err
		}
		s.mu.Lock()
		s.st.Done, s.st.Position = false, 0
		s.mu.Unlock()
		return s.sendJSON("set_property", "pause", false)
	}
	return s.sendJSON("set_property", "pause", !st.Paused)
}

func (mpvIPC) seek(s *Session, d time.Duration) error {
	return s.sendJSON("seek", d.Seconds(), "relative")
}

// mpvEvent is the shape of everything mpv pushes; only property changes matter.
type mpvEvent struct {
	Event string          `json:"event"`
	Name  string          `json:"name"`
	Data  json.RawMessage `json:"data"`
}

// jsonValue decodes raw into a T, reporting false for null — which would otherwise
// unmarshal as the zero value and read as "position zero".
func jsonValue[T any](raw json.RawMessage) (T, bool) {
	var v *T
	if err := json.Unmarshal(raw, &v); err != nil || v == nil {
		var zero T
		return zero, false
	}
	return *v, true
}

func (mpvIPC) absorb(s *Session, line []byte) {
	var ev mpvEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Event != "property-change" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Name {
	case "time-pos":
		if secs, ok := jsonValue[float64](ev.Data); ok {
			s.st.Position = seconds(secs)
		}
	case "duration":
		if secs, ok := jsonValue[float64](ev.Data); ok {
			s.st.Duration = seconds(secs)
		}
	case "speed":
		if f, ok := jsonValue[float64](ev.Data); ok {
			s.st.Speed = f
		}
	case "pause":
		if b, ok := jsonValue[bool](ev.Data); ok {
			s.st.Paused = b
		}
	case "eof-reached":
		// Followed both ways: mpv flips it on briefly while seeking near the end.
		// A closed session stays done.
		if b, ok := jsonValue[bool](ev.Data); ok && !s.closed {
			s.st.Done = b
		}
	}
}

// vlcRC drives VLC's remote-control interface: text commands, unnamed numeric
// answers matched to questions by order, and absolute seeks.
type vlcRC struct{}

func (vlcRC) poll() time.Duration   { return 250 * time.Millisecond }
func (vlcRC) begin(*Session)        {}
func (vlcRC) quit(s *Session) error { return s.write("quit") }

func (vlcRC) flags(socket string, speed float64) []string {
	return []string{
		"--intf", "rc",
		"--rc-unix", socket,
		"--no-video",
		"--no-osd",
		"--quiet",
		fmt.Sprintf("--rate=%.4f", clampSpeed(speed)),
	}
}

// pause toggles, and from the end starts over, matching mpv.
func (vlcRC) pause(s *Session) error {
	if s.State().Done {
		if err := s.write("seek 0"); err != nil {
			return err
		}
		s.mu.Lock()
		s.st.Done, s.st.Position, s.st.Paused = false, 0, false
		s.mu.Unlock()
		return s.write("play")
	}
	return s.write("pause")
}

// seek turns a relative move into VLC's absolute one from the last reported
// position, clamped at zero (VLC rejects a negative seek).
func (vlcRC) seek(s *Session, d time.Duration) error {
	target := max(s.State().Position+d, 0)
	return s.write(fmt.Sprintf("seek %d", int(target.Seconds())))
}

// speed records the rate itself, since VLC never reports it.
func (vlcRC) speed(s *Session, f float64) error {
	speed := clampSpeed(f)
	s.mu.Lock()
	s.st.Speed = speed
	s.mu.Unlock()
	return s.write(fmt.Sprintf("rate %.4f", speed))
}

// vlcQueries are what one poll asks, in answer order.
var vlcQueries = []string{"get_time", "get_length", "is_playing"}

func (vlcRC) ask(s *Session) error {
	for _, query := range vlcQueries {
		// Queued before sending: the answer can be read before write returns.
		s.mu.Lock()
		s.pending = append(s.pending, query)
		s.mu.Unlock()
		if err := s.write(query); err != nil {
			return err
		}
	}
	return nil
}

// absorb reads one reply line, stripping VLC's "> " prompt and dropping anything
// that is not a number for an outstanding question.
func (vlcRC) absorb(s *Session, line []byte) {
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(line)), ">"))
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return // VLC's chatter (prompts, status lines) is not an answer: not an error
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return
	}
	query := s.pending[0]
	s.pending = s.pending[1:]
	switch query {
	case "get_time":
		s.st.Position = seconds(value)
	case "get_length":
		if value > 0 {
			s.st.Duration = seconds(value)
		}
	case "is_playing":
		// Not playing is paused unless it stopped at the end it reported.
		playing := value != 0
		s.st.Paused = !playing && !s.st.Done
		if !playing && s.st.Duration > 0 && s.st.Position >= s.st.Duration-time.Second && !s.closed {
			s.st.Done, s.st.Paused = true, false
		}
	}
}
