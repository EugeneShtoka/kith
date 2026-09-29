package tui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// typingNotice is the room we told we are typing and when. Empty means nobody.
type typingNotice struct {
	room domain.RoomID
	at   time.Time
}

// liveActivity is who is typing in each room. Kept for every room because the stream
// carries every room and there is no request to re-fetch it on entry.
type liveActivity struct {
	typists map[domain.RoomID][]string
}

func newLiveActivity() liveActivity {
	return liveActivity{typists: map[domain.RoomID][]string{}}
}

// handleActivity folds one room's activity in. Typing carries the whole set, so it is
// assigned rather than merged; other people's read positions are deliberately dropped.
func (m Model) handleActivity(msg activityMsg) (Model, tea.Cmd) {
	a := msg.a
	if len(a.Typing) == 0 {
		m.live.typists = withoutEntry(m.live.typists, a.RoomID)
	} else {
		m.live.typists = withEntry(m.live.typists, a.RoomID, a.Typing)
	}
	return m, m.listenActivityCmd()
}

// typingNote is what the composer divider says about who is typing in the open room,
// or "" when nobody is or the setting is off. Past two people it becomes a count.
func (m Model) typingNote() string {
	if !m.prefs.display.ShowTyping() {
		return ""
	}
	who := m.live.typists[m.openRoom]
	switch len(who) {
	case 0:
		return ""
	case 1:
		return m.personName(who[0]) + " is typing"
	case 2:
		return m.personName(who[0]) + " and " + m.personName(who[1]) + " are typing"
	default:
		return strconv.Itoa(len(who)) + " people are typing"
	}
}

// personName resolves an MXID to a display name: identity alias, then the room's
// member list, then the newest loaded message they sent, then their localpart.
func (m Model) personName(mxid string) string {
	for i := range m.timeline.members {
		if m.timeline.members[i].UserID == mxid && m.timeline.members[i].DisplayName != "" {
			return m.personIn(m.openRoom, mxid, m.timeline.members[i].DisplayName)
		}
	}
	for i := range slices.Backward(m.timeline.messages) {
		if m.timeline.messages[i].Sender == mxid && m.timeline.messages[i].SenderName != "" {
			return m.senderName(m.timeline.messages[i])
		}
	}
	return m.personIn(m.openRoom, mxid, "")
}

// The refresh must be comfortably shorter than the homeserver timeout or the indicator
// blinks off between keystrokes (Element's numbers).
const (
	typingTimeout = 15 * time.Second
	typingRefresh = 10 * time.Second
)

// noteTyping tells the open room you are typing, if it has not been told recently.
// The notice expires server-side, so no timer is needed; an empty composer stops it.
func (m Model) noteTyping() (Model, tea.Cmd) {
	if !m.prefs.display.SendsTyping() {
		return m, nil
	}
	room, ok := m.currentRoom()
	if !ok || room.IsInvite() {
		return m, nil
	}
	if strings.TrimSpace(m.compose.input) == "" {
		return m.stopTyping()
	}
	// Moved rooms mid-sentence: tell the old room we stopped.
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

// stopTyping cancels the notice, if one is out.
func (m Model) stopTyping() (Model, tea.Cmd) {
	if m.sentTyping.room == "" {
		return m, nil
	}
	room := m.sentTyping.room
	m.sentTyping.room, m.sentTyping.at = "", time.Time{}
	return m, m.sendTypingCmd(room, false)
}
