package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/audio"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// fakeSession stands in for a running player.
type fakeSession struct {
	mu     sync.Mutex
	st     audio.State
	seeks  []time.Duration
	speeds []float64
	toggle int
	closed int
}

func (f *fakeSession) State() audio.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeSession) TogglePause() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toggle++
	f.st.Paused = !f.st.Paused
	return nil
}

func (f *fakeSession) Seek(d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seeks = append(f.seeks, d)
	return nil
}

func (f *fakeSession) SetSpeed(s float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.speeds = append(f.speeds, s)
	f.st.Speed = s
	return nil
}

func (f *fakeSession) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
}

// finish is the note reaching its end (mpv --keep-open keeps the session).
func (f *fakeSession) finish() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.Done = true
}

func (f *fakeSession) setPaused(paused bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st.Paused = paused
}

func voice(id domain.EventID) domain.Message {
	return domain.Message{
		ID: id, RoomID: "!a:x", Sender: "@dana:x", Timestamp: at(1),
		Media: &domain.Media{Type: domain.MediaAudio, Name: "note.ogg", Mime: "audio/ogg", Size: 12345},
	}
}

// playing is a model with a voice note loaded and a fake player behind it.
func playing(t *testing.T, disp config.Display) (Model, *fakeSession) {
	t.Helper()

	m := sized(t, withRooms(t, starterNew(apitest.Nop{}, disp)))
	m.focus = paneTimeline
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{voice("$v")}}})
	session := &fakeSession{st: audio.State{Duration: 34 * time.Second, Position: 5 * time.Second, Speed: 1}}
	m.player = playerState{
		event:      "$v",
		title:      "note.ogg",
		place:      m.placeOf(voice("$v")),
		configured: disp.Media.Audio.PlaySpeed(),
		speed:      disp.Media.Audio.PlaySpeed(),
		session:    session,
	}
	return m, session
}

func TestPlayNeedsAVoiceNote(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.focus = paneTimeline
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{{ID: "$t", RoomID: "!a:x", Sender: "@a:x", Body: "hello", Timestamp: at(1)}},
	}})
	m, _ = press(t, m, keyCode('p'))
	if m.player.active() {
		t.Error("a text message should not open a player")
	}
	if !strings.Contains(m.status(), "nothing to play here") {
		t.Errorf("status = %q, want it to say there is nothing to play here", m.status())
	}
}

// Space plays and pauses from whichever pane has the keyboard.
func TestPlayerKeysWorkFromEveryPane(t *testing.T) {
	t.Parallel()

	for name, focus := range map[string]pane{"rail": paneRail, "rooms": paneRooms, "timeline": paneTimeline} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m, session := playing(t, config.Display{})
			m.focus = focus
			pressedThrough(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
			if session.toggle != 1 {
				t.Errorf("space in the %s pane toggled %d times, want 1", name, session.toggle)
			}
		})
	}
}

func TestSkipKeysMoveByTheConfiguredStep(t *testing.T) {
	t.Parallel()

	m, session := playing(t, config.Display{Media: config.Media{Audio: config.Audio{Skip: 15}}})
	m = pressedThrough(t, m, keyCode('['))
	pressedThrough(t, m, keyCode(']'))
	want := []time.Duration{-15 * time.Second, 15 * time.Second}
	if len(session.seeks) != 2 || session.seeks[0] != want[0] || session.seeks[1] != want[1] {
		t.Errorf("seeks = %v, want %v", session.seeks, want)
	}
}

