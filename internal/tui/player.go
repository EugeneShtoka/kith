package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/audio"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Voice-note playback: mpv plays headless (internal/audio) and the controls live
// in a bar that does not own the keyboard — its keys are a scope consulted ahead of
// the pane's while a note is loaded. `{`/`}` change this playback's speed only; `s`
// offers to remember it via the scope picker.

// playerState is the loaded voice note, if any.
type playerState struct {
	session audioSession
	// event is the message being played, so the play key again means "toggle".
	event domain.EventID
	title string
	// place is where the note was said, for the speed-scope picker (the cursor may
	// have moved since).
	place domain.DownloadPlace
	// configured is the place's configured speed, what `=` returns to.
	configured float64
	// speed is in force now, held ahead of the player's answer.
	speed float64
	// loading: the note's bytes are still being fetched.
	loading bool
	// pulsing: a redraw tick is in flight; ticks cannot be canceled, so only one
	// chain may run.
	pulsing bool
	// grace is how many ticks to redraw regardless of State(): mpv answers controls
	// over IPC, so State() lags a keypress briefly.
	grace int
}

// audioSession is the part of an audio.Session the model uses.
type audioSession interface {
	State() audio.State
	TogglePause() error
	Seek(time.Duration) error
	SetSpeed(float64) error
	Close()
}

func (p playerState) active() bool { return p.session != nil || p.loading }

// advancing reports whether the bar can still change on its own; a paused or
// finished (--keep-open) note is loaded but still.
func (p playerState) advancing() bool {
	if p.loading {
		return true
	}
	state, ok := p.state()
	return ok && !state.Done && !state.Paused
}

func (p playerState) state() (audio.State, bool) {
	if p.session == nil {
		return audio.State{}, false
	}
	return p.session.State(), true
}

// replace swaps in a new note, carrying over pulsing (the in-flight tick outlives
// the state it was armed for).
func (p playerState) replace(next playerState) playerState {
	next.pulsing = p.pulsing
	return next
}

// close releases the session; idempotent.
func (p playerState) close() {
	if p.session != nil {
		p.session.Close()
	}
}

// Redraw rate bounds while playing; the clock reads whole seconds, hence the max.
const (
	playerPulseMin = 250 * time.Millisecond
	playerPulseMax = time.Second
)

// pulseGrace is the ticks a control buys before State() is trusted (~1s).
const pulseGrace = 4

type playerTickMsg struct{}

// playerCloseMsg takes a finished note's bar away, if event is still the one loaded.
type playerCloseMsg struct{ event domain.EventID }

type audioReadyMsg struct {
	event   domain.EventID
	session *audio.Session
	err     error
}

// hasVoiceNote reports whether the selection is audio, which puts play in the legend.
func (m Model) hasVoiceNote() bool {
	msg, ok := m.selectedMessage()
	return ok && msg.Media.IsAudio()
}

// play opens the selection: a voice note in the bar, a video in an external player.
func (m Model) play() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	switch {
	case !ok:
		return m, nil
	case msg.Media.IsVideo():
		return m.playVideo(msg)
	case !msg.Media.IsAudio():
		return m.say("nothing to play here — the cursor has to be on a voice note or a video"), nil
	}
	if m.player.active() && m.player.event == msg.ID {
		return m.togglePlay()
	}
	command, err := audio.Resolve(m.prefs.display.Media.Audio.PlayerCommand())
	if err != nil {
		return m.say(playerAdvice(err)), nil
	}
	m.player.close()

	policy := m.mediaPolicy(msg)
	m.player = m.player.replace(playerState{
		event:      msg.ID,
		title:      audioTitle(msg),
		place:      m.placeOf(msg),
		configured: policy.Speed,
		speed:      policy.Speed,
		loading:    true,
	})
	job := mediaJob{
		roomID: msg.RoomID, eventID: msg.ID,
		name: msg.Media.Name, mime: msg.Media.Mime,
		cache: policy.Cache,
	}
	mdl, pulse := m.armPulse()
	return mdl, tea.Batch(mdl.playAudioCmd(command, job, policy.Speed), pulse)
}

// playAudioCmd puts the note in the media cache and starts the player. A room that
// forbids caching still plays; its file is just not kept alive, so Trim reclaims it.
func (m Model) playAudioCmd(command []string, job mediaJob, speed float64) tea.Cmd {
	cache, backend, ctx := m.pics.cache, m.backend, m.ctx
	return func() tea.Msg {
		path := ensureOnDisk(ctx, cache, backend.LoadImage, job)
		if path == "" {
			return audioReadyMsg{event: job.eventID, err: errNoAudio}
		}
		session, err := audio.Play(ctx, command, path, speed)
		return audioReadyMsg{event: job.eventID, session: session, err: err}
	}
}

