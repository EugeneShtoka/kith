package tui

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Leaving a space from the rail follows its network (domain.Space.Leaving). A WhatsApp
// community goes with all its groups, asked once. For a Matrix space (each room a separate
// membership): ask whether its rooms go, then about each also in another space (or all at
// once), and last whether to leave the space. Nothing is left until that last yes. A bridged
// room is left on its network only if its bridge passes leaves on, which the last question
// says.

type spaceLeave struct {
	space domain.Space
	// rooms go with the space; shared are rooms also in another space, still to ask about.
	rooms  []domain.RoomID
	shared []domain.RoomID
	// asked counts shared rooms asked about; kept counts rooms kept because they are parts of
	// another chat left only whole (a forum's topics).
	asked, kept int
}

// askLeaveSpaceWith opens the questions for a space left on its own or with its rooms.
func (m Model) askLeaveSpaceWith(space domain.Space) Model {
	l := spaceLeave{space: space}
	if space.Leaving == domain.LeftWithRooms || space.Leaving == domain.LeftWhole {
		l.rooms = slices.Clone(space.Children)
		return m.nextSpaceQuestion(l)
	}
	if len(space.Children) == 0 {
		return m.nextSpaceQuestion(l)
	}
	m.confirm = confirmState{action: pendingLeaveSpaceRooms, leaving: l}
	return m
}

// answerEarlierStep takes an answer before the last question of leaving a space or deleting
// a chat: a no there is an answer (keep the rooms, delete for you only), not a cancel.
func (m Model) answerEarlierStep(c confirmState, yes bool) (Model, bool) {
	switch c.action { //nolint:exhaustive // every other action is answered by resolveConfirm
	case pendingLeaveSpaceRooms:
		return m.answerSpaceRooms(c.leaving, yes), true
	case pendingLeaveSharedRoom:
		return m.answerSharedRoom(c.leaving, yes, false), true
	case pendingDeleteForThem:
		return m.answerDeleteForThem(c, yes), true
	case pendingSignOutForget:
		return m.answerSignOutForget(c, yes), true
	}
	return m, false
}

// leaveSpace leaves what the questions settled on.
func (m Model) leaveSpace(l spaceLeave) (Model, tea.Cmd) {
	m = m.say("leaving " + isolate(l.space.DisplayName()) + "…")
	return m, m.leaveSpaceAndRoomsCmd(l)
}

// answerSpaceRooms takes whether the space's rooms go: rooms only in this space go, rooms also
// elsewhere are asked about.
func (m Model) answerSpaceRooms(l spaceLeave, yes bool) Model {
	if yes {
		for _, room := range l.space.Children {
			if _, whole := m.wholeChatOf(room); whole {
				l.kept++ // leaving it would leave all of its forum
				continue
			}
			if len(m.otherSpacesOf(room, l.space.ID)) > 0 {
				l.shared = append(l.shared, room)
			} else {
				l.rooms = append(l.rooms, room)
			}
		}
	}
	return m.nextSpaceQuestion(l)
}

// answerSharedRoom takes whether the next asked room goes, or with all, every remaining one.
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

// otherSpacesOf is the names of other leavable spaces holding room; an account's own space
// holds all its rooms, so it does not count.
func (m Model) otherSpacesOf(room domain.RoomID, except domain.SpaceID) []string {
	var names []string
	for _, s := range m.rooms.spaces {
		if s.ID != except && s.Leavable() && slices.Contains(s.Children, room) {
			names = append(names, isolate(s.DisplayName()))
		}
	}
	return names
}

// wholeChatOf names a space holding room whose rooms are parts of one chat (a forum,
// domain.Space.Whole); such a room is left only with all of it.
func (m Model) wholeChatOf(room domain.RoomID) (string, bool) {
	for _, s := range m.rooms.spaces {
		if s.Whole() && slices.Contains(s.Children, room) {
			return isolate(s.DisplayName()), true
		}
	}
	return "", false
}

// spaceRoomName is a room's name as the room list gives it.
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
		if l.kept > 0 {
			q += "; " + roomsPhrase(l.kept) + " of forums stay, left only with the whole forum"
		}
		return q + "?" + m.bridgedNote(l.rooms)
	}
}

// bridgedNote names the bridged rooms being left. A bridge leaves them on their network only
// if configured to (mautrix's bridge_matrix_leave, off by default), which kith cannot see.
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
	return " bridged: " + strings.Join(parts, ", ") +
		", left there too only if the bridge passes a leave on (bridge_matrix_leave)"
}

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

// leaveSpaceAndRoomsCmd leaves each room, then the space. A failing room stops neither the
// others nor the space; each failure is reported.
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

// handleLeaving reports a room or a space left, or an account signed out.
func (m Model) handleLeaving(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spaceLeftMsg:
		return m.handleSpaceLeft(msg)
	case signedOutMsg:
		return m.handleSignedOut(msg)
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