// The speed keys step a quarter at a time and clamp silently.
func TestSpeedKeysStepAndClamp(t *testing.T) {
	t.Parallel()

	m, session := playing(t, config.Display{})
	for range 3 {
		m = pressedThrough(t, m, keyCode('}'))
	}
	if m.player.speed != 1.75 {
		t.Errorf("speed = %v, want three quarter-steps up from 1", m.player.speed)
	}
	for range 20 {
		m = pressedThrough(t, m, keyCode('}'))
	}
	if m.player.speed != audio.MaxSpeed {
		t.Errorf("speed = %v, want it clamped to %v", m.player.speed, audio.MaxSpeed)
	}
	for range 40 {
		m = pressedThrough(t, m, keyCode('{'))
	}
	if m.player.speed != audio.MinSpeed {
		t.Errorf("speed = %v, want it clamped to %v", m.player.speed, audio.MinSpeed)
	}
	if last := session.speeds[len(session.speeds)-1]; last != audio.MinSpeed {
		t.Errorf("the player was last told %v, want %v", last, audio.MinSpeed)
	}
}

// "Normal speed" is the speed configured for this place, not 1.
func TestNormalSpeedReturnsToTheConfiguredOne(t *testing.T) {
	t.Parallel()

	m, _ := playing(t, config.Display{Media: config.Media{Audio: config.Audio{Speed: 1.5}}})
	m, _ = press(t, m, keyCode('}'))
	m, _ = press(t, m, keyCode('='))
	if m.player.speed != 1.5 {
		t.Errorf("speed = %v, want the configured 1.5", m.player.speed)
	}
}

func TestStopClosesThePlayer(t *testing.T) {
	t.Parallel()

	m, session := playing(t, config.Display{})
	m, _ = press(t, m, keyCode('P'))
	if m.player.active() {
		t.Error("the bar is still up after stopping")
	}
	if session.closed != 1 {
		t.Errorf("the session was closed %d times, want 1", session.closed)
	}
}

// While nothing is loaded the player's keys are the panes' own again.
func TestPlayerKeysAreGivenBack(t *testing.T) {
	t.Parallel()

	m, _ := playing(t, config.Display{})
	m.focus = paneRail
	if act := m.keys.lookup("S", m.withPlayerScope(scopeRail, scopeNav, scopeCommand)...); act != actShowHidden {
		t.Fatalf("the rail's own S should still work while a note plays, got %v", act)
	}
	m, _ = press(t, m, keyCode('P'))
	if act := m.keys.lookup("space", m.withPlayerScope(scopeRail, scopeNav, scopeCommand)...); act == actPlayPause {
		t.Error("space still means play/pause with no note loaded")
	}
}

// The player's keys must never reach the composer.
func TestPlayerKeysDoNotReachTheComposer(t *testing.T) {
	t.Parallel()

	m, session := playing(t, config.Display{})
	m.focus, m.compose.insertMode = paneTimeline, true
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if session.toggle != 0 {
		t.Error("space while composing reached the player")
	}
	if m.compose.input != " " {
		t.Errorf("input = %q, want the space to have been typed", m.compose.input)
	}
}

// The play key on the loaded note toggles it rather than spawning a second player.
func TestPlayingTheSameNoteAgainIsPlayPause(t *testing.T) {
	t.Parallel()

	m, session := playing(t, config.Display{})
	m.focus = paneTimeline
	m = pressedThrough(t, m, keyCode('p'))
	if session.toggle != 1 {
		t.Errorf("toggled %d times, want the loaded note to play/pause", session.toggle)
	}
	if session.closed != 0 {
		t.Error("the running player was closed and started again")
	}
}

// A player arriving for a note no longer loaded lost a race; it is not shown.
func TestAStalePlayerIsClosedRatherThanShown(t *testing.T) {
	t.Parallel()

	m, session := playing(t, config.Display{})
	m = update(t, m, audioReadyMsg{event: "$other"})
	if m.player.session != audioSession(session) {
		t.Error("a player for another note replaced the loaded one")
	}
}

func TestPlayerBarSaysWhereItIsUpTo(t *testing.T) {
	t.Parallel()

	m, _ := playing(t, config.Display{Media: config.Media{Audio: config.Audio{Speed: 1.5}}})
	bar := m.renderPlayer(m.width)
	for _, want := range []string{"note.ogg", "0:05", "0:34", "1.5×"} {
		if !strings.Contains(bar, want) {
			t.Errorf("the bar does not mention %q:\n%s", want, bar)
		}
	}
	if !strings.Contains(stripStyles(m.View().Content), "note.ogg") {
		t.Error("the bar is not in the frame")
	}
}

