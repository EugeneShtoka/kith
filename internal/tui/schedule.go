package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Schedules queues messages to send later. Only the daemon (daemon.Remote) implements
// it: a message sent only while the client is open is late, not scheduled.
type Schedules interface {
	// Schedule queues a message, returning the handle that cancels it.
	Schedule(ctx context.Context, msg domain.ScheduledMessage) (string, error)
	// ScheduledMessages returns the whole queue, every room, soonest first.
	ScheduledMessages(ctx context.Context) ([]domain.ScheduledMessage, error)
	// CancelScheduled drops one pending message.
	CancelScheduled(ctx context.Context, id string) error
}

type scheduledMsg struct {
	queue []domain.ScheduledMessage
	err   error
	// onlyRoom narrows the answer to one room ("" = every room); carried with the
	// request so switching rooms meanwhile cannot change it.
	onlyRoom domain.RoomID
}

type scheduleDoneMsg struct {
	what string
	err  error
}

var errNoSchedules = errors.New("this client cannot schedule: it is not attached to a daemon")

// scheduleCmd queues a message off the event loop.
func (m Model) scheduleCmd(msg domain.ScheduledMessage, what string) tea.Cmd {
	if m.schedules == nil {
		return func() tea.Msg { return scheduleDoneMsg{err: errNoSchedules} }
	}
	schedules, ctx := m.schedules, m.ctx
	return func() tea.Msg {
		if _, err := schedules.Schedule(ctx, msg); err != nil {
			return scheduleDoneMsg{err: err}
		}
		return scheduleDoneMsg{what: what}
	}
}

// queuedUnsentMsg reports whether a failed send was queued for retry; if not, its
// draft goes back to the room.
type queuedUnsentMsg struct {
	err    error
	roomID domain.RoomID
	draft  domain.Draft
}

// queueUnsentCmd puts a failed send into the queue.
func (m Model) queueUnsentCmd(unsent sentMsg, queued domain.ScheduledMessage) tea.Cmd {
	if m.schedules == nil {
		return func() tea.Msg {
			return queuedUnsentMsg{
				err:    errors.New("this client is not attached to a daemon"),
				roomID: unsent.roomID, draft: unsent.draft,
			}
		}
	}
	schedules, ctx := m.schedules, m.ctx
	return func() tea.Msg {
		_, err := schedules.Schedule(ctx, queued)
		return queuedUnsentMsg{err: err, roomID: unsent.roomID, draft: unsent.draft}
	}
}

func (m Model) scheduledCmd(onlyRoom domain.RoomID) tea.Cmd {
	if m.schedules == nil {
		return func() tea.Msg { return scheduledMsg{err: errNoSchedules} }
	}
	return fetch(m.ctx, m.schedules.ScheduledMessages, func(queue []domain.ScheduledMessage, err error) tea.Msg {
		return scheduledMsg{queue: queue, err: err, onlyRoom: onlyRoom}
	})
}

func (m Model) cancelScheduledCmd(id string) tea.Cmd {
	if m.schedules == nil {
		return nil
	}
	schedules, ctx := m.schedules, m.ctx
	return func() tea.Msg {
		if err := schedules.CancelScheduled(ctx, id); err != nil {
			return scheduleDoneMsg{err: err}
		}
		return scheduleDoneMsg{what: "canceled"}
	}
}

// handleScheduleDone reports the outcome: the only sign a queued message left the composer.
func (m Model) handleScheduleDone(msg scheduleDoneMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("schedule", msg.err), nil
	}
	return m.say(msg.what), nil
}

// handleScheduled opens the queue in the chooser, or says why it is empty.
func (m Model) handleScheduled(msg scheduledMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("scheduled", msg.err), nil
	}
	now := time.Now()
	items := make([]pickerItem, 0, len(msg.queue))
	for i := range msg.queue {
		entry := &msg.queue[i]
		if msg.onlyRoom != "" && entry.RoomID != msg.onlyRoom {
			continue
		}
		// Within one room the room name says nothing.
		label := m.scheduledRowLabel(*entry, now)
		if msg.onlyRoom != "" {
			label = entry.SummaryWith(now, isolate)
		}
		items = append(items, pickerItem{label: label, value: entry.ID, match: label})
	}
	if len(items) == 0 {
		if msg.onlyRoom != "" {
			return m.say("nothing scheduled in this room"), nil
		}
		return m.say("nothing scheduled"), nil
	}
	m.picker = newPicker(pickerScheduled, items)
	return m, nil
}

