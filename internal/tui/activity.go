package tui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// typingNotice is the room told we are typing, and when. Empty means none.
type typingNotice struct {
	room domain.RoomID
	at   time.Time
}

// liveActivity is who is typing in each room, as kithd named them. Kept for every
// room: the stream carries all rooms and nothing re-fetches it on entry.
type liveActivity struct {
	typists map[domain.RoomID]domain.Activity
}

func newLiveActivity() liveActivity {
	return liveActivity{typists: map[domain.RoomID]domain.Activity{}}
}

// handleActivity folds one room's activity in. Typing is the whole set, so it is
// assigned, not merged; other people's read positions are dropped.
func (m Model) handleActivity(msg activityMsg) (Model, tea.Cmd) {
	a := msg.a
	if len(a.Typing) == 0 {
		m.live.typists = withoutEntry(m.live.typists, a.RoomID)
	} else {
		m.live.typists = withEntry(m.live.typists, a.RoomID, a)
	}
	return m, m.listenActivityCmd()
}

// typingNote is the composer divider's note on who is typing, or "" when nobody is
// or the setting is off. Past two people it becomes a count.
func (m Model) typingNote() string {
	if !m.prefs.device.ShowTyping() {
		return ""
	}
	a := m.live.typists[m.openRoom]
	switch len(a.Typing) {
	case 0:
		return ""
	case 1:
		return m.typistName(a, 0) + " is typing"
	case 2:
		return m.typistName(a, 0) + " and " + m.typistName(a, 1) + " are typing"
	default:
		return strconv.Itoa(len(a.Typing)) + " people are typing"
	}
}

// typistName is the i-th typist as kithd named them, shaped by the room's space rule and
// isolated for a sentence.
func (m Model) typistName(a domain.Activity, i int) string {
	known := ""
	if i < len(a.TypingKnownAs) {
		known = a.TypingKnownAs[i]
	}
	return isolate(m.processedName(domain.Message{RoomID: a.RoomID, Sender: a.Typing[i], SenderKnownAs: known}))
}

// personName resolves an MXID to a display name, isolated for a sentence (knownName).
func (m Model) personName(mxid string) string {
	return isolate(m.knownName(mxid, m.openRoom))
}

// knownName is someone's label in room, as the sender column writes it: kithd's name for
// them from the open room's member list, the newest loaded message or as kithd was asked
// (named.go), then localpart or number.
func (m Model) knownName(mxid string, room domain.RoomID) string {
	for i := range m.timeline.members {
		if member := m.timeline.members[i]; member.UserID == mxid && (member.DisplayName != "" || member.KnownAs != "") {
			return m.processedName(domain.Message{RoomID: room, Sender: mxid, SenderName: member.DisplayName, SenderKnownAs: member.KnownAs})
		}
	}
	for i := range slices.Backward(m.timeline.messages) {
		if msg := m.timeline.messages[i]; msg.Sender == mxid && (msg.SenderName != "" || msg.SenderKnownAs != "") {
			return m.processedName(msg)
		}
	}
	return m.processedName(domain.Message{RoomID: room, Sender: mxid, SenderKnownAs: m.named[mxid]})
}

// The refresh must be well under the homeserver timeout or the indicator blinks off
// between keystrokes (Element's numbers).
const (
	typingTimeout = 15 * time.Second
	typingRefresh = 10 * time.Second
)

// noteTyping tells the open room you are typing, unless told recently. The notice
// expires server-side, so no timer; an empty composer stops it.
func (m Model) noteTyping() (Model, tea.Cmd) {
	if !m.prefs.device.SendsTyping() {
		return m, nil
	}
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	if strings.TrimSpace(m.compose.input) == "" {
		return m.stopTyping()
	}
	// Changed rooms mid-sentence: tell the old one we stopped.
	var cmds []tea.Cmd
	if m.sentTyping.room != "" && m.sentTyping.room != room.ID {
		cmds = append(cmds, m.sendTypingCmd(m.sentTyping.room, false))
		m.sentTyping.room, m.sentTyping.at = "", time.Time{}
	}
	if m.sentTyping.room == room.ID && time.Since(m.sentTyping.at) < typingRefresh {
		return m, tea.Batch(cmds...)
	}
	m.sentTyping.room, m.sentTyping.at = room.ID, time.Now()
	cmds = append(cmds, m.sendTypingCmd(room.ID, true))
	return m, tea.Batch(cmds...)
}

func (m Model) stopTyping() (Model, tea.Cmd) {
	if m.sentTyping.room == "" {
		return m, nil
	}
	room := m.sentTyping.room
	m.sentTyping.room, m.sentTyping.at = "", time.Time{}
	return m, m.sendTypingCmd(room, false)
}