// A note of unknown length gets an empty track.
func TestProgressBarWithNoDurationClaimsNothing(t *testing.T) {
	t.Parallel()

	got := progressBar(audio.State{Position: 5 * time.Second}, 10)
	if !strings.HasPrefix(got, "●") {
		t.Errorf("progress = %q, want the head at the start while the length is unknown", got)
	}
}

// The bar takes a row from the panes; the frame height never changes.
func TestTheBarTakesARowFromThePanes(t *testing.T) {
	t.Parallel()

	quiet := sized(t, withRooms(t, newModel()))
	loud, _ := playing(t, config.Display{})
	before := strings.Split(stripStyles(quiet.View().Content), "\n")
	after := strings.Split(stripStyles(loud.View().Content), "\n")
	if len(after) != len(before) {
		t.Fatalf("the frame is %d rows with a player and %d without; it must not change height",
			len(after), len(before))
	}
	if len(after) != loud.height {
		t.Fatalf("the frame is %d rows, want the terminal's %d", len(after), loud.height)
	}
	if row := after[len(after)-2]; !strings.Contains(row, "note.ogg") {
		t.Errorf("the row above the status line is %q, want the player bar", row)
	}
}

func TestAudioTitle(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		"":              "voice message",
		"audio":         "voice message",
		"Voice message": "voice message",
		"note.ogg":      "note.ogg",
	} {
		msg := voice("$v")
		msg.Media.Name = name
		if got := audioTitle(msg); got != want {
			t.Errorf("audioTitle(%q) = %q, want %q", name, got, want)
		}
	}
}

// Remembering a speed offers places narrowest first, ending with "everywhere".
func TestRememberingASpeedOffersTheUsualScopes(t *testing.T) {
	t.Parallel()

	m, _ := playing(t, config.Display{})
	m, _ = press(t, m, keyCode('w'))
	if m.picker.kind != pickerSpeedScope {
		t.Fatalf("picker = %v, want the speed scopes", m.picker.kind)
	}
	got := make([]string, 0, len(m.choosing.speedScopes))
	for _, scope := range m.choosing.speedScopes {
		got = append(got, scope.what)
	}
	want := []string{"dana in Alpha", "dana, anywhere", "everything in Alpha", "everywhere"}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("scopes = %v, want %v", got, want)
	}
}

// Choosing a place writes a rule the running client picks up at once.
func TestRememberingASpeedWritesARule(t *testing.T) {
	t.Parallel()

	m, _ := playing(t, config.Display{})
	m, _ = press(t, m, keyCode('}')) // 1.25×
	m, _ = press(t, m, keyCode('w'))
	m = choose(t, m, "everything in Alpha")

	rules := m.conf.base.Display.Media.Rules
	if len(rules) != 1 || rules[0].Match != "!a:x" || rules[0].Sender != "" {
		t.Fatalf("rules = %+v, want one naming the room", rules)
	}
	if rules[0].Speed == nil || *rules[0].Speed != 1.25 {
		t.Fatalf("rule speed = %v, want 1.25", rules[0].Speed)
	}
	if got := m.mediaPolicy(voice("$v")).Speed; got != 1.25 {
		t.Errorf("the running client resolves %v, want the rule it just wrote", got)
	}
}

// "Everywhere" writes the base setting, not a rule.
func TestRememberingASpeedEverywhereIsNotARule(t *testing.T) {
	t.Parallel()

	m, _ := playing(t, config.Display{})
	m, _ = press(t, m, keyCode('}'))
	m, _ = press(t, m, keyCode('w'))
	m = choose(t, m, "everywhere")

	if len(m.conf.base.Display.Media.Rules) != 0 {
		t.Errorf("rules = %+v, want none", m.conf.base.Display.Media.Rules)
	}
	if got := m.conf.base.Display.Media.Audio.Speed; got != 1.25 {
		t.Errorf("the outer speed = %v, want 1.25", got)
	}
}