var errNoAudio = fmt.Errorf("the voice note could not be fetched")

func (m Model) handleAudioReady(msg audioReadyMsg) (Model, tea.Cmd) {
	// Lost a race with a second keypress: close it, or two voices play.
	if m.player.event != msg.event {
		msg.session.Close()
		return m, nil
	}
	m.player.loading = false
	if msg.err != nil {
		m.player = m.player.replace(playerState{})
		return m.say("could not play: " + playerAdvice(msg.err)), nil
	}
	m.player.session = msg.session
	return m.say("playing " + isolate(m.player.title)).armPulse()
}

// handlePlayerTick redraws the bar and re-arms while something is moving; otherwise
// the chain ends and the next control restarts it.
func (m Model) handlePlayerTick() (Model, tea.Cmd) {
	m.player.pulsing = false
	if !m.player.active() {
		return m, nil
	}
	if m.player.grace == 0 && !m.player.advancing() {
		return m.armClose()
	}
	wait := m.pulseInterval()
	m.player.grace = max(m.player.grace-1, 0)
	m.player.pulsing = true
	return m, playerTickCmd(wait)
}

// pulseInterval is how long the bar can wait before showing something stale: the
// floor just after a control (the grace ticks), else the faster of the clock (1s) and
// the progress head (duration/cells), over the speed.
func (m Model) pulseInterval() time.Duration {
	if m.player.grace > 0 {
		return playerPulseMin
	}
	state, ok := m.player.state()
	if !ok {
		return playerPulseMin
	}
	per := playerPulseMax // the clock, which reads whole seconds
	if _, _, cells := m.playerRow(state, m.width); cells > 1 && state.Duration > 0 {
		per = min(per, state.Duration/time.Duration(cells-1))
	}
	speed := state.Speed
	if speed <= 0 {
		speed = 1
	}
	return min(max(time.Duration(float64(per)/speed), playerPulseMin), playerPulseMax)
}

// armClose starts the one-shot that removes a finished note's bar, per config. A
// paused note does not arm it.
func (m Model) armClose() (Model, tea.Cmd) {
	state, ok := m.player.state()
	if !ok || !state.Done {
		return m, nil
	}
	switch delay := m.prefs.display.Media.Audio.CloseDelay(); {
	case delay < 0:
		return m, nil // stays until stopped by hand
	case delay == 0:
		return m.stopPlaying()
	default:
		return m, playerCloseCmd(delay, m.player.event)
	}
}

func (m Model) handlePlayerClose(msg playerCloseMsg) (Model, tea.Cmd) {
	if m.player.event != msg.event || m.player.advancing() {
		return m, nil
	}
	return m.stopPlaying()
}

// armPulse gives the pulse a grace period and starts it unless a chain is running.
// Every control goes through here.
func (m Model) armPulse() (Model, tea.Cmd) {
	if !m.player.active() {
		return m, nil
	}
	m.player.grace = pulseGrace
	if m.player.pulsing {
		return m, nil
	}
	m.player.pulsing = true
	return m, playerTickCmd(playerPulseMin)
}

// playerTickCmd is the bar's next pulse, after the given wait.
func playerTickCmd(after time.Duration) tea.Cmd {
	return tea.Tick(after, func(time.Time) tea.Msg { return playerTickMsg{} })
}

// playerCloseCmd closes the finished player after the given wait, unless another
// message has started playing by then.
func playerCloseCmd(after time.Duration, event domain.EventID) tea.Cmd {
	return tea.Tick(after, func(time.Time) tea.Msg { return playerCloseMsg{event: event} })
}

// playerAction handles the bar's keys; handled is false while nothing is loaded.
func (m Model) playerAction(act action) (Model, tea.Cmd, bool) {
	if !m.player.active() {
		return m, nil, false
	}
	switch act {
	case actPlayPause:
		return answered(m.togglePlay())
	case actSeekBack:
		return answered(m.seekAudio(-m.prefs.display.Media.Audio.SkipStep()))
	case actSeekForward:
		return answered(m.seekAudio(m.prefs.display.Media.Audio.SkipStep()))
	case actSlower:
		return answered(m.changeSpeed(-speedStep))
	case actFaster:
		return answered(m.changeSpeed(speedStep))
	case actNormalSpeed:
		return answered(m.setSpeed(m.player.configured))
	case actSaveSpeed:
		return answered(m.openSpeedScope())
	case actStopPlay:
		return answered(m.stopPlaying())
	}
	return m, nil, false
}

