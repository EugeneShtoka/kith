package tui

import (
	"fmt"
	"image/color"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

const (
	redactedBody = "(deleted)"
	editedMarker = "(edited)"
	starMarker   = "★"
	emoteMarker  = "*"
)

// hangingRows are drawn beneath a message as part of its span: caption chip, picture,
// reactions, and anchored thread summaries.
func (m Model) hangingRows(msg domain.Message, threads []domain.Thread, nameW, width int) []string {
	var rows []string
	dir := m.bodyDir(msg)
	// A captioned attachment's chip hangs beneath the caption. A deleted message's
	// attachment shows only in its history (history.go).
	if msg.Media != nil && msg.Caption() != "" && !msg.Redacted {
		rows = append(rows, m.underBody(m.theme.Faint.Render(drawLine(mediaChip(msg.Media),
			lineSpec{width: width - m.bodyColumn(nameW), sentence: true})), dir, nameW, width))
	}
	if m.showsPictures() && msg.Media.IsImage() && !msg.Redacted {
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

// underBody places a drawn row under the body column. Mirrored, it starts where its
// message reads from: against the name for RTL words, from the left edge for LTR.
func (m Model) underBody(row string, dir bidi.Direction, nameW, width int) string {
	switch {
	case !m.mirrored():
		return strings.Repeat(" ", m.bodyColumn(nameW)) + row
	case dir == bidi.RightToLeft:
		return padStart(row, width-m.bodyColumn(nameW))
	default:
		return row
	}
}

// bodyDir is the direction a message's words read in (one per message).
func (m Model) bodyDir(msg domain.Message) bidi.Direction {
	return m.rowDir(msg, msg.Body)
}

// rowDir is the direction a message is laid out in: its words'. In a mirrored room,
// rows without the sender's own words read as the room does: kith's stand-in text (a
// deletion, a bare file chip), only links, emoji or numbers, or only a bridge's English
// header ("↷ Forwarded").
func (m Model) rowDir(msg domain.Message, body string) bidi.Direction {
	if m.mirrored() && (standsIn(msg) || !domain.HasWords(domain.WithoutBridgeHeader(body))) {
		return bidi.RightToLeft
	}
	return paragraphDir(body)
}

// standsIn reports whether a message is drawn as kith's words standing in for the
// sender's: a deletion, or a file with no caption.
func standsIn(msg domain.Message) bool {
	return msg.Redacted || (msg.Media != nil && msg.Caption() == "")
}

// replyPreview is the one-line quote of the replied-to message, drawn as this one's
// first line, or "". The quote is flattened and drawn in its own direction; the marker
// and name lead it (on the right when mirrored).
func (m Model) replyPreview(msg domain.Message, derived *derivedCache, nameW, width int, colors map[string]color.Color) string {
	if msg.ReplyTo == "" {
		return ""
	}
	faint := m.theme.Faint
	avail := max(width-m.bodyColumn(nameW), 1)
	tgt, ok := m.quotedTarget(derived, msg.ReplyTo)
	if !ok {
		// Not loaded yet; quotes.go fetches it.
		return faint.Render(clamp("↪ (loading the quoted message…)", avail))
	}
	body := tgt.Body
	if tgt.Redacted {
		body = redactedBody
	}
	body, _ = presented(strings.Join(strings.Fields(body), " "), nil)
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
	// Capped to a snippet: a full-width quote repeats the original a few rows up. Cut
	// before reordering (see nameCell).
	if cap := min(quoteWidth, avail); cap > 0 {
		body = nameCell(body, cap)
	} else {
		body = displayName(body)
	}
	return truncateDrawn(faint.Render("↪ ")+name+faint.Render(": "+body), avail)
}

const quoteWidth = 40

// mediaChip is the one-line attachment placeholder: a type icon, the file name, and
// when known its pixel size and byte size, e.g. "🖼 cat.jpg · 1024×768 · 240 KB".
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
		parts = append(parts, domain.HumanSize(md.Size))
	}
	return icon + " " + strings.Join(parts, " · ")
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
		// emojiCell measures toned and text-presentation emoji as the terminal draws them;
		// displayName because a key can be any string.
		chips = append(chips, style.Render(emojiCell(displayName(t.Key))+" "+strconv.Itoa(t.Count)))
	}
	if m.mirrored() && dir == bidi.RightToLeft {
		return m.underBody(strings.Join(mirroredChips(chips), "  "), dir, nameW, width)
	}
	return m.underBody(strings.Join(chips, "  "), dir, nameW, width)
}

