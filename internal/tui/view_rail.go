package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// renderRail is the space rail, railWidth wide and h tall.
func (m Model) renderRail(h int) string {
	const w = railWidth
	active := m.focus == paneRail
	lines := make([]string, 0, len(m.rail.groups))
	// The cursor's row, not its index: dividers take rows too.
	cursorRow := 0
	for i, g := range m.rail.groups {
		selected := i == m.rail.cursor
		if selected {
			cursorRow = len(lines)
		}
		lines = append(lines, m.railRow(g, selected, active, w-paneBorder))
		if g.sepAfter {
			lines = append(lines, m.theme.Muted.Render(railDivider(w)))
		}
	}
	return m.framePane("Spaces", windowRows(lines, cursorRow, paneBodyRows(h)), w, h, active)
}

// railRow renders one rail entry: marker, label, and the group's unread count.
func (m Model) railRow(g group, selected, active bool, inner int) string {
	badge, highlight := m.groupBadge(g)
	return m.listRow(rowLabel{name: g.label}, badge, highlight, inner, selected, active)
}

// listRow lays out one rail or room-list row across the pane's interior: marker, label
// flush left, unread badge right-aligned, with the row style filling the width. The
// badge keeps its columns and the name is ellipsized instead, so a long name never
// hides the count.
func (m Model) listRow(label rowLabel, badge string, highlight bool, inner int, selected, active bool) string {
	style := m.theme.PaneRow(selected, active)
	marker := m.theme.Marker(selected, active).Render(cursor(selected))
	inner -= cursorWidth
	lead, trail := label.lead, label.trail
	if lead != "" {
		lead += " "
	}
	if trail != "" {
		trail = " " + trail
	}
	room := ansi.StringWidth(lead) + ansi.StringWidth(trail)
	// One column between name and count, so a long name never abuts its own badge.
	space := inner - ansi.StringWidth(badge) - 1
	if badge == "" || space < 1 {
		return marker + style.Width(inner).Render(lead+nameCell(label.name, inner-room)+trail)
	}
	drawn := lead + nameCell(label.name, space-room) + trail
	pad := strings.Repeat(" ", max(space-ansi.StringWidth(drawn)+1, 0))
	return marker + style.Render(drawn+pad) +
		m.theme.OnPaneRow(m.theme.Badge(highlight), selected, active).Render(badge)
}

// rowLabel is what a list row says: lead is an affordance that stays on the visual
// left, name is the logical name (drawn by listRow via nameCell), and trail follows the
// name and survives truncation.
type rowLabel struct {
	lead  string
	name  string
	trail string
}

// groupBadge is a rail group's summed unread count ("" when none) and whether any of it
// highlights the user, unrendered so the row can paint it onto its background.
func (m Model) groupBadge(g group) (badge string, highlight bool) {
	notifications, highlights := m.groupUnread(g)
	if notifications == 0 {
		return "", false
	}
	return fmt.Sprintf("●%d", notifications), highlights > 0
}

// railDivider is a horizontal separator spanning the rail's interior width
// (pane width minus its two border columns).
func railDivider(w int) string {
	if n := w - paneBorder; n > 0 {
		return strings.Repeat("─", n)
	}
	return ""
}

func (m Model) renderRooms(w, h int) string {
	active := m.focus == paneRooms
	rows := m.roomRows()
	cursor := m.rowCursor(rows)
	body := paneBodyRows(h)
	// Style only the visible window; styling every row made keypresses O(rooms).
	first := windowStart(cursor, body, len(rows))
	last := min(first+body, len(rows))
	lines := make([]string, 0, last-first)
	if len(rows) == 0 {
		lines = append(lines, m.theme.Muted.Render("(no rooms)"))
	}
	for i := first; i < last; i++ {
		lines = append(lines, m.roomListLine(rows[i], w-paneBorder, i == cursor, active))
	}
	title := isolate(m.rail.label()) // logical: framePane draws it
	// A list longer than the pane shows the cursor position.
	if len(rows) > body {
		title += fmt.Sprintf("  %d/%d", cursor+1, len(rows))
	}
	return m.framePane(title, lines, w, h, active)
}

// roomListLine draws one room-list row — a room, a thread under it, or the count of
// threads the cap left out — all through listRow.
func (m Model) roomListLine(r roomRow, inner int, selected, active bool) string {
	switch {
	case r.more > 0:
		return m.listRow(rowLabel{lead: m.prefs.display.Threads.RowMark(), name: m.moreThreadsLabel(r.more)},
			"", false, inner, false, active)
	case r.isThread():
		badge := ""
		if r.thread.Unread > 0 {
			badge = fmt.Sprintf("●%d", r.thread.Unread)
		}
		return m.listRow(rowLabel{lead: m.prefs.display.Threads.RowMark(), name: m.threadRowLabel(r.thread)},
			badge, r.thread.Mentions > 0, inner, selected, active)
	}
	label := rowLabel{name: m.roomLabelHere(r.room)}
	switch {
	case r.room.IsInvite():
		// Marked, so an invitation never reads as a room you can just open.
		label.lead = inviteMark
	case r.room.Replacement != "":
		// A replaced room looks ordinary but rejects sends; the mark says it is over.
		label.lead = replacedMark
	}
	// A held draft is marked right after the name, so it neither shifts names nor reads
	// as part of the badge.
	if m.hasDraft(r.room.ID) {
		label.trail = draftMark
	}
	badge, highlight := m.unreadBadge(r.room)
	return m.listRow(label, badge, highlight, inner, selected, active)
}

// Row marks: an invitation, a room upgraded into another, a room holding a draft.
const (
	inviteMark   = "✉"
	replacedMark = "→"
	draftMark    = "✎"
)

// unreadBadge is a room's unread marker and whether it highlights the user. Archived
// rooms show "·N" untinted; a room marked unread with nothing new shows a bare "●".
func (m Model) unreadBadge(room domain.Room) (badge string, highlight bool) {
	view := m.unreadView()
	n, highlights := view.count(room)
	if n == 0 {
		if view.marked(room) {
			return "●", false
		}
		return "", false
	}
	if view.isArchived(room) {
		return fmt.Sprintf("·%d", n), false
	}
	return fmt.Sprintf("●%d", n), highlights > 0
}
