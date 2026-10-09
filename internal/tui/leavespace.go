package tui

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Leaving a space from the rail is asked as its network leaves it (domain.Space.Leaving).
// A WhatsApp community goes with all its groups, asked once. A Matrix space is a
// membership of its own, and so is each of its rooms: whether its rooms go too is asked,
// then, for each of them also in another space, whether that one goes (or every such
// room, either way), and last whether to leave the space itself. Nothing is left until
// that last yes. A bridged room left is left on its network too (the bridge passes the
// leave on), which the last question says.

// spaceLeave is a space being left, gathered over its questions.
type spaceLeave struct {
	space domain.Space
	// rooms go with the space; shared are rooms also in another space, still to ask about.
	rooms  []domain.RoomID
	shared []domain.RoomID
	// asked is how many shared rooms have been asked about.
	asked int
}

// askLeaveSpaceWith opens the questions for a space left on its own or with its rooms.
func (m Model) askLeaveSpaceWith(space domain.Space) Model {
	l := spaceLeave{space: space}
	if space.Leaving == domain.LeftWithRooms {
		l.rooms = slices.Clone(space.Children)
		return m.nextSpaceQuestion(l)
	}
	if len(space.Children) == 0 {
		return m.nextSpaceQuestion(l)
	}
	m.confirm = confirmState{action: pendingLeaveSpaceRooms, leaving: l}
	return m
}

// answerSpaceStep takes an answer to a question before the last one of leaving a space:
// a no to its rooms, or to one of them, is an answer, not a cancel.
func (m Model) answerSpaceStep(c confirmState, yes bool) (Model, bool) {
	switch c.action { //nolint:exhaustive // every other action is answered by resolveConfirm
	case pendingLeaveSpaceRooms:
		return m.answerSpaceRooms(c.leaving, yes), true
	case pendingLeaveSharedRoom:
		return m.answerSharedRoom(c.leaving, yes, false), true
	}
	return m, false
}

// leaveSpace leaves what the questions settled on.
func (m Model) leaveSpace(l spaceLeave) (Model, tea.Cmd) {
	m = m.say("leaving " + isolate(l.space.DisplayName()) + "…")
	return m, m.leaveSpaceAndRoomsCmd(l)
}

// answerSpaceRooms takes whether the space's rooms go with it: each room only in this
// space goes, and each also in another is asked about.
func (m Model) answerSpaceRooms(l spaceLeave, yes bool) Model {
	if yes {
		for _, room := range l.space.Children {
			if len(m.otherSpacesOf(room, l.space.ID)) > 0 {
				l.shared = append(l.shared, room)
			} else {
				l.rooms = append(l.rooms, room)
			}
		}
	}
	return m.nextSpaceQuestion(l)
}

// answerSharedRoom takes whether the first room still asked about goes, or, for all,
// whether every one left to ask about does.
func (m Model) answerSharedRoom(l spaceLeave, yes, all bool) Model {
	if len(l.shared) == 0 {
		return m.nextSpaceQuestion(l)
	}
	n := 1
	if all {
		n = len(l.shared)
	}
	if yes {
		l.rooms = append(slices.Clone(l.rooms), l.shared[:n]...)
	}
	l.shared, l.asked = l.shared[n:], l.asked+n
	return m.nextSpaceQuestion(l)
}

// nextSpaceQuestion asks about the next shared room, or, with none left, whether to
// leave the space itself.
func (m Model) nextSpaceQuestion(l spaceLeave) Model {
	if len(l.shared) > 0 {
		m.confirm = confirmState{action: pendingLeaveSharedRoom, leaving: l}
		return m
	}
	m.confirm = confirmState{action: pendingLeaveSpaceItself, leaving: l}
	return m
}

// otherSpacesOf is the names of the spaces besides except that hold room and could be
// left themselves: an account's own space holds all its rooms, so it is not another
// place a room is in.
func (m Model) otherSpacesOf(room domain.RoomID, except domain.SpaceID) []string {
	var names []string
	for _, s := range m.rooms.spaces {
		if s.ID != except && s.Leavable() && slices.Contains(s.Children, room) {
			names = append(names, isolate(s.DisplayName()))
		}
	}
	return names
}

// spaceRoomName is a room of a space being left, by the name the room list gives it.
func (m Model) spaceRoomName(id domain.RoomID) string {
	if room, ok := m.roomByID(id); ok {
		return m.roomName(room)
	}
	return string(id)
}