// senderColorMap is every sender's color in the room (derived.go), from the whole
// loaded history so colors don't change as you scroll.
func (m Model) senderColorMap() map[string]color.Color {
	return m.derivedFor().colors
}

// bodyColumn is where a row's body starts: the time, a space, the name column, a space.
func (m Model) bodyColumn(nameW int) int { return m.timeWidth() + 1 + nameW + 1 }

// timeWidth is the time column's width: the widest time of day the clock writes.
func (m Model) timeWidth() int { return m.prefs.clock.TimeWidth() }

// rowTime is a row's time for the time column, blank for none.
func (m Model) rowTime(t time.Time) string {
	if t.IsZero() {
		return strings.Repeat(" ", m.timeWidth())
	}
	return m.theme.Muted.Render(m.prefs.clock.PaddedTime(t))
}

// messageRows renders one message: the first row carries the time and fixed-width
// sender column, continuation rows align under the body, blank lines are dropped.
func (m Model) messageRows(msg domain.Message, width, nameW int, colors map[string]color.Color, selected bool, preview string) []string {
	c := colors[msg.Sender]
	prefixW := m.bodyColumn(nameW)
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
	// The offset-mapped path draws formatting, tracked words and pill links (the painted
	// path cannot express a hyperlink).
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
	// A reply's quote shares the sender's first row.
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
	// Edited (not redacted) and starred marks trail the last row; a row too full for them
	// gets a row of their own, so none runs past the width.
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

// mirroredMarks puts a mirrored message's trailing marks on its last row: left of RTL
// words, after LTR ones. A row too full gets a row of its own, flushed right, so the
// body never runs into the names.
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

// alignIn pads a drawn row to width on the side its direction leaves open, so what
// follows (a mirrored row's name and time) starts at a fixed column.
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

// mirroredMessageRows is messageRows for an RTL room: name and time on the right of the
// first row, the body before them in its own direction; a quote shares that row and
// pushes the body below. Trailing marks follow the message's own reading order.
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

// messageBody is a message's text, mention spans and formatting spans. Mentions are
// resolved before wrapping since names change the length; toned emoji are spelled as
// drawn (presented) before anything measures.
func (m Model) messageBody(msg domain.Message, colors map[string]color.Color) (string, []mentionSpan, []richtext.Span) {
	if text, spans, ok := m.formattedBody(msg); ok {
		resolved, mentions := m.resolveMentions(text, msg.Mentions, colors, msg.RoomID)
		if resolved != text {
			// Resolved names shift every mark, so the sender's formatting is dropped.
			resolved, _ = presented(resolved, nil)
			return resolved, mentions, bareLinkSpans(resolved, nil)
		}
		text, spans = presented(text, spans)
		return text, mentions, append(spans, bareLinkSpans(text, spans)...)
	}
	body, mentions := m.plainBody(msg, colors)
	body, _ = presented(body, nil)
	return body, mentions, bareLinkSpans(body, nil)
}

// bareLinkSpans makes a link span of every written-out URL not already covered by one
// of the sender's (whose href may differ from the words).
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

func coveredByLink(spans []richtext.Span, start, end int) bool {
	for i := range spans {
		if spans[i].Link && start < spans[i].End && spans[i].Start < end {
			return true
		}
	}
	return false
}

// formattedBody is a message's formatting as text and spans, and whether to use it.
// Attachments, redactions and emotes take the plain path.
func (m Model) formattedBody(msg domain.Message) (string, []richtext.Span, bool) {
	if msg.Format.IsZero() || msg.Redacted || msg.Emote || msg.Media != nil {
		return "", nil, false
	}
	text, spans := msg.Format.Text(), msg.Format.Spans()
	if strings.TrimSpace(text) == "" {
		// Formatting that draws no words: use the plain body.
		return "", nil, false
	}
	return text, spans, true
}

// plainBody is a message's text when its formatting is not drawn.
func (m Model) plainBody(msg domain.Message, colors map[string]color.Color) (string, []mentionSpan) {
	switch {
	case msg.Redacted && m.timeline.revealed[msg.ID] && msg.Body != "":
		// Deleted but revealed: the placeholder, then the isolated words.
		return m.deletedBody(msg) + " " + isolate(msg.Body), nil
	case msg.Redacted:
		return m.deletedBody(msg), nil
	case msg.Poll != nil:
		return pollBody(msg.Poll), nil
	case msg.Emote:
		// The IRC marker without the name, which has its own column. Mention spans name people,
		// not offsets, so the prefix doesn't disturb them.
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

type mentionSpan struct {
	name string
	c    color.Color
	// uri is the pill's matrix: address for OSC 8 links, empty when not followable.
	uri string
}

// resolveMentions rewrites each mention to the person's processed name and returns
// spans to color: their room hue if they posted, else the accent. Longest first, so a
// shorter name cannot pre-empt a longer one.
func (m Model) resolveMentions(body string, mentions []domain.Mention, colors map[string]color.Color, roomID domain.RoomID) (string, []mentionSpan) {
	if len(mentions) == 0 {
		return body, nil
	}
	body, drawn := domain.ResolveMentions(body, mentions, func(mn domain.Mention, words string) string {
		return m.mentionedName(mn.UserID, roomID, mn.Known, words)
	})
	spans := make([]mentionSpan, 0, len(drawn))
	for i, mn := range drawn {
		if mn.Name == "" {
			continue
		}
		c, ok := colors[mn.UserID]
		if !ok {
			c = m.theme.Palette.Accent
		}
		spans = append(spans, mentionSpan{name: mn.Name, c: c, uri: mentions[i].URI()})
	}
	sort.Slice(spans, func(i, j int) bool { return len(spans[i].name) > len(spans[j].name) })
	return body, spans
}

// mentionedName is the name a mention of userID is drawn with: domain.People's answer
// (alias, member name, known names, latest message name, phone book), shaped by the
// room's space rule like the sender column. A name learned later reaches every mention
// already drawn.
func (m Model) mentionedName(userID string, roomID domain.RoomID, known ...string) string {
	member, sent := "", ""
	for i := range m.timeline.members {
		if m.timeline.members[i].UserID == userID {
			member = m.timeline.members[i].DisplayName
			break
		}
	}
	for i := range slices.Backward(m.timeline.messages) {
		if m.timeline.messages[i].Sender == userID && m.timeline.messages[i].SenderName != "" {
			sent = m.timeline.messages[i].SenderName
			break
		}
	}
	return m.shapedName(m.people().Name(userID, append(append([]string{member}, known...), sent)...), roomID)
}

// people is who this person knows people as: their aliases and the phone book.
func (m Model) people() domain.People {
	return domain.People{Alias: m.aliasOf, Dir: m.dir.dir}
}

// aliasOf is the person's own name for userID, or "": an identity alias, else the name
// of a room named after them.
func (m Model) aliasOf(userID string) string {
	if alias := m.prefs.aliases[userID]; alias != "" {
		return alias
	}
	if alias := m.prefs.identities[userID].alias; alias != "" {
		return alias
	}
	return m.peers.aliases[userID]
}

// styleMentions renders seg with each mentioned name bold in its color and the rest in
// base, matching non-overlapping first occurrences. seg must be ANSI-free (LTR only).
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

func rangeOccupied(occupied []bool, start, end int) bool {
	for i := start; i < end && i < len(occupied); i++ {
		if occupied[i] {
			return true
		}
	}
	return false
}

// messagePrefix is the styled "HH:MM <name> " lead-in, the name padded to nameW and
// bold in the sender's room color.
func (m Model) messagePrefix(msg domain.Message, nameW int, c color.Color, selected bool) string {
	ts, name := m.senderCells(msg, nameW, c, selected)
	return ts + " " + name + " "
}

// senderCells are a message's time and styled name padded to nameW: after the name,
// or before it when mirrored, so names sit against the time.
func (m Model) senderCells(msg domain.Message, nameW int, c color.Color, selected bool) (ts, name string) {
	label := displayName(m.processedName(msg))
	pad := strings.Repeat(" ", max(nameW-ansi.StringWidth(label), 0))
	// A sender keeps one color everywhere, mentioning you or not.
	nameStyle := lipgloss.NewStyle().Foreground(c).Bold(true)
	if selected {
		// Reverse video marks the cursor without shifting columns.
		nameStyle = nameStyle.Reverse(true)
	}
	ts = m.rowTime(msg.Timestamp)
	if m.mirrored() {
		return ts, pad + nameStyle.Render(label)
	}
	return ts, nameStyle.Render(label) + pad
}

// nameColWidth is the sender column width: the widest processed name in the room.
func (m Model) nameColWidth() int {
	return m.derivedFor().nameW
}

// senderName is processedName isolated for use in a sentence. A cell holding only the
// name (the sender column, a quote) uses displayName.
func (m Model) senderName(msg domain.Message) string {
	return isolate(m.processedName(msg))
}

// personIn is a sender label for someone named outside a message (thread reply,
// typing, invite); the room selects its space's name rule.
func (m Model) personIn(room domain.RoomID, sender, name string) string {
	return m.senderName(domain.Message{RoomID: room, Sender: sender, SenderName: name})
}

// processedName is the sender label: a merged identity's alias, else the name of a DM
// named after them (peeralias.go), else their name, shaped by shapedName.
func (m Model) processedName(msg domain.Message) string {
	if id, ok := m.prefs.identities[msg.Sender]; ok && id.alias != "" {
		return m.shapedName(id.alias, msg.RoomID)
	}
	if alias := m.peers.aliases[msg.Sender]; alias != "" {
		return m.shapedName(alias, msg.RoomID)
	}
	return m.shapedName(m.byNumber(senderLabel(msg)), msg.RoomID)
}

// shapedName applies the room's space name rules to a name, aliases included: the
// alias picks the name, the rule how much of it.
func (m Model) shapedName(name string, roomID domain.RoomID) string {
	return m.nameRuleFor(m.ownSpace(roomID)).apply(name)
}

// nameRule is everything the display settings do to a name, resolved for one space,
// so every surface applies the same transform.
type nameRule struct {
	firstOnly bool // keep the first word; per-space first_name_only
	rooms     bool // also shorten people-named room labels; [display] room_name_rules
	width     int  // truncate sender labels to this many columns, 0 = no limit
}

// nameRuleFor is the rule for one space, or only the global half for a room in none.
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

func (r nameRule) apply(name string) string {
	if r.firstOnly {
		name = firstName(name)
	}
	if r.width > 0 && ansi.StringWidth(name) > r.width {
		return truncateLogical(name, r.width)
	}
	return name
}

// within shortens participants inside a people-named room label, reporting whether it
// was one.
func (r nameRule) within(name string, members []string) (string, bool) {
	if !r.firstOnly || !r.rooms {
		return name, false
	}
	return shortenByMembers(name, members)
}

// ownSpace is the room's first space in priority order. The timeline uses it: the door
// you came through is not part of the conversation.
func (m Model) ownSpace(roomID domain.RoomID) string {
	spaces := m.homesOf(roomID)
	if len(spaces) == 0 {
		return ""
	}
	return spaces[0]
}

// listedSpace is the rail's selected space when it holds the room, else ownSpace. Only
// the room list uses it, so a space-scoped column is uniform.
func (m Model) listedSpace(roomID domain.RoomID) string {
	if here := m.rail.key(); here != "" {
		for _, space := range m.placeHomes(roomID) {
			if space == here {
				return space
			}
		}
	}
	return m.ownSpace(roomID)
}

func firstName(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// senderLabel is the resolved room display name, else the ID's short name.
func senderLabel(msg domain.Message) string {
	if msg.SenderName != "" {
		return msg.SenderName
	}
	return shortSender(msg.Sender)
}

// shortSender reduces a user ID to its short name (a localpart, a phone number), or
// the full value when it has none.
func shortSender(sender string) string {
	s := domain.ShortName(sender)
	if s == "" {
		return sender
	}
	return s
}