// Setting a speed twice for the same place edits the rule rather than adding one.
func TestRememberingASpeedTwiceEditsTheRule(t *testing.T) {
	t.Parallel()

	m, _ := playing(t, config.Display{})
	for _, faster := range []int{1, 2} {
		for range faster {
			m, _ = press(t, m, keyCode('}'))
		}
		m, _ = press(t, m, keyCode('w'))
		m = choose(t, m, "everything in Alpha")
	}
	if rules := m.conf.base.Display.Media.Rules; len(rules) != 1 {
		t.Errorf("rules = %+v, want the one rule edited", rules)
	}
}

// choose accepts the named row of the open picker.
func choose(t *testing.T, m Model, label string) Model {
	t.Helper()

	for i, item := range m.picker.items {
		if item.label == label {
			m.picker.cursor = i
			next, _ := m.acceptPick()
			return next
		}
	}
	t.Fatalf("no %q among %d rows", label, len(m.picker.items))
	return m
}

// The play key is advertised only on a voice note.
func TestThePlayKeyIsAdvertisedOnlyOnAVoiceNote(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.focus = paneTimeline
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$t", RoomID: "!a:x", Sender: "@a:x", Body: "hello", Timestamp: at(1)},
		voice("$v"),
	}}})
	m.timeline.selected = "$t"
	if strings.Contains(m.timelineHints(), "play") {
		t.Error("the play key is offered on a message with nothing to play")
	}
	m.timeline.selected = "$v"
	if !strings.Contains(m.timelineHints(), "play") {
		t.Errorf("the play key is not offered on a voice note: %s", m.timelineHints())
	}
}

// As the terminal narrows the legend goes first, then the progress bar; the clock
// and speed never go.
func TestTheBarGivesUpItsLegendBeforeItsProgress(t *testing.T) {
	t.Parallel()

	m, session := playing(t, config.Display{})
	session.st = audio.State{Position: 30 * time.Second, Duration: 60 * time.Second, Speed: 1}

	wide := m.renderPlayer(140)
	if !strings.Contains(wide, "remember") || !strings.Contains(wide, "●") {
		t.Errorf("a wide terminal should get the whole legend and a progress bar:\n%s", wide)
	}
	middling := m.renderPlayer(90)
	if strings.Contains(middling, "remember") {
		t.Errorf("the long legend should have given way first:\n%s", middling)
	}
	if !strings.Contains(middling, "●") || !strings.Contains(middling, "stop") {
		t.Errorf("the progress bar and the short legend should both fit at 90:\n%s", middling)
	}
	narrow := m.renderPlayer(38)
	if strings.Contains(narrow, "●") {
		t.Errorf("a progress bar too small to say anything should be dropped:\n%s", narrow)
	}
	for _, want := range []string{"0:30", "1:00", "1×"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("the narrow bar lost %q, which it must never do:\n%s", want, narrow)
		}
	}
}

// A long name is cut rather than crowding out the clock.
func TestALongNameDoesNotCrowdOutTheClock(t *testing.T) {
	t.Parallel()

	m, session := playing(t, config.Display{})
	session.st = audio.State{Position: 30 * time.Second, Duration: 60 * time.Second, Speed: 1}
	m.player.title = strings.Repeat("long-", 20) + "name.ogg"
	bar := m.renderPlayer(100)
	if !strings.Contains(bar, "0:30 / 1:00") {
		t.Errorf("the clock was crowded out:\n%s", bar)
	}
	if !strings.Contains(bar, "…") {
		t.Errorf("the name should have been cut short:\n%s", bar)
	}
}

// A note that could not be played takes the bar away and says why.
func TestAFailedNoteClosesTheBar(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m.player = playerState{event: "$v", title: "note.ogg", loading: true}
	m = update(t, m, audioReadyMsg{event: "$v", err: errNoAudio})
	if m.player.active() {
		t.Error("the bar is still up after a failure")
	}
	if !strings.Contains(m.status(), "could not play") {
		t.Errorf("status = %q, want it to say the note could not be played", m.status())
	}
}

