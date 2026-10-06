package tui

import (
	"fmt"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// pendingAction is an irreversible or wide-reaching action waiting for confirmation.
type pendingAction int

// The confirmable actions.
const (
	pendingNone pendingAction = iota
	pendingLeave
	pendingRedact
	pendingReject
	pendingMarkGroupRead
	pendingKick
	pendingBan
	// pendingJoinPlace: a room a followed link named that this account is not in.
	pendingJoinPlace
	// pendingDeleteTag: a [[tag]], by name (group).
	pendingDeleteTag
	// pendingCombineTags: a tag (group) renamed to another's name (address), folded
	// into it.
	pendingCombineTags
)

// confirmState is the pending action and its targets, captured with the question so a
// list shifting under the cursor cannot redirect the action.
type confirmState struct {
	action pendingAction
	room   domain.Room
	// group and rooms: the rail group label and rooms a group-wide action applies to.
	group string
	rooms []domain.RoomID
	// person/personName: whom a membership action targets; roomID is the open room.
	person     string
	personName string
	roomID     domain.RoomID
	// event is the message a pending redaction is about; mine decides the wording.
	event domain.EventID
	mine  bool
	// address and via: the room a followed link named (alias or ID) and its servers.
	address string
	via     []string
}

// active reports whether a confirmation is outstanding.
func (c confirmState) active() bool { return c.action != pendingNone }

// handleInvites applies a fresh, authoritative invitation set (from the cache or the
// stream), rebuilding the rail and announcing new invitations.
func (m Model) handleInvites(invites []domain.Room) (Model, tea.Cmd) {
	arrived := newInvites(m.rooms.invites, invites)
	m.rooms = m.rooms.withInvites(invites)
	m = m.refreshPlaces()

	m = m.rebuiltRail()

	if arrived > 0 {
		m = m.say(fmt.Sprintf("%s — see the Invites group", invitePhrase(arrived)))
	}
	// The open room may have been an answered invitation, or its group may have vanished.
	if _, ok := m.currentRoom(); !ok {
		if fr := m.filteredRooms(); len(fr) > 0 {
			return m.selectRoom(fr[0])
		}
		return m.clearRoom()
	}
	return m, nil
}

// newInvites counts how many of next were not already in prev.
func newInvites(prev, next []domain.Room) int {
	known := make(map[domain.RoomID]bool, len(prev))
	for i := range prev {
		known[prev[i].ID] = true
	}
	added := 0
	for i := range next {
		if !known[next[i].ID] {
			added++
		}
	}
	return added
}

// invitePhrase pluralizes an invitation count for the status line.
func invitePhrase(n int) string {
	if n == 1 {
		return "1 new invitation"
	}
	return fmt.Sprintf("%d new invitations", n)
}

// askLeave and askReject open the confirmation for the selected room; each is inert on
// the wrong kind of row.
func (m Model) askLeave() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	m.confirm = confirmState{action: pendingLeave, room: room}
	return m, nil
}

func (m Model) askReject() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || !room.IsInvite() {
		return m, nil
	}
	m.confirm = confirmState{action: pendingReject, room: room}
	return m, nil
}

// confirmPrompt is the question shown while a destructive action is pending.
func (m Model) confirmPrompt() string {
	switch m.confirm.action {
	case pendingLeave:
		return "leave " + m.roomName(m.confirm.room) + "?"
	case pendingReject:
		return "reject the invitation to " + m.roomName(m.confirm.room) + "?"
	case pendingMarkGroupRead:
		return fmt.Sprintf("mark %s read in %s?", roomsPhrase(len(m.confirm.rooms)), m.confirm.group)
	case pendingRedact:
		return m.redactPrompt(m.confirm)
	case pendingKick:
		return "remove " + m.confirm.personName + " from this room?"
	case pendingBan:
		return "ban " + m.confirm.personName + "? they cannot come back until unbanned"
	case pendingJoinPlace:
		return "join " + m.confirm.address + "? you are not in that room"
	case pendingDeleteTag:
		return "delete the tag " + isolate(m.confirm.group) + "? its rooms stay where else they are"
	case pendingCombineTags:
		return "a tag " + isolate(m.confirm.address) + " exists: combine " + isolate(m.confirm.group) +
			" into it? it holds both tags' rooms and keeps its own settings"
	case pendingNone:
		return ""
	}
	return ""
}

