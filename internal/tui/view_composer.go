package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// completionLines renders the completion popup as a plain, height-capped list, or nil.
func (m Model) completionLines(width int) []string {
	if !m.completion.active || len(m.completion.candidates) == 0 {
		return nil
	}
	total := len(m.completion.candidates)
	visible := min(completionRows, min(total, max(m.msgAreaRows()-1, 1)))
	start := windowStart(m.completion.cursor, visible, total)

	out := make([]string, 0, visible+1)
	for i := start; i < start+visible && i < total; i++ {
		out = append(out, m.candidateRow(m.completion.candidates[i], i == m.completion.cursor, width))
	}
	if hidden := total - visible; hidden > 0 {
		out = append(out, m.theme.Muted.Render(clamp(fmt.Sprintf("  …%d more", hidden), width)))
	}
	return out
}

// modelOptionLines is one truncated row of numbered continuations, shown only when there
// are at least two (one is the ghost's job).
func (m Model) modelOptionLines(width int) []string {
	options := m.modelOptions()
	if len(options) < 2 {
		return nil
	}
	var row strings.Builder
	for i, option := range options {
		if i > 0 {
			row.WriteString(m.theme.Muted.Render("  "))
		}
		row.WriteString(m.theme.Title.Render(strconv.Itoa(i + 1)))
		row.WriteString(m.theme.Muted.Render(" " + displayName(option)))
	}
	return []string{clamp(row.String(), width)}
}

// candidateRow renders one completion popup row.
func (m Model) candidateRow(row candidate, selected bool, width int) string {
	return m.labeledRow(cursor(selected), row.label, row.detail, selected, width)
}

// labeledRow is one row of a list to choose from: a lead, the label, and a dimmed detail
// when there is room. label and detail are logical and drawn here as sentences.
func (m Model) labeledRow(lead, label, detail string, selected bool, width int) string {
	shown := drawLine(label, lineSpec{width: max(width-ansi.StringWidth(lead), 1), sentence: true})
	line := m.theme.Row(selected, true).Render(clamp(lead+shown, width))
	if detail == "" {
		return line
	}
	room := width - ansi.StringWidth(line)
	if room < 7 { // " · " plus something worth reading
		return line
	}
	return line + m.theme.Muted.Render(" · "+drawLine(detail, lineSpec{width: room - 3, sentence: true}))
}

const (
	composerMaxRows = 6 // past this the composer scrolls to keep the caret visible
	minMsgRows      = 2 // the message area never shrinks below this
)

// composerPrefix is the composer's gutter: the mode tag and the prompt.
func (m Model) composerPrefix() (tag, prompt string) {
	mode := "NORMAL"
	if m.compose.insertMode {
		mode = "INSERT"
	}
	tag = m.theme.Muted.Render("[" + mode + "] ")
	// A thread prompt says so.
	lead := ""
	if m.thread.open() {
		lead = "thread "
	}
	// Say so while a model endpoint is configured (or refusing) for this room.
	if marker := m.modelMarker(); marker != "" {
		tag += m.theme.Muted.Render(marker + " ")
	}
	// A draft restored from an earlier session is labeled until the first keystroke.
	if note := m.draftNote(); note != "" {
		tag += m.theme.Muted.Render("✎ " + drawSentence(note) + " ")
	}
	prompt = m.theme.Prompt.Render(lead + "› ")
	switch {
	case m.compose.isEditing():
		// An edit replaces a message, so the gutter says so.
		prompt = m.theme.Prompt.Render(lead + "✎ editing › ")
	case m.compose.replyTo != "":
		// Composing a reply: name the person you're responding to, in their hue.
		prompt = m.theme.Prompt.Render(lead+"↪ ") + m.replyLabelStyled() + m.theme.Prompt.Render(" › ")
	}
	return tag, prompt
}

// composerTextWidth is the cells each composer row gets: the interior less the gutter
// and a caret column reserved on every row, so moving the caret never rewraps.
func (m Model) composerTextWidth() int {
	tag, prompt := m.composerPrefix()
	return max(m.contentWidth()-ansi.StringWidth(tag)-ansi.StringWidth(prompt)-1, 1)
}

// composerSegments is the message laid out in rows. The renderer and msgAreaRows both
// read it, so the height the pane reserves and the height it draws cannot disagree.
func (m Model) composerSegments() []segment {
	return wrapSegments(m.compose.input, m.composerTextWidth())
}

// composerRows is how many rows the composer occupies right now.
func (m Model) composerRows() int {
	if m.compose.reacting {
		return 1 // the react prompt takes the composer's place, and it is one line
	}
	rows := min(len(m.composerSegments()), composerMaxRows)
	if spare := m.height - 5 - minMsgRows; rows > spare {
		rows = spare
	}
	return max(rows, 1)
}