func TestAMissingPlayerSaysWhatToInstall(t *testing.T) {
	t.Parallel()

	_, err := audio.Resolve([]string{"kith-no-such-player"})
	if err == nil {
		t.Fatal("a player that is not installed should be an error")
	}
	if advice := playerAdvice(err); !strings.Contains(advice, "install mpv") {
		t.Errorf("advice = %q, want it to name what to install", advice)
	}
}

// The redraw pulse re-arms only while the bar can change: not at the end (the
// session survives via --keep-open), not paused, not once stopped.
func TestPulseRearmsOnlyWhileMoving(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		setup func(Model, *fakeSession) Model
		want  bool
	}{
		{"playing", func(m Model, _ *fakeSession) Model { return m }, true},
		{"ended", func(m Model, s *fakeSession) Model { s.finish(); return m }, false},
		{"paused", func(m Model, s *fakeSession) Model { s.setPaused(true); return m }, false},
		{"stopped", func(m Model, _ *fakeSession) Model { m, _ = m.stopPlaying(); return m }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, session := playing(t, config.Display{})
			m.player.pulsing = true
			m = update(t, tc.setup(m, session), playerTickMsg{})
			if m.player.pulsing != tc.want {
				t.Errorf("pulsing = %v, want %v", m.player.pulsing, tc.want)
			}
		})
	}
}

// Once the pulse has stopped, the controls start it again.
func TestAControlRestartsAStoppedPulse(t *testing.T) {
	t.Parallel()

	for name, control := range map[string]func(Model) (Model, tea.Cmd){
		"play/pause": func(m Model) (Model, tea.Cmd) { return m.togglePlay() },
		"seek":       func(m Model) (Model, tea.Cmd) { return m.seekAudio(10 * time.Second) },
		"speed":      func(m Model) (Model, tea.Cmd) { return m.setSpeed(1.5) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m, session := playing(t, config.Display{})
			session.finish()
			m = update(t, m, playerTickMsg{})
			if m.player.pulsing {
				t.Fatal("the pulse did not stop on a finished note")
			}

			m, cmd := control(m)
			if !m.player.pulsing {
				t.Error("the control did not restart the pulse")
			}
			if cmd == nil {
				t.Error("the control returned no tick to restart it with")
			}
		})
	}
}

// A control buys pulseGrace ticks before the session's (lagging) state is trusted.
func TestAControlBuysTheBarAGracePeriod(t *testing.T) {
	t.Parallel()

	m, session := playing(t, config.Display{})
	session.finish()
	m, cmd := m.togglePlay()
	if cmd == nil {
		t.Fatal("the control did not start the pulse")
	}
	for i := range pulseGrace {
		m = update(t, m, playerTickMsg{})
		if !m.player.pulsing {
			t.Fatalf("the pulse stopped after %d of %d grace ticks", i+1, pulseGrace)
		}
	}
	m = update(t, m, playerTickMsg{})
	if m.player.pulsing {
		t.Error("the pulse outlived its grace on a note that reports nothing moving")
	}
}

// One tick chain, however many times it is armed.
func TestArmingAnAlreadyRunningPulseAddsNothing(t *testing.T) {
	t.Parallel()

	m, _ := playing(t, config.Display{})
	m, first := m.armPulse()
	if first == nil {
		t.Fatal("the first arm returned no tick")
	}
	if _, second := m.armPulse(); second != nil {
		t.Error("a second tick was armed while a chain was already running")
	}
}

// Stopping or failing to open a note carries the in-flight tick across, or the next
// play would start a second chain.
func TestSwappingTheNoteKeepsTheTickAccountedFor(t *testing.T) {
	t.Parallel()

	m, _ := playing(t, config.Display{})
	m.player.pulsing = true
	stopped, _ := m.stopPlaying()
	if !stopped.player.pulsing {
		t.Error("stopping the player forgot the tick that was in flight")
	}
	failed := update(t, m, audioReadyMsg{event: "$v", err: errNoAudio})
	if !failed.player.pulsing {
		t.Error("a player that failed to open forgot the tick that was in flight")
	}
}