const speedStep = 0.25

func (m Model) togglePlay() (Model, tea.Cmd) {
	if m.player.session == nil {
		return m, nil
	}
	return m.control(audioSession.TogglePause)
}

func (m Model) seekAudio(d time.Duration) (Model, tea.Cmd) {
	if m.player.session == nil {
		return m, nil
	}
	return m.control(func(s audioSession) error { return s.Seek(d) })
}

// control sends one control to the player off the event loop (a hung player must not
// freeze the screen: a write waits up to its timeout), and pulses the bar.
func (m Model) control(do func(audioSession) error) (Model, tea.Cmd) {
	session, event := m.player.session, m.player.event
	send := func() tea.Msg { return playerControlMsg{event: event, err: do(session)} }
	m, pulse := m.armPulse()
	return m, tea.Batch(send, pulse)
}

// playerControlMsg is a control's answer, for the note it was sent to.
type playerControlMsg struct {
	event domain.EventID
	err   error
}

// handlePlayerControl says why a control failed, while that note is still loaded.
func (m Model) handlePlayerControl(msg playerControlMsg) (Model, tea.Cmd) {
	if msg.err == nil || msg.event != m.player.event {
		return m, nil
	}
	return m.say(msg.err.Error()), nil
}

func (m Model) changeSpeed(delta float64) (Model, tea.Cmd) {
	return m.setSpeed(m.player.speed + delta)
}

// setSpeed plays at f, silently clamped to what the player can pitch-correct.
func (m Model) setSpeed(f float64) (Model, tea.Cmd) {
	f = min(max(f, audio.MinSpeed), audio.MaxSpeed)
	m.player.speed = f
	if m.player.session == nil {
		return m, nil
	}
	return m.say("playing at " + audio.FormatSpeed(f)).control(func(s audioSession) error { return s.SetSpeed(f) })
}

func (m Model) stopPlaying() (Model, tea.Cmd) {
	m.player.close()
	m.player = m.player.replace(playerState{})
	return m.clearStatus(), nil
}

// audioTitle is the bar's name for a note, replacing bridge placeholders.
func audioTitle(msg domain.Message) string {
	name := strings.TrimSpace(msg.Media.Name)
	switch strings.ToLower(name) {
	case "", "audio", "voice message", "m.audio":
		return "voice message"
	}
	return name
}

// playerAdvice turns a player failure (usually mpv missing) into advice.
func playerAdvice(err error) string {
	text := err.Error()
	if strings.Contains(text, "cannot run") || strings.Contains(text, "executable file not found") ||
		strings.Contains(text, "no player found") {
		return text + " — install mpv or vlc, or set [display.media.audio] player to one this can control"
	}
	return text
}

// withPlayerScope puts the player's keys ahead of a pane's while a note is loaded.
// Never applied while typing, so space still types.
func (m Model) withPlayerScope(scopes ...scope) []scope {
	if !m.player.active() {
		return scopes
	}
	return append([]scope{scopePlayer}, scopes...)
}

// renderPlayer draws the bar above the status line. As the terminal narrows the key
// legend goes first (in two steps), then the progress bar; clock and speed stay.
func (m Model) renderPlayer(width int) string {
	if !m.player.active() {
		return ""
	}
	if m.player.loading {
		return m.theme.Muted.Render(drawLine("⏳ "+isolate(m.player.title)+" — fetching…",
			lineSpec{width: width, sentence: true}))
	}
	st := m.player.session.State()
	left, keys, cells := m.playerRow(st, width)
	if cells < minProgressCells {
		return m.theme.Muted.Render(clamp(left, width))
	}
	line := strings.TrimRight(left+"  "+progressBar(st, cells)+"  "+keys, " ")
	return m.theme.Muted.Render(clamp(line, width))
}

// playerRow divides the row at this width: the fixed block, the legend that fits,
// and the cells left for the progress bar (< minProgressCells: none). Shared with the
// pulse, which needs the cell count.
func (m Model) playerRow(st audio.State, width int) (left, keys string, cells int) {
	left = strings.Join([]string{
		playIcon(st),
		nameCell(m.player.title, maxTitleCells),
		audio.Format(st.Position) + " / " + audio.Format(st.Duration),
		audio.FormatSpeed(m.player.speed),
	}, "  ")
	const gaps = 4 // the double spaces around the bar
	for _, hints := range []string{m.playerHints(true), m.playerHints(false), ""} {
		room := width - ansi.StringWidth(left) - ansi.StringWidth(hints) - gaps
		if room >= minProgressCells {
			return left, hints, room
		}
	}
	return left, "", 0
}

