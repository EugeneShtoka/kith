package tui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The message-history view takes the timeline pane rather than expanding a row, and
// shows edits and the deletion together.

// historyState is the open history view.
type historyState struct {
	open bool
	msg  domain.Message
	revs []domain.Revision
	// deletion comes with the versions: the redaction time lives on the redaction event.
	deletion domain.Deletion
	loading  bool
	err      error
	scroll   int
}

// active reports whether the history has the pane.
func (h historyState) active() bool { return h.open }

// openHistory shows what the selected message has been through.
func (m Model) openHistory() (Model, tea.Cmd) {
	msg, ok := m.selectedMessage()
	if !ok {
		return m, nil
	}
	m.history = historyState{open: true, msg: msg, loading: true}
	return m, m.messageHistoryCmd(msg.RoomID, msg.ID)
}

// closeHistory puts the conversation back.
func (m Model) closeHistory() (Model, tea.Cmd) {
	m.history = historyState{}
	return m, nil
}

// handleHistory files the answer if it is still for the message being viewed.
func (m Model) handleHistory(msg historyMsg) (Model, tea.Cmd) {
	if !m.history.open || m.history.msg.ID != msg.eventID {
		return m, nil
	}
	m.history.loading = false
	m.history.revs, m.history.deletion, m.history.err = msg.revisions, msg.deletion, msg.err
	return m, nil
}

// scrollHistory moves through a long history, clamped at both ends.
func (m Model) scrollHistory(delta int) (Model, tea.Cmd) {
	m.history.scroll += delta
	if m.history.scroll < 0 {
		m.history.scroll = 0
	}
	return m, nil
}

// historyTitle names the pane: who wrote the message, and how much of it there is.
func (m Model) historyTitle() string {
	who := m.senderName(m.history.msg)
	switch {
	case m.history.loading:
		return who + " · reading the history…"
	case len(m.history.revs) > 1:
		return who + " · " + strconv.Itoa(len(m.history.revs)) + " versions"
	default:
		return who + " · message history"
	}
}

// historyLines renders the versions, newest last, with what happened to the message
// at the end of them.
func (m Model) historyLines(width, height int) []string {
	lines := make([]string, 0, height)
	switch {
	case m.history.loading:
		lines = append(lines, m.theme.Muted.Render("  reading what this message used to say…"))
	case m.history.err != nil:
		lines = append(lines, m.theme.Muted.Render("  could not read the history: "+m.history.err.Error()))
	case len(m.history.revs) == 0:
		if m.history.msg.Edited {
			lines = append(lines,
				m.theme.Muted.Render("  this message was edited before its versions were being kept,"),
				m.theme.Muted.Render("  and the server has nothing left to say about it"))
		} else {
			lines = append(lines, m.theme.Muted.Render("  this message has never been edited"))
		}
	default:
		lines = append(lines, m.versionLines(width)...)
	}
	if end := m.deletionLine(); end != "" {
		lines = append(lines, "", end)
	}
	if att := m.historyAttachment(width); att != "" {
		lines = append(lines, "", att)
	}
	return window(lines, m.history.scroll, height)
}

// versionLines is one block per version: its number, its time, what changed, and the
// words themselves.
func (m Model) versionLines(width int) []string {
	lines := make([]string, 0, len(m.history.revs)*3)
	for i, rev := range m.history.revs {
		head := "  " + strconv.Itoa(i+1) + "  " + m.prefs.clock.ShortDate(rev.At) + " " + m.prefs.clock.TimeSeconds(rev.At) + "  "
		if i == 0 {
			head += "sent"
		} else {
			head += "edited" + changeNote(m.history.revs[i-1].Body, rev.Body)
		}
		lines = append(lines, m.theme.Title.Render(head))
		// Through readerLines so an RTL version is reordered and flushed right like the
		// timeline, not drawn backwards.
		body := strings.Split(rev.Body, "\n")
		for k := range body {
			body[k] = "     " + body[k]
		}
		lines = append(lines, m.readerLines(body, width-1, nil)...)
		if i < len(m.history.revs)-1 {
			lines = append(lines, "")
		}
	}
	return lines
}

// deletionLine is the deletion as the last row of the sequence, with its time when the
// fetch reached the server (an empty time column otherwise) and the timeline's wording.
func (m Model) deletionLine() string {
	if !m.history.msg.Redacted {
		return ""
	}
	said := strings.TrimSuffix(strings.TrimPrefix(m.deletedBody(m.deletedMessage()), "("), ")")
	said = drawSentence(said)
	when := "  ·  "
	if at := m.deletionTime(); !at.IsZero() {
		when = "  " + m.prefs.clock.ShortDate(at) + " " + m.prefs.clock.TimeSeconds(at) + "  "
	}
	return m.theme.Title.Render("  ✕" + when + said)
}

