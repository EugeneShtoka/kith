package tui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// inviteLines renders the decision an invitation asks for: who invited you, and the
// two answers.
func (m Model) inviteLines(room domain.Room, width, rows int) []string {
	lines := []string{
		"",
		m.theme.Title.Render("You have been invited to"),
		"  " + displayName(m.roomLabel(room)),
	}
	if room.InvitedBy != "" {
		lines = append(lines, "", m.theme.Title.Render("by"), "  "+m.inviterLabel(room.InvitedBy))
	}
	if room.IsDirect {
		lines = append(lines, "", m.theme.Muted.Render("a direct message"))
	}
	accept := m.keys.keyHint(scopeRooms, actAccept)
	reject := m.keys.keyHint(scopeRooms, actReject)
	lines = append(lines, "", m.hintLine(
		keyed(accept, "accept"),
		keyed(reject, "reject"),
		note("in the room list"),
	))
	for i := range lines {
		lines[i] = clamp(lines[i], width)
	}
	if len(lines) > rows {
		return lines[:rows]
	}
	return lines
}

// inviterLabel names an inviter with the timeline's identity resolution, in their hue.
func (m Model) inviterLabel(mxid string) string {
	label := drawSentence(m.personIn(m.openRoom, mxid, m.inviterName(mxid)))
	if protocol := domain.ProtocolOf(mxid); protocol.IsBridged() {
		label += " " + m.theme.Muted.Render("("+protocol.String()+")")
	}
	return label
}

// inviterName resolves a display name for an inviter we may never have seen post:
// their configured alias if they have one, else the bare MXID.
func (m Model) inviterName(mxid string) string {
	if ident, ok := m.prefs.identities[mxid]; ok && ident.alias != "" {
		return ident.alias
	}
	return mxid
}

func (m Model) renderTimeline(w, h int) string {
	active := m.focus == paneTimeline
	inner := w - paneBorder

	// Search results, history, an invitation or a thread take the pane over.
	if m.search.active {
		// The completion popup is drawn here too: while a search prompt is open the
		// composer branch below is never reached.
		popup := m.completionLines(inner)
		lines := m.searchLines(inner, h-paneBorder-len(popup))
		lines = append(lines, popup...)
		return m.framePane(m.searchTitle(), lines, w, h, active)
	}

	if m.history.active() {
		return m.framePane(m.historyTitle(), m.historyLines(inner, h-paneBorder), w, h, active)
	}

	title := "(no room)"
	if room, ok := m.currentRoom(); ok {
		title = m.roomName(room)
		if room.IsInvite() {
			return m.framePane(inviteMark+" "+title, m.inviteLines(room, inner, h-paneBorder), w, h, active)
		}
	}
	if m.thread.open() {
		title = threadMark + " " + m.threadTitle()
	}

	title = m.decoratedTitle(title, inner)

	// Body layout: the message area, then a divider, then the composer. The
	// message-area height matches msgAreaRows so scroll math and rendering agree.
	rows := m.msgAreaRows()
	lines := make([]string, 0, rows+2)
	// One popup slot: the spell walk, else completion, else the model's continuations.
	popup := m.completionLines(inner)
	if walk := m.spellWalkLines(inner); len(walk) > 0 {
		popup = walk
	}
	if len(popup) == 0 {
		popup = m.modelOptionLines(inner)
	}
	// The command line's menu goes below the composer, next to the status-line field it
	// completes; every other popup belongs beside the caret.
	belowComposer := m.prompt.kind == promptCommand
	switch {
	case m.picker.active():
		// A chooser takes the message area but leaves the composer visible.
		lines = append(lines, m.pickerLines(inner, rows)...)
	default:
		// The popup covers the oldest visible messages, not the newest.
		lines = append(lines, m.messageLines(rows-len(popup), inner)...)
		if !belowComposer {
			lines = append(lines, popup...)
		}
	}
	lines = append(lines, m.theme.Muted.Render(m.composerDivider(inner)))
	lines = append(lines, m.composerLines(active)...)
	if belowComposer {
		lines = append(lines, popup...)
	}

	return m.framePane(title, lines, w, h, active)
}

// primeTimeline decides the open room's side (settleLayout) and makes, on the Update
// goroutine, every write to the shared cache the next frame would make: the derived answers (which assign colors), the running row
// totals and the rendered rows of the window. View then only reads. The window is the
// whole message area; a popup only shortens what View draws of it.
func (m Model) primeTimeline() Model {
	// Before anything is drawn: the side is part of the derived key.
	m = m.settleLayout()
	if !m.ready || m.width < minWidth || m.height < minHeight {
		_ = m.derivedFor() // not an error: called for the cache it fills
		return m           // View draws no timeline
	}
	m = m.anchorBelow()
	d := m.derivedFor()
	drops := d.drops
	_ = m.stickyDay()
	_, _ = m.layoutWindow(m.timeline.scroll, m.msgAreaRows())
	if d.drops != drops {
		// A row changed height (a reaction, a picture) after the totals placed the
		// window, which dropped them: place it again, so View finds them final.
		_ = m.stickyDay()
		_, _ = m.layoutWindow(m.timeline.scroll, m.msgAreaRows())
	}
	return m
}

