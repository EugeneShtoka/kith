package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Invite and unban take a typed MXID (the person is not in the member list); kick and ban act
// on the people list. All but invite ask first.

// openInvite asks who to invite to the room under the cursor.
func (m Model) openInvite() (Model, tea.Cmd) { return m.openMemberPrompt(promptInvite) }

func (m Model) openMemberPrompt(kind promptKind) (Model, tea.Cmd) {
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	m.aimedAt.member = room.ID
	return m.openPrompt(kind), nil
}

func (m Model) submitInvite(input string) (Model, tea.Cmd) {
	return m.submitMember(input, memberInvite, "inviting ")
}

func (m Model) submitUnban(input string) (Model, tea.Cmd) {
	return m.submitMember(input, memberUnban, "unbanning ")
}

func (m Model) submitMember(input string, act memberAction, doing string) (Model, tea.Cmd) {
	room, mxid, ok := m.memberTarget(input)
	// Cleared either way so a later prompt cannot inherit the room.
	m.aimedAt.member = ""
	if !ok {
		return m, nil
	}
	m = m.doing(doing + mxid + "…")
	return m, m.memberCmd(room, mxid, act, "")
}

// memberTarget validates input against the room the prompt was opened for. The MXID shape is
// checked because "@dana" otherwise gets an opaque homeserver error.
func (m Model) memberTarget(input string) (domain.RoomID, string, bool) {
	room := m.aimedAt.member
	mxid := domain.TypedUserID(input)
	if room == "" || !domain.IsMatrixUserID(mxid) {
		return "", "", false
	}
	return room, mxid, true
}

// askKickSelected and askBanSelected name the person, since the filtered row under the cursor
// may not be the one looked at.
func (m Model) askKickSelected() (Model, tea.Cmd) { return m.askAboutPerson(pendingKick) }

func (m Model) askBanSelected() (Model, tea.Cmd) { return m.askAboutPerson(pendingBan) }

func (m Model) askAboutPerson(what pendingAction) (Model, tea.Cmd) {
	item, ok := m.picker.selected()
	if !ok {
		return m, nil
	}
	room, ok := m.currentRoom()
	if !ok {
		return m, nil
	}
	if m.isMe(item.value) {
		m = m.say("that is you — use the leave key instead")
		return m, nil
	}
	m = m.closePicker()
	m.confirm = confirmState{action: what, person: item.value, personName: isolate(item.label), roomID: room.ID}
	return m, nil
}

// handleMemberChanged reports the outcome and, except for an invite, re-reads membership so
// the people list and mention completion drop a removed person.
func (m Model) handleMemberChanged(msg memberChangedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m = m.sayErr("could not "+msg.action.present()+" "+msg.person, msg.err)
		return m, nil
	}
	m = m.say(msg.action.past() + " " + msg.person)
	room, ok := m.currentRoom()
	if !ok || msg.action == memberInvite {
		return m, nil
	}
	return m, m.refreshMembersCmd(room.ID)
}