const maxTitleCells = 24

// minProgressCells is the narrowest progress bar worth drawing.
const minProgressCells = 12

// playerHints is the bar's legend; the short form keeps pause and stop.
func (m Model) playerHints(full bool) string {
	if !full {
		return m.hintLine(
			keyed(m.keys.keyHint(scopePlayer, actPlayPause), "play/pause"),
			keyed(m.keys.keyHint(scopePlayer, actStopPlay), "stop"),
		)
	}
	return m.hintLine(
		keyed(m.keys.keyHint(scopePlayer, actPlayPause), "play/pause"),
		keyed(m.keys.keyHint(scopePlayer, actSeekBack)+"/"+m.keys.keyHint(scopePlayer, actSeekForward),
			fmt.Sprintf("±%ds", int(m.prefs.display.Media.Audio.SkipStep().Seconds()))),
		keyed(m.keys.keyHint(scopePlayer, actSlower)+"/"+m.keys.keyHint(scopePlayer, actFaster), "speed"),
		keyed(m.keys.keyHint(scopePlayer, actSaveSpeed), "remember"),
		keyed(m.keys.keyHint(scopePlayer, actStopPlay), "stop"),
	)
}

func playIcon(st audio.State) string {
	switch {
	case st.Done:
		return "■"
	case st.Paused:
		return "⏸"
	default:
		return "▶"
	}
}

// progressBar draws playback position; an unknown duration gets an empty track.
func progressBar(st audio.State, cells int) string {
	if cells < 1 {
		return ""
	}
	head := 0
	if st.Duration > 0 {
		head = int(float64(cells-1) * min(float64(st.Position)/float64(st.Duration), 1))
	}
	return strings.Repeat("━", head) + "●" + strings.Repeat("─", cells-1-head)
}

// openSpeedScope offers the places to remember the current speed for, built from
// where the note was said.
func (m Model) openSpeedScope() (Model, tea.Cmd) {
	place := m.player.place
	who := place.Person
	if who == "" {
		who = place.Sender
	}
	where := place.Room
	if where == "" {
		where = place.RoomID
	}
	var scopes []ruleTarget
	if place.Sender != "" && place.RoomID != "" {
		scopes = append(scopes, ruleTarget{
			match: place.RoomID, sender: place.Sender, what: who + " in " + where})
	}
	if place.Sender != "" {
		scopes = append(scopes, ruleTarget{sender: place.Sender, what: who + ", anywhere"})
	}
	if place.RoomID != "" {
		scopes = append(scopes, ruleTarget{match: place.RoomID, what: "everything in " + where})
	}
	if place.Space != "" && !m.spans(place.Space) {
		scopes = append(scopes, ruleTarget{match: homeEntry(place.Space), what: "everything in " + domain.HomeLabel(place.Space)})
	}
	// "everywhere" is the base setting, not a rule.
	scopes = append(scopes, ruleTarget{what: "everywhere"})

	m.choosing.speedScopes = scopes
	m.picker = newPicker(pickerSpeedScope, ruleScopeItems(scopes))
	return m, nil
}

func (m Model) chooseSpeedScope(index int) (Model, tea.Cmd) {
	if index < 0 || index >= len(m.choosing.speedScopes) {
		return m.closePicker(), nil
	}
	target := m.choosing.speedScopes[index]
	speed := m.player.speed
	cfg := m.conf.base.Clone()
	if target.match == "" && target.sender == "" {
		cfg.Display.Media.Audio.Speed = speed
	} else {
		cfg.Display.Media.Rules = withSpeedRule(cfg.Display.Media.Rules, target, speed)
	}
	m = m.closePicker()
	return m.applyConfig(cfg, fmt.Sprintf("%s plays at %s", target.what, audio.FormatSpeed(speed)))
}

// withSpeedRule sets the speed on the rule naming this place, adding one if none.
func withSpeedRule(rules []config.MediaRule, target ruleTarget, speed float64) []config.MediaRule {
	next := append([]config.MediaRule(nil), rules...)
	for i := range next {
		if next[i].Match == target.match && next[i].Sender == target.sender {
			next[i].Speed = &speed
			return next
		}
	}
	return append(next, config.MediaRule{Match: target.match, Sender: target.sender, Speed: &speed})
}