// anchorBelow keeps the rows on screen where they are when a message below them, in
// the scrolled-past part, grows or shrinks (a reaction, a picture, a thread summary):
// it re-renders each such message and moves the scroll by the difference, as a live
// message does. Nothing drew those rows, so nothing else would notice until the view
// jumped the next time one was drawn.
func (m Model) anchorBelow() Model {
	if m.timeline.scroll <= 0 || m.derived == nil || m.derived.inputsRev == m.derived.anchoredRev {
		return m
	}
	d, w := m.walkFor()
	d.anchoredRev = d.inputsRev
	delta := 0
	// suffix[i] is where message i's top edge sits, counted up from the newest row;
	// wholly below the window while that is at most scroll.
	// Bounds fixed first: re-rendering a message drops the totals below it, but their
	// old values are still the ones to compare against.
	from, scroll := d.suffixFrom, m.timeline.scroll
	for i := len(w.msgs) - 1; i >= from && d.suffix[i] <= scroll; i-- {
		e := &d.rowCache[i]
		if e.current(m.entryInputs(w, i)) {
			continue
		}
		before := e.height
		delta += m.heightOf(w, i) - before
	}
	m.timeline.scroll = max(m.timeline.scroll+delta, 0)
	return m
}

// keepAnchored applies change with a scrolled-up view held still: the message at the
// window's bottom edge keeps its place on screen, whatever the change added above or
// below it. The scroll offset counts rows up from the newest, so the anchor's offset
// is its running total (suffix), and only the rows below it are measured, not the room.
func (m Model) keepAnchored(change func(Model) Model) Model {
	if m.timeline.scroll <= 0 {
		return change(m)
	}
	d, w := m.walkFor()
	if len(w.msgs) == 0 {
		return change(m)
	}
	edge := m.timeline.scroll + 1
	m.extendTo(w, edge)
	i := min(max(d.startFor(edge), d.suffixFrom), len(w.msgs)-1)
	anchor, above := w.msgs[i].ID, d.suffix[i]-m.timeline.scroll

	m = change(m)
	d, w = m.walkFor()
	j, ok := d.index[anchor]
	if !ok {
		return m // the anchor is gone (a thread closed, a message hidden)
	}
	m.extendToIndex(w, j)
	m.timeline.scroll = max(d.suffix[j]-above, 0)
	return m
}

// stickyDay is the selected message's day while it is on screen, else the day of the
// topmost visible row (typing scrolls the view while the cursor stays put).
func (m Model) stickyDay() string {
	rows := m.msgAreaRows()
	if msg, ok := m.selectedMessage(); ok && !msg.Timestamp.IsZero() {
		if from, to, found := m.selectionRows(); found && to > m.timeline.scroll && from < m.timeline.scroll+rows {
			return dayLabel(msg.Timestamp)
		}
	}
	if msg, ok := m.messageAtRow(m.timeline.scroll + rows - 1); ok && !msg.Timestamp.IsZero() {
		return dayLabel(msg.Timestamp)
	}
	return ""
}

// msgAreaRows is the timeline's message rows: the frame less the status line, borders,
// header, divider and the composer's current height. Shared by rendering and scrolling.
func (m Model) msgAreaRows() int {
	return max(m.height-5-m.composerRows(), 1)
}

// contentWidth is the width message rows wrap to: the timeline pane's interior less
// the gutter. Shared by layout and scroll math.
func (m Model) contentWidth() int {
	return max(m.width-railWidth-roomsWidth-paneBorder-m.gutterWidth(), 1)
}

// gutterWidth is the row-number column's width, taken out of the content width so
// layout and scroll math agree.
func (m Model) gutterWidth() int {
	if !m.prefs.display.RowNumbers {
		return 0
	}
	return gutterCells
}

// gutterCells is two digits and a space; fixed so scrolling never shifts the text.
const gutterCells = 3

// messageLines renders exactly rows lines of the timeline ending m.scroll rows back
// from the newest. A short room pads above, next to the composer; a thread reads from
// its root, so it pads below.
func (m Model) messageLines(rows, width int) []string {
	rows = max(rows, 1)
	visible, owners := m.layoutWindow(m.timeline.scroll, rows)
	visible = m.numbered(visible, owners, m.selectedIndex())
	body := make([]string, len(visible))
	for i, r := range visible {
		body[i] = clamp(r, width)
	}
	pad := make([]string, max(rows-len(visible), 0))
	if m.thread.open() {
		return append(body, pad...)
	}
	return append(pad, body...)
}