// composerLines is the composer: the gutter, then the message wrapped across as many
// rows as it needs, scrolled to keep the caret visible once it outgrows the cap.
func (m Model) composerLines(active bool) []string {
	if m.compose.reacting {
		return []string{m.reactLine(active)}
	}
	tag, prompt := m.composerPrefix()
	indent := strings.Repeat(" ", ansi.StringWidth(tag)+ansi.StringWidth(prompt))
	// The caret's reserved column is drawable — it is only held back from the wrap.
	width := m.composerTextWidth() + 1

	e := m.editorFor(fieldComposer)
	segs := m.composerSegments()
	rows := m.composerRows()
	caret := caretSegment(segs, e.at)
	first := windowStart(caret, rows, len(segs))
	// One base direction for the whole draft, as messageRows does; misspellings are in
	// draft offsets, narrowed per row below.
	dir := paragraphDir(e.text)
	marks := m.composerMarks()

	out := make([]string, 0, rows)
	for i := first; i < first+rows && i < len(segs); i++ {
		row := e.text[segs[i].start:segs[i].end]
		rowMarks := rangesIn(marks, segs[i].start, segs[i].end)
		line := m.markedRow(row, dir, rowMarks)
		if i == caret && active && m.typingField() == fieldComposer {
			line = m.caretRow(row, e.at-segs[i].start, dir, rowMarks, width,
				m.ghostTail(row, e.at-segs[i].start, dir, width))
		}
		// The gutter goes on the first row drawn.
		if len(out) == 0 {
			out = append(out, tag+prompt+line)
			continue
		}
		out = append(out, indent+line)
	}
	return out
}

// caretRow draws the composer row the caret is on, each side reordered separately with
// its own underlines. A row that does not fit is drawn plain and windowed, since
// clipToward would cut ANSI escapes.
func (m Model) caretRow(
	row string, at int, dir bidi.Direction, marks []domain.Misspelling, width int, ghost string,
) string {
	rtl := dir == bidi.RightToLeft
	before, after := drawFragment(row[:at], dir, nil), drawFragment(row[at:], dir, nil)
	// A ghost suggestion only appears at the end of the draft, so it fills the empty half.
	if ghost != "" && after == "" {
		after = ghost
	}
	if len(marks) == 0 || ansi.StringWidth(before)+ansi.StringWidth(after) > width {
		return caretWindow(before, after, width, rtl)
	}
	// rangesIn rebases, so each half's marks are offsets into that half.
	return caretWindow(
		m.markedRow(row[:at], dir, rangesIn(marks, 0, at)),
		m.markedRow(row[at:], dir, rangesIn(marks, at, len(row))),
		width, rtl)
}

// composerDivider is the rule above the composer, carrying who is typing and how many
// composer rows are scrolled out of sight.
func (m Model) composerDivider(width int) string {
	rule := strings.Repeat("─", max(width, 0))
	if m.compose.reacting {
		return rule
	}
	segs := m.composerSegments()
	rows := m.composerRows()
	var parts []string
	if note := m.typingNote(); note != "" {
		parts = append(parts, note)
	}
	if len(segs) > rows {
		first := windowStart(caretSegment(segs, m.editorFor(fieldComposer).at), rows, len(segs))
		if first > 0 {
			parts = append(parts, fmt.Sprintf("%d above", first))
		}
		if below := len(segs) - first - rows; below > 0 {
			parts = append(parts, fmt.Sprintf("%d below", below))
		}
	}
	if len(parts) == 0 {
		return rule
	}
	// Drawn as a sentence: the typing note names people.
	note := " " + drawSentence(strings.Join(parts, " · ")) + " "
	// Built from repeat counts: "─" is three bytes per cell.
	noteW := ansi.StringWidth(note)
	if noteW >= width-4 {
		return rule // no room to say it without crowding out the rule itself
	}
	left := (width - noteW) / 2
	return strings.Repeat("─", left) + note + strings.Repeat("─", width-left-noteW)
}

// reactLine is the react prompt, which takes over the composer while it is open: an
// empty prompt shows the quick-pick palette; once you start typing it shows your
// :shortcode:/emoji.
func (m Model) reactLine(active bool) string {
	prompt := m.theme.Prompt.Render("react › ")
	if m.compose.reactInput == "" {
		return prompt + m.theme.Muted.Render(m.paletteHint()+"   · or :name: · "+m.keys.keyHint(scopeReact, actCancel))
	}
	hint := m.theme.Muted.Render("   · " + m.keys.keyHint(scopeReact, actSend) + ": send · " +
		m.keys.keyHint(scopeReact, actCancel))
	room := m.contentWidth() - ansi.StringWidth(prompt) - ansi.StringWidth(hint)
	return prompt + editedLine(m.editorFor(fieldReact), room, active && m.typingField() == fieldReact) + hint
}

