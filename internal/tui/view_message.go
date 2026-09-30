package tui

import (
	"fmt"
	"image/color"
	"slices"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

const (
	// timeFormat is a row's time, and timestampWidth the column it fills.
	timeFormat     = "15:04"
	timestampWidth = len(timeFormat)
	redactedBody   = "(deleted)"
	editedMarker   = "(edited)"
	starMarker     = "★"
	emoteMarker    = "*"
)

// hangingRows are drawn beneath a message and part of its span: caption chip, picture,
// reactions, and anchored thread summaries.
func (m Model) hangingRows(msg domain.Message, threads []domain.Thread, nameW, width int) []string {
	var rows []string
	dir := m.bodyDir(msg)
	// A captioned attachment's chip hangs beneath the caption.
	if msg.Media != nil && msg.Caption() != "" {
		rows = append(rows, m.underBody(m.theme.Faint.Render(drawLine(mediaChip(msg.Media),
			lineSpec{width: width - bodyColumn(nameW), sentence: true})), dir, nameW, width))
	}
	if m.showsPictures() && msg.Media.IsImage() {
		for _, r := range m.pics.rowsFor(msg.ID) {
			rows = append(rows, m.underBody(r, dir, nameW, width))
		}
	}
	if rr := m.reactionRow(msg.ID, dir, nameW, width); rr != "" {
		rows = append(rows, rr)
	}
	for i := range threads {
		rows = append(rows, m.threadRow(threads[i], nameW, width))
	}
	return rows
}

// underBody places a drawn row under the body column. Mirrored, it goes where its
// message reads from: against the name on the right for right-to-left words, from the
// left edge for left-to-right ones.
func (m Model) underBody(row string, dir bidi.Direction, nameW, width int) string {
	switch {
	case !m.mirrored():
		return strings.Repeat(" ", bodyColumn(nameW)) + row
	case dir == bidi.RightToLeft:
		return padStart(row, width-bodyColumn(nameW))
	default:
		return row
	}
}

// bodyDir is the direction a message's words read in (the whole message takes one).
func (m Model) bodyDir(msg domain.Message) bidi.Direction {
	return m.rowDir(msg, msg.Body)
}

// rowDir is the direction a message is laid out in, body its drawn text: the
// direction of the sender's words. In a mirrored room what has no words of its own
// reads as the room does: kith's text standing in for them (a deletion, a bare file
// chip), and a message of only links, emoji or numbers.
func (m Model) rowDir(msg domain.Message, body string) bidi.Direction {
	if m.mirrored() && (standsIn(msg) || !domain.HasWords(body)) {
		return bidi.RightToLeft
	}
	return paragraphDir(body)
}

// standsIn reports whether a message is drawn as kith's own words standing in for the
// sender's: a deletion, or a file with no caption.
func standsIn(msg domain.Message) bool {
	return msg.Redacted || (msg.Media != nil && msg.Caption() == "")
}

// replyPreview is the one-line quote of the message this one replies to, drawn as its
// first line, or "" when not a reply. The quote is flattened to one line and drawn in
// its own direction; the marker and name lead it (on the right when mirrored).
func (m Model) replyPreview(msg domain.Message, derived *derivedCache, nameW, width int, colors map[string]color.Color) string {
	if msg.ReplyTo == "" {
		return ""
	}
	faint := m.theme.Faint
	avail := max(width-bodyColumn(nameW), 1)
	tgt, ok := m.quotedTarget(derived, msg.ReplyTo)
	if !ok {
		// Not loaded yet; quotes.go fetches it.
		return faint.Render(clamp("↪ (loading the quoted message…)", avail))
	}
	body := tgt.Body
	if tgt.Redacted {
		body = redactedBody
	}
	body = strings.Join(strings.Fields(body), " ")
	// The quoted sender in their hue; marker and snippet dim.
	name := lipgloss.NewStyle().Foreground(colors[tgt.Sender]).Bold(true).Render(displayName(m.processedName(tgt)))
	if m.mirrored() && m.bodyDir(msg) == bidi.RightToLeft {
		// Read from the right: the marker, the name, then the quote, cut on its left.
		quote := min(quoteWidth, avail-ansi.StringWidth(name)-len("↩ : "))
		if quote < 1 {
			return truncateDrawn(name+faint.Render(" ↩"), avail)
		}
		return faint.Render(nameCell(body, quote)+" :") + name + faint.Render(" ↩")
	}
	// Capped to a snippet: a full-width quote repeats the original a few rows up.
	// Cut before reordering (see nameCell).
	if cap := min(quoteWidth, avail); cap > 0 {
		body = nameCell(body, cap)
	} else {
		body = displayName(body)
	}
	return truncateDrawn(faint.Render("↪ ")+name+faint.Render(": "+body), avail)
}

// quoteWidth caps the quoted text in a reply preview.
const quoteWidth = 40

// mediaChip is the one-line placeholder shown for an attachment: a type icon, the
// file name, and (when known) its pixel dimensions and byte size, e.g.
// "🖼 cat.jpg · 1024×768 · 240 KB".
func mediaChip(md *domain.Media) string {
	icon := "📎"
	switch md.Type {
	case domain.MediaImage:
		icon = "🖼"
	case domain.MediaVideo:
		icon = "🎬"
	case domain.MediaAudio:
		icon = "🎵"
	case domain.MediaFile:
		icon = "📎"
	}
	// Isolated: the name is somebody's, the rest is ours.
	name := isolate(md.Name)
	if name == "" {
		name = string(md.Type)
	}
	parts := []string{name}
	if md.Width > 0 && md.Height > 0 {
		parts = append(parts, fmt.Sprintf("%d×%d", md.Width, md.Height))
	}
	if md.Size > 0 {
		parts = append(parts, humanSize(md.Size))
	}
	return icon + " " + strings.Join(parts, " · ")
}

// humanSize formats a byte count as a compact human-readable string.
func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// reactionRow renders reactions as "key count" chips under the body column, or "".
func (m Model) reactionRow(target domain.EventID, dir bidi.Direction, nameW, width int) string {
	tallies := domain.AggregateReactions(m.timeline.reactions[target], "")
	if len(tallies) == 0 {
		return ""
	}
	style := m.theme.Faint
	chips := make([]string, 0, len(tallies))
	for _, t := range tallies {
		// emojiCell measures toned and text-presentation emoji as the terminal draws
		// them; displayName because a key can be any string.
		chips = append(chips, style.Render(emojiCell(displayName(t.Key))+" "+strconv.Itoa(t.Count)))
	}
	if m.mirrored() && dir == bidi.RightToLeft {
		return m.underBody(strings.Join(mirroredChips(chips), "  "), dir, nameW, width)
	}
	return m.underBody(strings.Join(chips, "  "), dir, nameW, width)
}

// senderColorMap is every sender's color in the room (derived.go), computed from the
// whole loaded history so colors do not change as you scroll.
func (m Model) senderColorMap() map[string]color.Color {
	return m.derivedFor().colors
}

// bodyColumn is where a row's body starts: the time, a space, the name column, a space.
func bodyColumn(nameW int) int { return timestampWidth + 1 + nameW + 1 }

// messageRows renders one message: the first row carries the timestamp and fixed-width
// sender column, continuation rows align under the body, blank lines are dropped.
func (m Model) messageRows(msg domain.Message, width, nameW int, colors map[string]color.Color, selected bool, preview string) []string {
	c := colors[msg.Sender]
	prefixW := bodyColumn(nameW)
	bodyW := max(width-prefixW, 1)
	body, mentions, marks := m.messageBody(msg, colors)
	style := m.richStyleFor(msg, body, mentions, c)
	mirror := m.mirrored()
	spec := blockSpec{
		width: bodyW,
		// Blank lines are dropped; one row is kept so the sender still shows.
		dropBlank: true,
		paint: func(vis string, dir bidi.Direction) string {
			return m.styleSegment(msg, vis, mentions, c, dir)
		},
	}
	// The offset-mapped path draws the sender's formatting, tracked words and pill
	// links (the painted path has no way to say a hyperlink).
	if len(marks) > 0 || len(style.tracked) > 0 || style.linksMentions() {
		spec.marks = func(from, to int) markFunc { return style.window(marks, from, to).at }
	}
	// One base direction per message keeps wrapped mixed-direction lines stable.
	drawn := drawBlock(body, spec)
	if mirror {
		return m.mirroredMessageRows(msg, drawn, m.rowDir(msg, body), bodyW, nameW, c, selected, preview)
	}
	prefix := m.messagePrefix(msg, nameW, c, selected)
	indent := strings.Repeat(" ", prefixW)
	rows := make([]string, 0, len(drawn)+1)
	// A reply's quote shares the sender's first row rather than being a row of its own.
	if preview != "" {
		rows = append(rows, prefix+preview)
	}
	for i, row := range drawn {
		if i == 0 && preview == "" {
			rows = append(rows, prefix+row)
		} else {
			rows = append(rows, indent+row)
		}
	}
	// An edited (but not redacted) message trails a faint marker on its last row;
	// starred messages trail the mark here, not in the (default-off) gutter. A last
	// row too full for them gets a row of their own, so none runs past the width.
	if marks := m.trailingMarks(msg); len(marks) > 0 && len(rows) > 0 {
		joined := strings.Join(marks, " ")
		last := len(rows) - 1
		if ansi.StringWidth(rows[last])+1+ansi.StringWidth(joined) <= width {
			rows[last] += " " + joined
		} else {
			rows = append(rows, indent+joined)
		}
	}
	return rows
}

// mirroredMarks puts the marks trailing a mirrored message's words on its last row:
// on the left of right-to-left words, after left-to-right ones. A row too full for
// them gets a row of its own, flushed right, so the body never runs into the names.
func mirroredMarks(drawn, marks []string, dir bidi.Direction, bodyW int) []string {
	last := len(drawn) - 1
	text := strings.TrimLeft(drawn[last], " ")
	if dir == bidi.RightToLeft {
		marks = slices.Clone(marks)
		slices.Reverse(marks)
	}
	joined := strings.Join(marks, " ")
	if ansi.StringWidth(text)+1+ansi.StringWidth(joined) > bodyW {
		return append(drawn, alignIn(joined, dir, bodyW))
	}
	if dir == bidi.RightToLeft {
		drawn[last] = leadRow(text, joined+" ", bodyW)
	} else {
		drawn[last] = text + " " + joined
	}
	return drawn
}

// alignIn fills a drawn row to width on the side its direction leaves open: right to
// left against the right edge, left to right from the left, so what follows it (the
// name and the time of a mirrored row) always starts at the same column.
func alignIn(row string, dir bidi.Direction, width int) string {
	if dir == bidi.RightToLeft {
		return padStart(row, width)
	}
	return padEnd(row, width)
}

// trailingMarks are what follows a message's words, in reading order: edited, starred.
func (m Model) trailingMarks(msg domain.Message) []string {
	var marks []string
	if msg.Edited && !msg.Redacted {
		marks = append(marks, m.theme.Faint.Render(editedMarker))
	}
	if m.isStarred(msg.ID) {
		marks = append(marks, lipgloss.NewStyle().Foreground(m.theme.Palette.Badge).Render(starMarker))
	}
	return marks
}

// mirroredMessageRows is messageRows in a room that reads right to left: the name and
// the time on the right of the first row, and the body in its own direction before
// them (a right-to-left one against the name); the quote, when
// there is one, shares that row and the body goes under it. The marks that trail the
// words follow the message's own reading order: on the left of right-to-left words,
// after left-to-right ones.
func (m Model) mirroredMessageRows(msg domain.Message, drawn []string, dir bidi.Direction, bodyW, nameW int, c color.Color, selected bool, preview string) []string {
	ts, name := m.senderCells(msg, nameW, c, selected)
	tail := " " + name + " " + ts
	if marks := m.trailingMarks(msg); len(marks) > 0 && len(drawn) > 0 {
		drawn = mirroredMarks(drawn, marks, dir, bodyW)
	}
	// Each message keeps its own direction: only the name and the time moved.
	for i := range drawn {
		drawn[i] = alignIn(drawn[i], dir, bodyW)
	}
	rows := make([]string, 0, len(drawn)+1)
	if preview != "" {
		rows = append(rows, alignIn(preview, dir, bodyW)+tail)
		return append(rows, drawn...)
	}
	rows = append(rows, drawn[0]+tail)
	return append(rows, drawn[1:]...)
}

// messageBody is the text a message shows, its mention spans, and its formatting spans.
// Mentions are resolved before wrapping since resolved names change the length.
func (m Model) messageBody(msg domain.Message, colors map[string]color.Color) (string, []mentionSpan, []richtext.Span) {
	if text, spans, ok := m.formattedBody(msg); ok {
		resolved, mentions := m.resolveMentions(text, msg.Mentions, colors, msg.RoomID)
		if resolved != text {
			// Resolved names shift every mark, so the sender's formatting is dropped.
			return resolved, mentions, bareLinkSpans(resolved, nil)
		}
		return text, mentions, append(spans, bareLinkSpans(text, spans)...)
	}
	body, mentions := m.plainBody(msg, colors)
	return body, mentions, bareLinkSpans(body, nil)
}

// bareLinkSpans turns every written-out URL into a link span, skipping any already
// covered by one of the sender's (whose href may differ from the words).
func bareLinkSpans(text string, have []richtext.Span) []richtext.Span {
	found := domain.LinkSpans(text)
	if len(found) == 0 {
		return nil
	}
	out := make([]richtext.Span, 0, len(found))
	for _, link := range found {
		if coveredByLink(have, link.Start, link.End) {
			continue
		}
		out = append(out, richtext.Span{
			Start: link.Start, End: link.End, Link: true, Href: link.URL,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// coveredByLink reports whether any link span overlaps [start, end).
func coveredByLink(spans []richtext.Span, start, end int) bool {
	for i := range spans {
		if spans[i].Link && start < spans[i].End && spans[i].Start < end {
			return true
		}
	}
	return false
}

// formattedBody is a message's HTML as text and spans, and whether to use it.
// Attachments, redactions and emotes take the plain path.
func (m Model) formattedBody(msg domain.Message) (string, []richtext.Span, bool) {
	if msg.HTML == "" || msg.Redacted || msg.Emote || msg.Media != nil {
		return "", nil, false
	}
	text, spans := richtext.Parse(msg.HTML)
	if strings.TrimSpace(text) == "" {
		// Formatting that draws no words: use the plain body.
		return "", nil, false
	}
	return text, spans, true
}

// plainBody is the text a message shows when its formatting is not being drawn.
func (m Model) plainBody(msg domain.Message, colors map[string]color.Color) (string, []mentionSpan) {
	switch {
	case msg.Redacted && m.timeline.revealed[msg.ID] && msg.Body != "":
		// Deleted but revealed: the placeholder, then the isolated words.
		return m.deletedBody(msg) + " " + isolate(msg.Body), nil
	case msg.Redacted:
		return m.deletedBody(msg), nil
	case msg.Emote:
		// The IRC marker without the name, which has its own column. Mention spans name
		// people rather than offsets, so the prefix does not disturb them.
		body, spans := m.resolveMentions(msg.Body, msg.Mentions, colors, msg.RoomID)
		return emoteMarker + " " + body, spans
	case msg.Caption() != "":
		return m.resolveMentions(msg.Caption(), msg.Mentions, colors, msg.RoomID)
	case msg.Media != nil:
		return mediaChip(msg.Media), nil
	default:
		return m.resolveMentions(msg.Body, msg.Mentions, colors, msg.RoomID)
	}
}

// styleSegment paints one wrapped body line. Mentions are colored only on LTR lines,
// since the RTL reorder is not ANSI-aware.
func (m Model) styleSegment(msg domain.Message, vis string, mentions []mentionSpan, c color.Color, dir bidi.Direction) string {
	switch {
	case msg.Redacted:
		return m.theme.Faint.Italic(true).Render(vis)
	case msg.Media != nil && msg.Caption() == "":
		// A bare chip is chrome; a caption is someone's sentence.
		return m.theme.Faint.Render(vis)
	case len(mentions) > 0 && dir != bidi.RightToLeft:
		base := lipgloss.NewStyle()
		if m.prefs.display.ColorMessages {
			base = base.Foreground(c)
		}
		return styleMentions(vis, mentions, base)
	case m.prefs.display.ColorMessages:
		// The sender's hue without the bold, so the name still stands out.
		return lipgloss.NewStyle().Foreground(c).Render(vis)
	}
	return vis
}

// mentionSpan is a name to color in a body and the color to use.
type mentionSpan struct {
	name string
	c    color.Color
	// uri is the pill's matrix: address for OSC 8 links, empty when not followable.
	uri string
}

// resolveMentions rewrites each mention to the person's processed display name and
// returns spans to color: their room hue if they have posted, else the accent. Spans are
// longest-first so a shorter name cannot pre-empt a longer one.
func (m Model) resolveMentions(body string, mentions []domain.Mention, colors map[string]color.Color, roomID domain.RoomID) (string, []mentionSpan) {
	if len(mentions) == 0 {
		return body, nil
	}
	spans := make([]mentionSpan, 0, len(mentions))
	for _, mn := range mentions {
		if mn.Name == "" {
			continue
		}
		resolved := m.processedMentionName(mn.UserID, mn.Name, roomID)
		if resolved != mn.Name {
			body = strings.Replace(body, mn.Name, resolved, 1)
		}
		c, ok := colors[mn.UserID]
		if !ok {
			c = m.theme.Palette.Accent
		}
		spans = append(spans, mentionSpan{name: resolved, c: c, uri: mn.URI()})
	}
	sort.Slice(spans, func(i, j int) bool { return len(spans[i].name) > len(spans[j].name) })
	return body, spans
}

// processedMentionName resolves a mentioned user's name like a sender label (alias,
// then shapedName), so pills and the sender column agree.
func (m Model) processedMentionName(userID, pillName string, roomID domain.RoomID) string {
	name := pillName
	if id, ok := m.prefs.identities[userID]; ok && id.alias != "" {
		name = id.alias
	}
	return m.shapedName(name, roomID)
}

// styleMentions renders seg with each mentioned name in its color (bold) and the
// rest in base, matching non-overlapping first occurrences. seg must be free of
// ANSI (LTR bodies only).
func styleMentions(seg string, mentions []mentionSpan, base lipgloss.Style) string {
	type span struct {
		start, end int
		c          color.Color
	}
	occupied := make([]bool, len(seg))
	spans := make([]span, 0, len(mentions))
	for _, mn := range mentions {
		for idx := strings.Index(seg, mn.name); idx >= 0; {
			end := idx + len(mn.name)
			if !rangeOccupied(occupied, idx, end) {
				for i := idx; i < end; i++ {
					occupied[i] = true
				}
				spans = append(spans, span{idx, end, mn.c})
				break
			}
			next := strings.Index(seg[idx+1:], mn.name)
			if next < 0 {
				break
			}
			idx = idx + 1 + next
		}
	}
	if len(spans) == 0 {
		return base.Render(seg)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var b strings.Builder
	pos := 0
	for _, s := range spans {
		if s.start > pos {
			b.WriteString(base.Render(seg[pos:s.start]))
		}
		b.WriteString(lipgloss.NewStyle().Foreground(s.c).Bold(true).Render(seg[s.start:s.end]))
		pos = s.end
	}
	if pos < len(seg) {
		b.WriteString(base.Render(seg[pos:]))
	}
	return b.String()
}

// rangeOccupied reports whether any index in [start,end) is already taken.
func rangeOccupied(occupied []bool, start, end int) bool {
	for i := start; i < end && i < len(occupied); i++ {
		if occupied[i] {
			return true
		}
	}
	return false
}

// messagePrefix is the styled "HH:MM <name> " lead-in for a message's first row,
// with the name padded to nameW so bodies align. c is the sender's per-room
// color; the name is bold to stand out from a color-tinted body.
func (m Model) messagePrefix(msg domain.Message, nameW int, c color.Color, selected bool) string {
	ts, name := m.senderCells(msg, nameW, c, selected)
	return ts + " " + name + " "
}

// senderCells are a message's time and its styled name padded to nameW: after the
// name, or, mirrored, before it, so names sit against the time either way.
func (m Model) senderCells(msg domain.Message, nameW int, c color.Color, selected bool) (ts, name string) {
	label := displayName(m.processedName(msg))
	pad := strings.Repeat(" ", max(nameW-ansi.StringWidth(label), 0))
	nameStyle := lipgloss.NewStyle().Foreground(c).Bold(true)
	if msg.Mentioned {
		// Mentions of you tint the name (n/N jump between them).
		nameStyle = m.theme.Badge(true)
	}
	if selected {
		// Reverse video marks the cursor without shifting columns.
		nameStyle = nameStyle.Reverse(true)
	}
	ts = strings.Repeat(" ", timestampWidth)
	if !msg.Timestamp.IsZero() {
		ts = m.theme.Muted.Render(msg.Timestamp.Format(timeFormat))
	}
	if m.mirrored() {
		return ts, pad + nameStyle.Render(label)
	}
	return ts, nameStyle.Render(label) + pad
}

// nameColWidth is the sender column width: the widest processed name in the room.
func (m Model) nameColWidth() int {
	return m.derivedFor().nameW
}

// senderName is processedName isolated for a sentence, like roomName: logical, and drawn
// by whatever it is put into. A cell that is the name and nothing else — the sender
// column, a reply's quote — draws it with displayName.
func (m Model) senderName(msg domain.Message) string {
	return isolate(m.processedName(msg))
}

// personIn is a sender label for someone named outside a message (thread reply,
// typing, invite). The room must be passed so its space's name rule applies.
func (m Model) personIn(room domain.RoomID, sender, name string) string {
	return m.senderName(domain.Message{RoomID: room, Sender: sender, SenderName: name})
}

// processedName is the sender label after applying the display rules: a merged
// identity's alias supplies the name when there is one, and shapedName shapes it.
func (m Model) processedName(msg domain.Message) string {
	if id, ok := m.prefs.identities[msg.Sender]; ok && id.alias != "" {
		return m.shapedName(id.alias, msg.RoomID)
	}
	return m.shapedName(senderLabel(msg), msg.RoomID)
}

// shapedName applies the room's space name rules to a name, aliases included: an alias
// says which name, the rule says how much of it.
func (m Model) shapedName(name string, roomID domain.RoomID) string {
	return m.nameRuleFor(m.ownSpace(roomID)).apply(name)
}

// nameRule is everything the display settings do to a name, resolved for one space, so
// every surface applies the same transform and only chooses which space's rule.
type nameRule struct {
	firstOnly bool // keep the first word; per-space first_name_only
	rooms     bool // also shorten people-named room labels; [display] room_name_rules
	width     int  // truncate sender labels to this many columns, 0 = no limit
}

// nameRuleFor is the rule in force for one space, or the global half alone for a room
// that belongs to none.
func (m Model) nameRuleFor(space string) nameRule {
	rule := nameRule{
		rooms: m.prefs.display.ApplyRoomNameRules(),
		width: m.prefs.display.MaxNameLength,
	}
	if space == "" {
		return rule
	}
	for _, r := range m.prefs.display.SpaceRules {
		if r.Space == space {
			rule.firstOnly = r.FirstNameOnly
			break
		}
	}
	return rule
}

// apply transforms one person's name, for a sender label.
func (r nameRule) apply(name string) string {
	if r.firstOnly {
		name = firstName(name)
	}
	if r.width > 0 && ansi.StringWidth(name) > r.width {
		return truncateLogical(name, r.width)
	}
	return name
}

// within shortens the participants inside a people-named room label, reporting whether
// the name was one.
func (r nameRule) within(name string, members []string) (string, bool) {
	if !r.firstOnly || !r.rooms {
		return name, false
	}
	return shortenByMembers(name, members)
}

// ownSpace is the room's first space in the user's priority order. The timeline uses it:
// which door you came through is not part of the conversation.
func (m Model) ownSpace(roomID domain.RoomID) string {
	spaces := m.spacesOf(roomID)
	if len(spaces) == 0 {
		return ""
	}
	return spaces[0]
}

// listedSpace is the rail's selected space when the room is in it, else ownSpace. Only
// the room list uses it, so a space-scoped column is uniform with that space.
func (m Model) listedSpace(roomID domain.RoomID) string {
	if here := m.rail.key(); here != "" {
		for _, space := range m.spacesOf(roomID) {
			if space == here {
				return space
			}
		}
	}
	return m.ownSpace(roomID)
}

// firstName returns the first whitespace-delimited token of a name.
func firstName(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// senderLabel is the name shown for a message: the resolved room display name,
// falling back to the MXID localpart when none was resolved.
func senderLabel(msg domain.Message) string {
	if msg.SenderName != "" {
		return msg.SenderName
	}
	return shortSender(msg.Sender)
}

// shortSender reduces a Matrix user ID (@name:server) to its localpart for a
// compact timeline, falling back to the full value when it isn't an MXID.
func shortSender(sender string) string {
	s := strings.TrimPrefix(sender, "@")
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return sender
	}
	return s
}