// numbered prefixes the row-number gutter. Numbers count messages (what j/k move in),
// on each message's first row only; the cursor's message is marked "▸".
func (m Model) numbered(rows []string, owners []int, cursor int) []string {
	if m.gutterWidth() == 0 || len(owners) != len(rows) {
		return rows
	}
	if cursor < 0 {
		// No cursor: keep the column so the text does not move when one arrives.
		return m.blankGutter(rows)
	}
	out := make([]string, len(rows))
	last := noOwner
	for i, row := range rows {
		owner := owners[i]
		label := ""
		if owner != noOwner && owner != last {
			label = gutterLabel(owner - cursor)
		}
		last = owner
		out[i] = m.theme.Muted.Render(fmt.Sprintf("%*s ", gutterCells-1, label)) + row
	}
	return out
}

// blankGutter is the column with nothing in it, for a pane with no cursor.
func (m Model) blankGutter(rows []string) []string {
	out := make([]string, len(rows))
	pad := strings.Repeat(" ", gutterCells)
	for i, row := range rows {
		out[i] = pad + row
	}
	return out
}

// gutterLabel is one row's cell: the unsigned distance, or "~" past two digits.
func gutterLabel(distance int) string {
	switch {
	case distance == 0:
		return "▸"
	case distance < 0:
		distance = -distance
	}
	if distance > 99 {
		return "~"
	}
	return strconv.Itoa(distance)
}

// walkFor assembles everything a layout needs that does not depend on which rows
// it is going to draw, and readies the row cache for this width.
func (m Model) walkFor() (*derivedCache, walk) {
	derived := m.derivedFor()
	w := m.walkOver(derived)
	derived.prepareRows(w.width, derived.nameW)
	return derived, w
}

// walkOver is the per-frame walk over derived's messages. Name column and colors come
// from every loaded message (cached per revision), so they do not change as you scroll.
func (m Model) walkOver(derived *derivedCache) walk {
	return walk{
		msgs: derived.shown, derived: derived, byAnchor: derived.byAnchor,
		colors: derived.colors, width: m.contentWidth(), nameW: derived.nameW,
		sel:       m.selectedID(),
		highlight: m.focus == paneTimeline && !m.compose.insertMode,
	}
}

// topAnchorRows are summaries of threads whose roots are not loaded, drawn only when
// the layout reaches the top.
func (m Model) topAnchorRows(w walk) []string {
	top := w.byAnchor[""]
	if len(top) == 0 {
		return nil
	}
	rows := make([]string, 0, len(top))
	for i := range top {
		rows = append(rows, m.threadRow(top[i], w.nameW, w.width))
	}
	return rows
}

// scrollLimit is how far back a scroll to target may go and whether the top of the
// history is reached: exact once the oldest message is reached, otherwise a lower bound
// never below target.
func (m Model) scrollLimit(target, rows int) (limit int, exhausted bool) {
	derived, w := m.walkFor()
	if len(w.msgs) == 0 {
		return 0, true
	}
	m.extendTo(w, target+rows)
	exhausted = derived.suffixFrom == 0
	total := derived.suffix[derived.suffixFrom]
	return max(total-rows, 0), exhausted
}

// selectionRows is where the selected message sits, measured in rows up from the
// newest: the row its bottom rests on, and the row above its top. Its date divider
// is not included — scrolling to a message should not scroll to the day heading
// above it.
func (m Model) selectionRows() (from, to int, ok bool) {
	derived, w := m.walkFor()
	i, found := derived.index[m.selectedID()]
	if !found {
		return 0, 0, false
	}
	m.extendToIndex(w, i)
	e := m.entryFor(w, i)
	from = derived.suffix[i+1]
	return from, from + len(e.rows), true
}

// messageAtRow is the message whose rows cover the given offset up from the
// newest, or false when the timeline is empty.
func (m Model) messageAtRow(offset int) (domain.Message, bool) {
	derived, w := m.walkFor()
	if len(w.msgs) == 0 {
		return domain.Message{}, false
	}
	m.extendTo(w, max(offset, 0)+1)
	i := derived.startFor(max(offset, 0) + 1)
	return w.msgs[min(max(i, 0), len(w.msgs)-1)], true
}