// pulseOnce drives one pulse and reports the interval the next one was armed at, and
// whether one was: the pulse re-arms one timer and keeps the chain marked running.
func pulseOnce(t *testing.T, m Model) (Model, time.Duration, bool) {
	t.Helper()
	wait := m.pulseInterval() // what the tick arms is decided before it
	next, cmd := asModel(m.Update(playerTickMsg{}))
	return next, wait, timers(t, cmd) == 1 && next.player.pulsing
}

func noted(t *testing.T, length time.Duration, speed float64) (Model, *fakeSession) {
	t.Helper()
	m, session := playing(t, config.Display{})
	session.mu.Lock()
	session.st = audio.State{Duration: length, Position: length / 2, Speed: speed}
	session.mu.Unlock()
	return m, session
}

// The pulse rate is the faster of the clock and the progress head, over the speed,
// clamped to [playerPulseMin, playerPulseMax].
func TestPulseRateFollowsTheNote(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		length time.Duration
		speed  float64
		want   time.Duration
	}{
		"an hour is capped at the ceiling":  {time.Hour, 1, playerPulseMax},
		"three minutes still hits it":       {3 * time.Minute, 1, playerPulseMax},
		"two seconds needs the floor":       {2 * time.Second, 1, playerPulseMin},
		"speed brings the ceiling down":     {time.Hour, 4, playerPulseMin},
		"a slow speed cannot exceed either": {time.Hour, 0.25, playerPulseMax},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m, _ := noted(t, tc.length, tc.speed)
			m.player.pulsing = true
			_, after, ok := pulseOnce(t, m)
			if !ok {
				t.Fatal("the pulse did not re-arm on a playing note")
			}
			if after != tc.want {
				t.Errorf("armed at %v, want %v", after, tc.want)
			}
		})
	}
}

// A faster speed never redraws less often.
func TestPulseRateIsMonotonicInSpeed(t *testing.T) {
	t.Parallel()

	var last time.Duration
	for _, speed := range []float64{0.5, 1, 2, 4} {
		m, _ := noted(t, 90*time.Second, speed)
		m.player.pulsing = true
		_, after, ok := pulseOnce(t, m)
		if !ok {
			t.Fatalf("no pulse armed at %g×", speed)
		}
		if last != 0 && after > last {
			t.Errorf("at %g× armed at %v, slower than the %v of the speed below it", speed, after, last)
		}
		last = after
	}
}

// Inside the grace window the rate is the floor whatever the note.
func TestGraceTicksUseTheFloorRate(t *testing.T) {
	t.Parallel()

	m, _ := noted(t, time.Hour, 1)
	m, _ = m.togglePlay()
	_, after, ok := pulseOnce(t, m)
	if !ok {
		t.Fatal("the control did not arm the pulse")
	}
	if after != playerPulseMin {
		t.Errorf("a grace tick armed at %v, want the floor %v", after, playerPulseMin)
	}
}

func closeAfter(seconds int) config.Display {
	return config.Display{Media: config.Media{Audio: config.Audio{CloseAfter: &seconds}}}
}

// finished drives a playing note to its end and returns the tick's result.
func finished(t *testing.T, disp config.Display) (Model, tea.Cmd) {
	t.Helper()
	m, session := playing(t, disp)
	m.player.pulsing = true
	session.finish()
	return asModel(m.Update(playerTickMsg{}))
}