// editedLine is a field being typed into, drawn in reading order with the caret, and
// windowed around it when too long; showCaret is false where the field is only shown.
// The two sides of the caret are reordered separately: x/text/bidi gives no caret
// positions, and splitting costs at most a digit run resolving differently.
func editedLine(e editor, width int, showCaret bool) string {
	dir := paragraphDir(e.text)
	if !showCaret {
		return drawFragment(e.text, dir, nil)
	}
	// In an RTL line what precedes the caret is drawn to its right.
	return caretWindow(drawFragment(e.before(), dir, nil), drawFragment(e.after(), dir, nil), width,
		dir == bidi.RightToLeft)
}

// caretMark marks the caret in a rendered frame: a NUL, zero-width and untypeable.
// takeCaret turns its position into the real terminal cursor and strips it.
const caretMark = "\x00"

// caretWindow fits the text around the caret into width cells, keeping the caret
// visible; rtl decides which side each half is drawn on.
func caretWindow(before, after string, width int, rtl bool) string {
	if width < 1 {
		return ""
	}
	if line := join(before, after, rtl); ansi.StringWidth(line) <= width {
		return line
	}
	// The typed side gets the larger share, after gets up to a third, and whatever one
	// side does not use goes to the other.
	shownBefore := clipToward(before, width-min(ansi.StringWidth(after), width/3), !rtl)
	shownAfter := clipToward(after, width-ansi.StringWidth(shownBefore), rtl)
	return join(shownBefore, shownAfter, rtl)
}

// join puts the two sides of the caret in the order they are read.
func join(before, after string, rtl bool) string {
	if rtl {
		return after + caretMark + before
	}
	return before + caretMark + after
}

// clipToward reduces s to n cells, keeping the end that touches the caret — the tail
// of a block drawn to the caret's left, the head of one drawn to its right — and
// marking the end it dropped, so a windowed field cannot be mistaken for a short one.
func clipToward(s string, n int, onLeft bool) string {
	if onLeft {
		if kept := tailCells(s, n); ansi.StringWidth(kept) == ansi.StringWidth(s) {
			return kept
		}
		return "…" + tailCells(s, n-1)
	}
	if kept := headCells(s, n); ansi.StringWidth(kept) == ansi.StringWidth(s) {
		return kept
	}
	return headCells(s, n-1) + "…"
}

// headCells and tailCells are the whole graphemes at each end of s that fit in n cells.
func headCells(s string, n int) string {
	if n <= 0 {
		return ""
	}
	var out strings.Builder
	used, state := 0, -1
	for s != "" {
		var cluster string
		cluster, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		w := ansi.StringWidth(cluster)
		if used+w > n {
			break
		}
		out.WriteString(cluster)
		used += w
	}
	return out.String()
}

func tailCells(s string, n int) string {
	if n <= 0 {
		return ""
	}
	type cluster struct{ offset, width int }
	clusters := make([]cluster, 0, len(s))
	state := -1
	for rest, offset := s, 0; rest != ""; {
		var c string
		c, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		clusters = append(clusters, cluster{offset: offset, width: ansi.StringWidth(c)})
		offset += len(c)
	}
	used := 0
	for i, cluster := range slices.Backward(clusters) {
		if used+cluster.width > n {
			return s[clusters[i+1].offset:]
		}
		used += cluster.width
	}
	return s
}

// replyLabelStyled is the "who you're replying to" tag shown in the composer, the
// person's resolved name drawn in their own hue.
func (m Model) replyLabelStyled() string {
	colors := m.senderColorMap()
	for i := range m.timeline.messages {
		if m.timeline.messages[i].ID == m.compose.replyTo {
			name := displayName(m.processedName(m.timeline.messages[i]))
			return lipgloss.NewStyle().Foreground(colors[m.timeline.messages[i].Sender]).Bold(true).Render(name)
		}
	}
	return m.theme.Prompt.Render("message")
}

// moveComposerRow moves the caret one display row up or down, keeping the goal column
// across a run of moves (caret.goal).
func (m Model) moveComposerRow(delta int) (Model, tea.Cmd) {
	e := m.editorFor(fieldComposer)
	segs := m.composerSegments()
	row := caretSegment(segs, e.at)
	target := row + delta
	if target < 0 || target >= len(segs) {
		return m, nil // already at the top or bottom row of the message
	}
	goal := m.compose.caret.goal - 1
	if m.compose.caret.goal == 0 {
		goal = ansi.StringWidth(e.text[segs[row].start:e.at])
	}
	e.at = segs[target].start + offsetAtColumn(e.text[segs[target].start:segs[target].end], goal)
	m = m.store(fieldComposer, e)
	m.compose.caret.goal = goal + 1
	return m.composerTyped("")
}

// offsetAtColumn is the byte offset in one display row nearest to column col, always on
// a grapheme boundary: a row shorter than col ends there, and a wide glyph straddling it
// is landed before rather than inside.
func offsetAtColumn(row string, col int) int {
	used, at, state := 0, 0, -1
	for rest := row; rest != ""; {
		var cluster string
		cluster, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
		w := clusterWidth(cluster)
		if used+w > col {
			return at
		}
		used, at = used+w, at+len(cluster)
	}
	return at
}