// resolveConfirm carries out the pending action, or abandons it; the prompt closes
// either way.
func (m Model) resolveConfirm(yes bool) (Model, tea.Cmd) {
	pending := m.confirm
	m.confirm = confirmState{}
	if !yes {
		m = m.say("canceled")
		return m, nil
	}
	switch pending.action {
	case pendingLeave:
		m = m.say("leaving " + m.roomName(pending.room) + "…")
		return m, m.leaveRoomCmd(pending.room.ID)
	case pendingReject:
		m = m.say("rejecting the invitation to " + m.roomName(pending.room) + "…")
		return m, m.leaveRoomCmd(pending.room.ID)
	case pendingMarkGroupRead:
		m = m.say(fmt.Sprintf("marking %s in %s read…", roomsPhrase(len(pending.rooms)), pending.group))
		return m, m.markRoomsReadCmd(pending.rooms, pending.group)
	case pendingRedact:
		return m.redact(pending)
	case pendingKick:
		m = m.doing("removing " + pending.personName + "…")
		return m, m.memberCmd(pending.roomID, pending.person, memberKick, "")
	case pendingBan:
		m = m.doing("banning " + pending.personName + "…")
		return m, m.memberCmd(pending.roomID, pending.person, memberBan, "")
	case pendingJoinPlace:
		return m.joinPlace(pending)
	case pendingDeleteTag:
		return m.deleteTag(pending.group)
	case pendingCombineTags:
		return m.combineTags(pending.group, pending.address)
	case pendingNone:
		return m, nil
	}
	return m, nil
}

// acceptInvite joins the selected invitation, without confirmation.
func (m Model) acceptInvite() (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || !room.IsInvite() {
		return m, nil
	}
	m = m.say("joining " + m.roomName(room) + "…")
	return m, m.joinRoomCmd(string(room.ID))
}

// submitJoin joins whatever address was typed. An empty prompt just closes.
func (m Model) submitJoin(target string) (Model, tea.Cmd) {
	target = strings.TrimSpace(target)
	if target == "" {
		return m, nil
	}
	m = m.say("joining " + target + "…")
	return m, m.joinRoomCmd(target)
}

// handleJoined reports a join; on success the rooms and spaces are re-fetched.
func (m Model) handleJoined(msg joinedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m = m.sayErr("join failed", msg.err)
		return m, nil
	}
	m = m.say("joined")
	return m, tea.Batch(m.refreshRoomsCmd(), m.refreshSpacesCmd())
}

// handleLeft reports a leave (or rejection) and drops the room locally at once.
func (m Model) handleLeft(msg leftMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m = m.sayErr("leave failed", msg.err)
		return m, nil
	}
	m = m.say("left")
	m.rooms = m.rooms.without(msg.roomID)
	next, cmd := m.handleInvites(m.rooms.invites)
	return next, tea.Batch(cmd, next.refreshRoomsCmd())
}

// handleCachedInvites seeds the invitation set from the cache at startup; a read error
// is not evidence the invitations are gone.
func (m Model) handleCachedInvites(msg invitesMsg) (Model, tea.Cmd) {
	m.logErr(slog.LevelWarn, "load cached invites", msg.err)
	if msg.err != nil {
		return m, nil
	}
	return m.handleInvites(msg.invites)
}

// handleInviteUpdate applies a streamed invitation set and keeps listening.
func (m Model) handleInviteUpdate(msg inviteUpdateMsg) (Model, tea.Cmd) {
	mdl, cmd := m.handleInvites(msg.invites)
	return mdl, tea.Batch(cmd, m.listenInvitesCmd())
}

// handleConfirmKey answers a pending action; nothing else reaches the app meanwhile.
func (m Model) handleConfirmKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	switch m.keys.lookup(key.String(), scopeConfirm) {
	case actYes:
		return m.resolveConfirm(true)
	case actNo:
		return m.resolveConfirm(false)
	}
	return m, nil
}

// goToReplacement is /replacement: it leaves an upgraded (tombstoned) room for its
// replacement, joining it only if the server has not already.
func (m Model) goToReplacement(room domain.Room) (Model, tea.Cmd) {
	m.compose.input, m.compose.drafted = "", nil
	if room.Replacement == "" {
		return m.say("this room has not been replaced"), nil
	}
	if replacement, joined := m.rooms.byID(room.Replacement); joined {
		return m.selectRoom(replacement)
	}
	return m.say("joining the room that replaced this one…"), m.joinRoomCmd(string(room.Replacement))
}
