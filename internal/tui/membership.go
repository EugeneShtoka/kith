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
	// pendingLeaveSpace: a space (group) left by leaving one room (room), its rooms with it.
	pendingLeaveSpace
	// pendingLeaveSpaceRooms, pendingLeaveSharedRoom and pendingLeaveSpaceItself: the
	// questions of leaving a space (leaving) on its own or with its rooms (leavespace.go).
	pendingLeaveSpaceRooms
	pendingLeaveSharedRoom
	pendingLeaveSpaceItself
	// pendingDeleteForThem and pendingDeleteChat: the questions of deleting a chat that
	// cannot be left (room), for its other person too (forEveryone) or not
	// (deletechat.go).
	pendingDeleteForThem
	pendingDeleteChat
	// pendingSignOutForget and pendingSignOut: the questions of signing out the account
	// a space is (leaving.space), forgetting its rooms (forEveryone) or not (signout.go).
	pendingSignOutForget
	pendingSignOut
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
	// leaving is a space being left, over several questions.
	leaving spaceLeave
	// forEveryone: a chat being deleted goes for its other person too; an account being
	// signed out takes its rooms with it.
	forEveryone bool
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
	if room.Deleting != domain.ChatLeft {
		return m.askDeleteChat(room), nil
	}
	m.confirm = confirmState{action: pendingLeave, room: room}
	return m, nil
}

// askLeaveGroup is the rail's leave: a tag is deleted, a space that one room carries
// (a Telegram forum) is left with that room, a space left with its rooms or on its own
// as leavespace.go asks, and an account's own space signs it out (signout.go), each
// after asking. A bridge's own space for an account is not left from here, and says so.
func (m Model) askLeaveGroup() (Model, tea.Cmd) {
	entry, ok := m.currentGroup()
	if !ok {
		return m, nil
	}
	if name, isTag := domain.TagOf(entry.key); isTag {
		m.confirm = confirmState{action: pendingDeleteTag, group: name}
		return m, nil
	}
	space, ok := m.spaceNamed(entry.key)
	switch {
	case !ok:
		return m, nil
	case space.Leaving == domain.LeftWithRooms, space.Leaving == domain.LeftAlone:
		return m.askLeaveSpaceWith(space), nil
	case space.Leaving == domain.LeftBySigningOut:
		return m.askSignOut(space), nil
	case space.Leaving == domain.LeftByRoom && space.LeaveBy != "":
		room, known := m.roomByID(space.LeaveBy)
		if !known {
			room = domain.Room{ID: space.LeaveBy, Name: space.DisplayName()}
		}
		m.confirm = confirmState{action: pendingLeaveSpace, room: room, group: space.DisplayName(), rooms: space.Children}
		return m, nil
	default:
		return m.say("leaving " + isolate(space.DisplayName()) +
			" from the rail is not something kith does: it is a bridge's own space for an account, which only the bridge signs out"), nil
	}
}

// spaceNamed is the space a rail row is (its key is the space's name), when exactly one
// space has that name.
func (m Model) spaceNamed(name string) (domain.Space, bool) {
	var found []domain.Space
	for _, s := range m.rooms.spaces {
		if s.DisplayName() == name {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		return domain.Space{}, false
	}
	return found[0], true
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
	case pendingLeaveSpace:
		return fmt.Sprintf("leave %s and its %s?", isolate(m.confirm.group), roomsPhrase(len(m.confirm.rooms)))
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
	case pendingLeaveSpaceRooms, pendingLeaveSharedRoom, pendingLeaveSpaceItself:
		return m.leaveSpacePrompt(m.confirm)
	case pendingDeleteForThem, pendingDeleteChat:
		return m.deleteChatPrompt(m.confirm)
	case pendingSignOutForget, pendingSignOut:
		return m.signOutPrompt(m.confirm)
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
	if next, answered := m.answerEarlierStep(pending, yes); answered {
		return next, nil
	}
	if !yes {
		m = m.say("canceled")
		return m, nil
	}
	switch pending.action {
	case pendingLeave:
		m = m.say("leaving " + m.roomName(pending.room) + "…")
		return m, m.leaveRoomCmd(pending.room.ID)
	case pendingLeaveSpace:
		m = m.say("leaving " + isolate(pending.group) + "…")
		return m, m.leaveSpaceCmd(pending.room.ID, pending.rooms)
	case pendingReject:
		m = m.say("rejecting the invitation to " + m.roomName(pending.room) + "…")
		return m, m.leaveRoomCmd(pending.room.ID)
	case pendingMarkGroupRead:
		m = m.say(fmt.Sprintf("marking %s in %s read…", roomsPhrase(len(pending.rooms)), pending.group))
		return m, m.markRoomsReadCmd(pending.rooms, pending.group)
	case pendingRedact:
		return m.redact(pending)
	case pendingKick, pendingBan:
		return m.removeMember(pending)
	case pendingJoinPlace:
		return m.joinPlace(pending)
	case pendingDeleteTag:
		return m.deleteTag(pending.group)
	case pendingCombineTags:
		return m.combineTags(pending.group, pending.address)
	case pendingLeaveSpaceItself:
		return m.leaveSpace(pending.leaving)
	case pendingDeleteChat:
		return m.deleteChat(pending)
	case pendingSignOut:
		return m.signOut(pending)
	case pendingLeaveSpaceRooms, pendingLeaveSharedRoom, pendingDeleteForThem, pendingSignOutForget, pendingNone:
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
	if msg.err != nil && msg.deleted {
		return m.sayErr("delete failed", msg.err), nil
	}
	if msg.err != nil {
		m = m.sayErr("leave failed", msg.err)
		return m, nil
	}
	m.rooms = m.rooms.without(msg.roomID)
	for _, also := range msg.also {
		m.rooms = m.rooms.without(also)
	}
	next, cmd := m.handleInvites(m.rooms.invites)
	// Said last: the open room may have gone, and opening the next one says so.
	if msg.deleted {
		next = next.say("deleted")
	} else {
		next = next.say("left")
	}
	if msg.space {
		// The space went with its room: the rail loses its row.
		return next, tea.Batch(cmd, next.refreshRoomsCmd(), next.refreshSpacesCmd())
	}
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
	case actYesAll, actNoAll:
		// Only a room also in another space is asked about with its like.
		if m.confirm.action == pendingLeaveSharedRoom {
			pending := m.confirm
			m.confirm = confirmState{}
			return m.answerSharedRoom(pending.leaving, m.keys.lookup(key.String(), scopeConfirm) == actYesAll, true), nil
		}
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

// removeMember removes or bans the person a confirmation was about.
func (m Model) removeMember(c confirmState) (Model, tea.Cmd) {
	if c.action == pendingBan {
		m = m.doing("banning " + c.personName + "…")
		return m, m.memberCmd(c.roomID, c.person, memberBan, "")
	}
	m = m.doing("removing " + c.personName + "…")
	return m, m.memberCmd(c.roomID, c.person, memberKick, "")
}