// scheduledRowLabel names one pending send: room, when, and what it says.
func (m Model) scheduledRowLabel(entry domain.ScheduledMessage, now time.Time) string {
	where := string(entry.RoomID)
	if room, ok := m.roomByID(entry.RoomID); ok {
		where = m.roomName(room)
	}
	// The body is isolated so an RTL message cannot reorder the time around it.
	return fmt.Sprintf("%s · %s", where, entry.SummaryWith(now, isolate))
}

// parseScheduleTime splits `/at 09:00 text` or `/in 2h text` into send time and message.
func parseScheduleTime(kind, arg string, now time.Time) (time.Time, string, error) {
	arg = strings.TrimSpace(arg)
	when, rest, found := strings.Cut(arg, " ")
	if !found || strings.TrimSpace(rest) == "" {
		return time.Time{}, "", fmt.Errorf("needs a time and a message — try /%s %s", kind, exampleFor(kind))
	}
	rest = strings.TrimSpace(rest)

	switch kind {
	case "in":
		d, err := time.ParseDuration(when)
		if err != nil {
			return time.Time{}, "", fmt.Errorf("%q is not a delay — try 30m, 2h, 1h30m", when)
		}
		if d <= 0 {
			return time.Time{}, "", fmt.Errorf("a delay has to be in the future — %q is not", when)
		}
		return now.Add(d), rest, nil
	default:
		at, err := parseClock(when, now)
		if err != nil {
			return time.Time{}, "", err
		}
		return at, rest, nil
	}
}

// parseClock reads a wall-clock time; one already past today means tomorrow.
func parseClock(when string, now time.Time) (time.Time, error) {
	for _, layout := range []string{"15:04", "1504", "3pm", "3:04pm"} {
		t, err := time.Parse(layout, strings.ToLower(when))
		if err != nil {
			continue // not this layout; the caller reports when none fits
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("%q is not a time — try 09:00 or 9pm", when)
}

func exampleFor(kind string) string {
	if kind == "in" {
		return "2h see you then"
	}
	return "09:00 good morning"
}

// scheduleComposed queues the text after /at or /in, with its thread, reply target and
// mention pills, and clears the composer.
func (m Model) scheduleComposed(room domain.Room, kind, arg string) (Model, tea.Cmd) {
	if _, _, err := parseScheduleTime(kind, arg, time.Now()); err != nil {
		return m.say("/" + kind + " " + err.Error()), nil
	}
	if m.asksSMS(room) {
		m.confirm = confirmState{action: pendingScheduleSMS, room: room, address: kind, body: arg}
		return m, nil
	}
	return m.scheduleComposedNow(room, kind, arg)
}

// scheduleComposedNow is scheduleComposed with nothing left to ask.
func (m Model) scheduleComposedNow(room domain.Room, kind, arg string) (Model, tea.Cmd) {
	at, body, err := parseScheduleTime(kind, arg, time.Now())
	if err != nil {
		return m.say("/" + kind + " " + err.Error()), nil
	}
	mentions, replyTo := m.compose.drafted, m.compose.replyTo
	m.compose = m.compose.cleared()
	msg := domain.ScheduledMessage{
		RoomID:     room.ID,
		Body:       body,
		ThreadRoot: m.thread.root,
		ReplyTo:    replyTo,
		Mentions:   mentions,
		At:         at.UTC(),
		Written:    time.Now().UTC(),
	}
	where := m.roomName(room)
	if msg.ThreadRoot != "" {
		where += " (in this thread)"
	}
	if msg.ReplyTo != "" {
		where += ", as a reply"
	}
	return m, m.scheduleCmd(msg, fmt.Sprintf("queued for %s — %s", at.Format("Mon")+" "+m.prefs.clock.Time(at), where))
}