// leaveSpacePrompt is the question for each step of leaving a space.
func (m Model) leaveSpacePrompt(c confirmState) string {
	l := c.leaving
	name := isolate(l.space.DisplayName())
	switch c.action {
	case pendingLeaveSpaceRooms:
		return fmt.Sprintf("leaving %s: leave its %s too? no leaves only the space", name, roomsPhrase(len(l.space.Children)))
	case pendingLeaveSharedRoom:
		room := l.shared[0]
		return fmt.Sprintf("%s is also in %s: leave it too? (%d of %d)", m.spaceRoomName(room),
			strings.Join(m.otherSpacesOf(room, l.space.ID), ", "), l.asked+1, l.asked+len(l.shared))
	default:
		q := "leave " + name
		if len(l.rooms) > 0 {
			q += " and " + roomsPhrase(len(l.rooms)) + " in it"
		}
		return q + "?" + m.bridgedNote(l.rooms)
	}
}

// bridgedNote says which rooms being left are bridged ones, which the bridge leaves on
// their network too.
func (m Model) bridgedNote(rooms []domain.RoomID) string {
	by := map[domain.Protocol]int{}
	var networks []domain.Protocol
	for _, id := range rooms {
		room, ok := m.roomByID(id)
		if !ok || domain.NetworkOf(string(id)) != domain.ProtocolMatrix {
			continue
		}
		network := m.factsFor(room).Protocol
		if !network.IsBridged() {
			continue
		}
		if by[network] == 0 {
			networks = append(networks, network)
		}
		by[network]++
	}
	if len(networks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(networks))
	for _, n := range networks {
		parts = append(parts, fmt.Sprintf("%d on %s", by[n], n))
	}
	return " bridged rooms are left on their network too: " + strings.Join(parts, ", ")
}

// spaceLeftMsg reports leaving a space and the rooms that went with it.
type spaceLeftMsg struct {
	space domain.Space
	// left are the rooms left; failed, each room that could not be, with why.
	left   []domain.RoomID
	failed []roomFailure
	// spaceErr is why the space itself could not be left.
	spaceErr error
}

// roomFailure is one room that could not be left.
type roomFailure struct {
	room domain.RoomID
	err  error
}

// leaveSpaceAndRoomsCmd leaves each room, then the space. A room that fails does not
// stop the others, nor the space: each failure is reported.
func (m Model) leaveSpaceAndRoomsCmd(l spaceLeave) tea.Cmd {
	ctx, backend := m.ctx, m.backend
	return func() tea.Msg {
		msg := spaceLeftMsg{space: l.space}
		for _, room := range l.rooms {
			if err := backend.LeaveRoom(ctx, room); err != nil {
				msg.failed = append(msg.failed, roomFailure{room: room, err: err})
				continue
			}
			msg.left = append(msg.left, room)
		}
		msg.spaceErr = backend.LeaveRoom(ctx, domain.RoomID(l.space.ID))
		return msg
	}
}

// handleLeaving reports a room, or a space, left.
func (m Model) handleLeaving(msg tea.Msg) (Model, tea.Cmd) {
	if space, ok := msg.(spaceLeftMsg); ok {
		return m.handleSpaceLeft(space)
	}
	left, _ := msg.(leftMsg)
	return m.handleLeft(left)
}

// handleSpaceLeft drops what was left and says what was not.
func (m Model) handleSpaceLeft(msg spaceLeftMsg) (Model, tea.Cmd) {
	for _, room := range msg.left {
		m.rooms = m.rooms.without(room)
	}
	name := isolate(msg.space.DisplayName())
	var said string
	if msg.spaceErr != nil {
		said = "could not leave " + name + ": " + msg.spaceErr.Error()
		if len(msg.left) > 0 {
			said += "; left " + roomsPhrase(len(msg.left)) + " in it"
		}
	} else {
		said = "left " + name
		if len(msg.left) > 0 {
			said += " and " + roomsPhrase(len(msg.left)) + " in it"
		}
	}
	if len(msg.failed) > 0 {
		failures := make([]string, 0, len(msg.failed))
		for _, f := range msg.failed {
			failures = append(failures, m.spaceRoomName(f.room)+" ("+f.err.Error()+")")
		}
		said += "; could not leave " + strings.Join(failures, "; ")
	}
	m.logErr(slog.LevelWarn, "leave a space", msg.spaceErr, "space", msg.space.ID)
	for _, f := range msg.failed {
		m.logErr(slog.LevelWarn, "leave a room of a space", f.err, "room", f.room)
	}
	// Said last: the open room may have gone, and opening the next one says so.
	next, cmd := m.handleInvites(m.rooms.invites)
	next = next.say(said)
	return next, tea.Batch(cmd, next.refreshRoomsCmd(), next.refreshSpacesCmd())
}