// A finished note removes its bar after close_after (default when unset); 0 closes
// at once and -1 never.
func TestAFinishedNoteClosesPerConfig(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		disp   config.Display
		after  time.Duration // 0: no timer armed
		active bool
	}{
		{"configured", closeAfter(15), 15 * time.Second, true},
		{"default", config.Display{}, config.DefaultCloseAfterSeconds * time.Second, true},
		{"zero closes at once", closeAfter(0), 0, false},
		{"negative never closes", closeAfter(-1), 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, cmd := finished(t, tc.disp)
			if m.player.active() != tc.active {
				t.Errorf("active = %v, want %v", m.player.active(), tc.active)
			}
			if got := m.prefs.display.Media.Audio.CloseDelay(); tc.after != 0 && got != tc.after {
				t.Fatalf("close delay %v, want %v", got, tc.after)
			}
			armed := timers(t, cmd) == 1
			if armed != (tc.after != 0) {
				t.Fatalf("close armed = %v, want %v", armed, tc.after != 0)
			}
			if armed {
				if closed := update(t, m, playerCloseMsg{event: "$v"}); closed.player.active() {
					t.Error("the armed close for the note that ended left its bar up")
				}
			}
		})
	}
}

// A paused note does not arm the close.
func TestAPausedNoteDoesNotArmTheClose(t *testing.T) {
	t.Parallel()

	m, session := playing(t, closeAfter(15))
	m.player.pulsing = true
	session.setPaused(true)
	_, cmd := m.Update(playerTickMsg{})
	if timers(t, cmd) != 0 {
		t.Error("a paused note armed the close")
	}
}

// A close for a note that was replaced or restarted is ignored.
func TestAStaleCloseIsIgnored(t *testing.T) {
	t.Parallel()

	t.Run("the note was replaced", func(t *testing.T) {
		t.Parallel()

		m, session := playing(t, closeAfter(15))
		session.finish()
		next, _ := m.handlePlayerClose(playerCloseMsg{event: "$other"})
		if !next.player.active() {
			t.Error("a close armed for another note took this one away")
		}
	})

	t.Run("the note was started again", func(t *testing.T) {
		t.Parallel()

		m, _ := playing(t, closeAfter(15)) // still playing, never finished
		next, _ := m.handlePlayerClose(playerCloseMsg{event: "$v"})
		if !next.player.active() {
			t.Error("a close took away a note that was playing again")
		}
	})

	t.Run("the note really did end", func(t *testing.T) {
		t.Parallel()

		m, session := playing(t, closeAfter(15))
		session.finish()
		next, _ := m.handlePlayerClose(playerCloseMsg{event: "$v"})
		if next.player.active() {
			t.Error("the bar stayed up after its close arrived")
		}
		if session.closed != 1 {
			t.Errorf("session closed %d times, want the player released exactly once", session.closed)
		}
	})
}

// pressedThrough is m after key, with what it asks the player to do done: controls go
// to the player off the event loop.
func pressedThrough(t *testing.T, m Model, key tea.KeyPressMsg) Model {
	t.Helper()
	next, cmd := press(t, m, key)
	return deliver(t, next, cmd)
}

// hungSession is a player whose control socket takes a while to accept a write, as a
// hung mpv does until the write times out.
type hungSession struct {
	*fakeSession
	wait time.Duration
}

func (h hungSession) TogglePause() error {
	time.Sleep(h.wait)
	return h.fakeSession.TogglePause()
}

func (h hungSession) Seek(d time.Duration) error {
	time.Sleep(h.wait)
	return h.fakeSession.Seek(d)
}

func (h hungSession) SetSpeed(f float64) error {
	time.Sleep(h.wait)
	return h.fakeSession.SetSpeed(f)
}

// The player's controls reach it off the event loop: a player that does not answer
// does not freeze the screen for as long as its write takes.
func TestAHungPlayerDoesNotHoldUpTheKeys(t *testing.T) {
	t.Parallel()
	m, session := playing(t, config.Display{})
	const wait = 300 * time.Millisecond
	m.player.session = hungSession{fakeSession: session, wait: wait}
	for _, key := range []tea.KeyPressMsg{keyText(" "), keyCode(']'), keyCode('}')} {
		start := time.Now()
		_, _ = m.Update(key)
		if took := time.Since(start); took >= wait {
			t.Errorf("%q held Update for %v on the player's socket", key.String(), took.Round(time.Millisecond))
		}
	}
}
