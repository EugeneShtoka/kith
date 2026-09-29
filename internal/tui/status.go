package tui

import (
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The status line's left slot holds a standing line (what the pane is doing, true
// until it changes) and an event covering it (what just happened, for statusLife).
// say, doing and clearStatus are the only ways to write it.

// statusLife is how long an event stays on screen.
const statusLife = 6 * time.Second

// statusSweepGrace lands the sweep just after expiry rather than racing it.
const statusSweepGrace = 50 * time.Millisecond

// statusState is the slot's contents: the standing line, and the event covering it.
type statusState struct {
	standing string
	event    string
	// until is when the event stops being news. Zero means there is no event.
	until time.Time
}

// statusTickMsg is the sweep.
type statusTickMsg struct{}

// status is what the slot shows: the event while it is still news, else the
// standing line. An expired event is never shown, swept or not.
func (m Model) status() string {
	if m.sayingSomething() {
		return m.st.event
	}
	return m.st.standing
}

// sayingSomething reports whether an event is still covering the standing line.
func (m Model) sayingSomething() bool {
	return m.st.event != "" && time.Now().Before(m.st.until)
}

// say reports something that just happened, covering the standing line for
// statusLife.
func (m Model) say(text string) Model {
	m.st.event = text
	m.st.until = time.Now().Add(statusLife)
	return m
}

// sayErr reports a failed action with its reason ("could not mark X read: M_BAD_JSON
// …") and logs it at warn, since the status line forgets it in seconds. what is the
// sentence without the reason; nil err says what alone.
func (m Model) sayErr(what string, err error, attrs ...any) Model {
	if err == nil {
		return m.say(what)
	}
	m.logErr(slog.LevelWarn, what, err, attrs...)
	return m.say(what + ": " + err.Error())
}

// logErr writes a failure to the client's log only (a failure not worth the status
// line, or already reported there another way).
func (m Model) logErr(level slog.Level, what string, err error, attrs ...any) {
	if err == nil || m.log == nil {
		return
	}
	m.log.Log(m.ctx, level, what, append([]any{"err", err}, attrs...)...)
}

// doing sets the standing line and clears any event, which a new state makes stale.
func (m Model) doing(text string) Model {
	m.st.standing = text
	m.st.event, m.st.until = "", time.Time{}
	return m
}

// clearStatus empties the slot.
func (m Model) clearStatus() Model {
	m.st = statusState{}
	return m
}

// handleStatusTick sweeps an expired event. It does not re-arm; Update arms the
// next sweep when something new is said.
func (m Model) handleStatusTick() (Model, tea.Cmd) {
	m.sweepArmed = false
	if m.st.event != "" && !time.Now().Before(m.st.until) {
		m.st.event, m.st.until = "", time.Time{}
	}
	return m, nil
}

// armStatusSweep schedules the one sweep an event needs, unless one is pending.
// Called from Update so the dozens of say callers need not know about the timer.
func (m Model) armStatusSweep() (Model, tea.Cmd) {
	if m.sweepArmed || m.st.event == "" {
		return m, nil
	}
	wait := time.Until(m.st.until) + statusSweepGrace
	if wait <= 0 {
		return m, nil // already stale; status() reads past it anyway
	}
	m.sweepArmed = true
	return m, statusSweepCmd(wait)
}

// statusSweepCmd schedules one sweep after wait.
func statusSweepCmd(wait time.Duration) tea.Cmd {
	return tea.Tick(wait, func(time.Time) tea.Msg { return statusTickMsg{} })
}