// historyAttachment is what the message had attached, and the key that shows it: the
// only place a deleted message's attachment is shown.
func (m Model) historyAttachment(width int) string {
	if m.history.msg.Media == nil {
		return ""
	}
	line := "  📎 " + mediaChip(m.history.msg.Media) + "  ·  " + m.keys.keyHint(scopeTimeline, actViewMedia) + " opens it"
	return m.theme.Muted.Render(drawLine(line, lineSpec{width: max(width-1, 1), sentence: true}))
}

// viewHistoryMedia opens the attachment of the message whose history is open, a
// deleted message's included: a picture in the viewer, alone and at full size; a
// video in the player; anything else with the desktop's handler.
func (m Model) viewHistoryMedia() (Model, tea.Cmd) {
	msg := m.history.msg
	switch {
	case msg.Media == nil:
		return m.say("nothing was attached to this message"), nil
	case msg.Media.IsImage():
		viewer, err := m.viewerCommand()
		if err != nil {
			return m.say(err.Error()), nil
		}
		configured := strings.TrimSpace(m.prefs.display.Media.Viewer) != ""
		return m.doing("opening the picture…"), m.viewMediaCmd(viewer, configured, []mediaJob{m.jobFor(msg)}, 0)
	case msg.Media.IsVideo():
		return m.playVideo(msg)
	default:
		return m.openFile(msg)
	}
}

// deletedMessage folds in the remover the history fetch learned about.
func (m Model) deletedMessage() domain.Message {
	msg := m.history.msg
	if m.history.deletion.By != "" {
		msg.RedactedBy, msg.RedactedReason = m.history.deletion.By, m.history.deletion.Reason
	}
	return msg
}

// deletionTime is what the fetch found, else what the row already carried.
func (m Model) deletionTime() time.Time {
	if !m.history.deletion.At.IsZero() {
		return m.history.deletion.At
	}
	return m.history.msg.RedactedAt
}

// changeNote is how much a version changed, in runes added and removed.
func changeNote(before, after string) string {
	added, removed := len([]rune(after))-len([]rune(before)), 0
	if added < 0 {
		added, removed = 0, -added
	}
	switch {
	case added > 0 && removed > 0:
		return "  +" + strconv.Itoa(added) + " −" + strconv.Itoa(removed)
	case added > 0:
		return "  +" + strconv.Itoa(added)
	case removed > 0:
		return "  −" + strconv.Itoa(removed)
	}
	return "  reworded"
}

// window is the visible slice of a rendered list, clamped so scrolling past the end
// lands on the last screenful rather than on blank space.
func window(lines []string, scroll, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	if scroll > len(lines)-height {
		scroll = len(lines) - height
	}
	if scroll < 0 {
		scroll = 0
	}
	return lines[scroll : scroll+height]
}

// historyScrollAll is further than any history: both ends clamp.
const historyScrollAll = 1 << 20

// handleHistoryKey owns every key while the view is open (no cursor moves behind it),
// routed through scopeNav so rebinding applies.
func (m Model) handleHistoryKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	press := key.String()
	// The quit key closes it, as it closes a pager; so does back (esc by default).
	if m.keys.lookup(press, scopeCommand) == actQuit {
		return m.closeHistory()
	}
	act := m.keys.lookup(press, scopeNav)
	if act == actBack {
		return m.closeHistory()
	}
	if m.keys.lookup(press, scopeTimeline) == actViewMedia {
		return m.viewHistoryMedia()
	}
	if delta, ok := navDelta(act, m.take(), max(m.height-1, 1), historyScrollAll); ok {
		return m.scrollHistory(delta)
	}
	return m, nil
}

// navDelta is how far one motion moves a plain scrolled list, for the panes that are
// one: a count repeats a line step, a page is the pane's height, and an end is all
// (signed toward the newest end, so a newest-first list passes it negative).
// ok is false for an action that is not a motion.
func navDelta(act action, count, page, all int) (delta int, ok bool) {
	switch act {
	case actDown:
		return count, true
	case actUp:
		return -count, true
	case actHalfPageDown:
		return max(page/2, 1), true
	case actHalfPageUp:
		return -max(page/2, 1), true
	case actPageDown:
		return page, true
	case actPageUp:
		return -page, true
	case actSelectOldest, actScrollOldest:
		return -all, true
	case actSelectNewest, actScrollNewest:
		return all, true
	}
	return 0, false
}