// layoutWindow is the count rows sitting skip rows above the newest, and each row's
// message index (noOwner for dividers and anchors). Cost is proportional to count.
func (m Model) layoutWindow(skip, count int) ([]string, []int) {
	skip, count = max(skip, 0), max(count, 1)
	derived, w := m.walkFor()
	n := len(w.msgs)
	if n == 0 {
		return nil, nil
	}
	m.extendTo(w, skip+count)
	// The messages the window's bottom and top edges fall in.
	last := derived.startFor(skip + 1)
	first := derived.startFor(skip + count)

	var rows []string
	var owners []int
	own := func(added int, i int) {
		for range added {
			owners = append(owners, i)
		}
	}
	if first == 0 {
		anchors := m.topAnchorRows(w)
		rows = append(rows, anchors...)
		own(len(anchors), noOwner)
	}
	for i := first; i <= last && i < n; i++ {
		msgRows, rule := m.cachedRows(w, i)
		if rule.any() {
			rows = append(rows, m.ruleAboveRow(rule, w.msgs[i].Timestamp, w.width))
			own(1, noOwner)
		}
		rows = append(rows, msgRows...)
		own(len(msgRows), i)
	}
	// Trim the parts of the edge messages that fall outside the window: whatever of
	// the bottom message hangs below the edge, then the window's height from there.
	below := skip - derived.suffix[min(last+1, n)]
	end := len(rows) - max(below, 0)
	from, to := max(end-count, 0), max(end, 0)
	return rows[from:to], owners[from:to]
}

// noOwner marks a row that belongs to no message: a date divider, or a thread summary
// anchored above the oldest thing loaded.
const noOwner = -1

// walk is the per-frame state shared by every message's layout.
type walk struct {
	msgs      []domain.Message
	derived   *derivedCache
	byAnchor  map[domain.EventID][]domain.Thread
	colors    map[string]color.Color
	width     int
	nameW     int
	sel       domain.EventID
	highlight bool
}

// renderRows is one message's rows: its reply quote, wrapped body, and hanging rows.
func (m Model) renderRows(w walk, i int, selected bool) []string {
	msg := w.msgs[i]
	preview := ""
	// A quote of the message directly above is omitted (quote-replying to the last
	// message is common on bridges). The predecessor is read by index so a tail layout
	// agrees with the full one.
	if i == 0 || msg.ReplyTo != w.msgs[i-1].ID {
		preview = m.replyPreview(msg, w.derived, w.nameW, w.width, w.colors)
	}
	rows := m.messageRows(msg, w.width, w.nameW, w.colors, selected, preview)
	return append(rows, m.hangingRows(msg, w.byAnchor[msg.ID], w.nameW, w.width)...)
}

// dividerRow renders a centered date divider ("──── Today ────") spanning the
// content width.
func (m Model) dividerRow(t time.Time, width int) string {
	return m.ruleRow(dayLabel(t), width, m.theme.Muted)
}

// unreadRow is the line above the first unread message, in the badge color. It carries
// the date when it displaces a date divider.
func (m Model) unreadRow(t time.Time, width int, withDate bool) string {
	label := "new"
	if withDate {
		label = "new · " + dayLabel(t)
	}
	return m.ruleRow(label, width, lipgloss.NewStyle().Foreground(m.theme.Palette.Badge))
}

// ruleAboveRow draws whichever rule belongs above a message — the unread line wins,
// carrying the date it displaced.
func (m Model) ruleAboveRow(rule ruleAbove, t time.Time, width int) string {
	if rule.unread {
		return m.unreadRow(t, width, rule.date)
	}
	return m.dividerRow(t, width)
}

// ruleRow centers a label in a horizontal rule spanning the content width.
func (m Model) ruleRow(label string, width int, style lipgloss.Style) string {
	text := " " + label + " "
	if tw := ansi.StringWidth(text); tw < width {
		left := (width - tw) / 2
		right := width - tw - left
		text = strings.Repeat("─", left) + text + strings.Repeat("─", right)
	}
	return style.Render(text)
}

// dayKey is a message's local calendar day, used to detect day boundaries. A
// zero timestamp yields "" so undated rows (e.g. placeholders) start no divider.
func dayKey(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

// dayLabel is the human divider label: "Today", "Yesterday", or a full date.
func dayLabel(t time.Time) string {
	now := time.Now()
	switch {
	case sameDay(t, now):
		return "Today"
	case sameDay(t, now.AddDate(0, 0, -1)):
		return "Yesterday"
	default:
		return t.Format("Mon, 02 Jan 2006")
	}
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// decoratedTitle is the timeline pane's title around the room or thread named title:
// the day in view (date separators scroll away), then the topic, which yields first.
// A chooser with a title says what it is choosing instead, over the room it covers.
func (m Model) decoratedTitle(title string, inner int) string {
	if m.picker.active() && m.picker.spec.title != "" {
		return m.picker.spec.title
	}
	if day := m.stickyDay(); day != "" {
		title += "  " + day
	}
	if topic := m.topicSuffix(title, inner); topic != "" {
		title += topic
	}
	return title
}
