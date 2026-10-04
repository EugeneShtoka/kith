package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The emoji browser: a non-modal picker with a grid renderer, so typing narrows the
// grid from the first keystroke.

// emojiCellWidth is one grid cell: a double-width emoji plus a space.
const emojiCellWidth = 3

// openEmojiPalette starts the browser for inserting, closing the completion popup.
func (m Model) openEmojiPalette() (Model, tea.Cmd) {
	m.picker = newPicker(pickerEmoji, m.emojiPickerItems(m.emojiCandidatesOf("", domain.EmojiComposed)))
	m = m.closeCompletion()
	return m, repaint()
}

// openReactionPalette is the same grid reacting to the selected message, filtered to
// what the room's network accepts.
func (m Model) openReactionPalette() (Model, tea.Cmd) {
	if _, ok := m.selectedMessage(); !ok {
		return m, nil
	}
	items := m.emojiPickerItems(m.emojiCandidatesOf("", domain.EmojiReaction))
	m.picker = newPicker(pickerReaction, m.reactableItems(items))
	return m.closeCompletion(), repaint()
}

// reactWithPicked posts the chosen (already toned) emoji and closes the grid.
func (m Model) reactWithPicked(emoji string) (Model, tea.Cmd) {
	m = m.closePicker()
	return m.sendReaction(emoji)
}

// emojiPickerItems turns emoji candidates into picker rows matched by shortcode.
func (m Model) emojiPickerItems(rows []candidate) []pickerItem {
	out := make([]pickerItem, 0, len(rows))
	for _, row := range rows {
		name := m.shortcodeFor(row.emoji)
		out = append(out, pickerItem{
			label:  row.emoji,
			detail: ":" + name + ":",
			value:  row.emoji,
			match:  name,
		})
	}
	return out
}

// insertPickedEmoji inserts a chosen emoji at the caret, enters insert mode and
// closes the browser.
func (m Model) insertPickedEmoji(emoji string) (Model, tea.Cmd) {
	m = m.store(fieldComposer, m.editorFor(fieldComposer).insert(emoji))
	m.compose.insertMode = true
	m = m.closePicker()
	return m, m.recordEmojiCmd(emoji)
}

// pickerColumns is how many grid cells fit across the timeline pane's interior
// (framePane's w-2). Rendering and vertical motion must agree on it.
func (m Model) pickerColumns() int {
	interior := m.width - railWidth - roomsWidth - 2
	return max(interior/emojiCellWidth, 1)
}

// pickerLines renders the open picker: a header naming the selection, then the
// items. A grid picker shows values in columns; a list picker shows labeled rows.
func (m Model) pickerLines(width, rows int) []string {
	var out []string
	if header := m.pickerHeader(); header != "" {
		out = append(out, m.theme.Muted.Render(drawLine(header, lineSpec{width: width, sentence: true})))
	}
	if len(m.picker.items) == 0 {
		return padTo(out, rows)
	}
	doc := m.settingDocLines(width)
	body := max(rows-len(out)-len(doc), 1)
	if m.picker.spec.grid {
		return padTo(append(out, m.gridRows(body, width)...), rows)
	}
	out = padTo(append(out, m.listRows(body, width)...), rows-len(doc))
	for _, line := range doc {
		out = append(out, m.theme.Muted.Render(line))
	}
	return out
}

// docParagraphs is default.toml's text as paragraphs to wrap: its prose lines joined
// (the file wraps them for itself), an indented line (a table of values, an example)
// kept as written, a blank line a break.
func docParagraphs(doc string) []string {
	var out []string
	prose := false
	for line := range strings.SplitSeq(doc, "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			prose = false
		case strings.HasPrefix(line, " "):
			out, prose = append(out, line), false
		case prose:
			out[len(out)-1] += " " + line
		default:
			out, prose = append(out, line), true
		}
	}
	return out
}

// The setting's text beneath the list takes the rows the list leaves, at least
// settingDocLeast and at most settingDocMost.
const (
	settingDocLeast = 6
	settingDocMost  = 20
)

// settingDocLines is what default.toml says of the setting under the cursor, wrapped
// to width under a blank line; none outside a settings group or for a setting with
// nothing written about it.
func (m Model) settingDocLines(width int) []string {
	if m.picker.kind != pickerSetting {
		return nil
	}
	item, ok := m.picker.selected()
	if !ok {
		return nil
	}
	s, ok := m.setting(item.value)
	if !ok || strings.TrimSpace(s.doc) == "" {
		return nil
	}
	lines := []string{""}
	for _, para := range docParagraphs(s.doc) {
		lines = append(lines, drawBlock(para, blockSpec{width: width})...)
	}
	room := min(max(m.msgAreaRows()-len(m.picker.items)-1, settingDocLeast), settingDocMost)
	if len(lines) > room {
		lines = append(lines[:room-1], "…")
	}
	return lines
}

// pickerBody is how many item rows the open picker shows: the message area, less its
// header (as pickerLines lays it out).
func (m Model) pickerBody() int {
	rows := m.msgAreaRows() - len(m.settingDocLines(m.pickerWidth()))
	if m.pickerHeader() != "" {
		rows--
	}
	return max(rows, 1)
}

// pickerWidth is the width a picker's rows are drawn in: the timeline pane's interior.
func (m Model) pickerWidth() int { return max(m.width-railWidth-roomsWidth-paneBorder, 1) }

// gridRows lays the items out in columns, keeping the cursor's row on screen.
func (m Model) gridRows(body, width int) []string {
	perRow := m.pickerColumns()
	total := (len(m.picker.items) + perRow - 1) / perRow
	first := windowStart(m.picker.cursor/perRow, body, total)

	out := make([]string, 0, body)
	for row := first; row < first+body; row++ {
		start := row * perRow
		if start >= len(m.picker.items) {
			break
		}
		out = append(out, m.gridRow(start, perRow, width))
	}
	return out
}

// emojiCell is a glyph as drawn (display only): text-presentation emoji (‼ ✂ ⚠ ❤ …)
// get U+FE0F so they measure two columns, as the terminal draws them.
func emojiCell(glyph string) string {
	if ansi.StringWidth(glyph) == 2 {
		return glyph
	}
	// A toned text-presentation emoji (base + modifier) is drawn two wide but measures
	// one; the selector between them makes both agree.
	if base, mod, ok := splitTone(glyph); ok {
		return base + presentationSelector + mod
	}
	if len([]rune(glyph)) == 1 {
		return glyph + presentationSelector
	}
	return glyph
}

// gridRow renders one row of cells, marking the selected one with a background fill
// (a cursor glyph would shift cells; a foreground color is invisible on emoji).
func (m Model) gridRow(start, perRow, width int) string {
	var b strings.Builder
	used := 0
	for i := start; i < start+perRow && i < len(m.picker.items); i++ {
		// Never draw past the pane: clipping afterwards could halve a wide glyph.
		if used+emojiCellWidth > width {
			break
		}
		used += emojiCellWidth
		glyph := emojiCell(m.picker.items[i].label)
		cell := glyph + strings.Repeat(" ", max(emojiCellWidth-ansi.StringWidth(glyph), 0))
		if i == m.picker.cursor {
			b.WriteString(m.theme.PaneRow(true, true).Render(cell))
			continue
		}
		b.WriteString(cell)
	}
	// Pad a short last row so the pane's border stays put.
	row := b.String()
	if pad := min(perRow*emojiCellWidth, width) - ansi.StringWidth(row); pad > 0 {
		row += strings.Repeat(" ", pad)
	}
	return clamp(row, width)
}

// listRows renders the items as labeled rows.
func (m Model) listRows(body, width int) []string {
	first := windowStart(m.picker.cursor, body, len(m.picker.items))
	out := make([]string, 0, body)
	for i := first; i < first+body && i < len(m.picker.items); i++ {
		item := m.picker.items[i]
		selected := i == m.picker.cursor
		lead := cursor(selected)
		if m.picker.spec.multi {
			lead += checkbox(m.picker.ticked(item.value)) + " "
		}
		if selected && m.editingSettingRow() {
			out = append(out, m.editedRow(lead, item.label, width))
			continue
		}
		out = append(out, m.labeledRow(lead, item.label, item.detail, selected, width))
	}
	return out
}

// editingSettingRow reports whether the selected settings row (a setting, or an entry
// of a list setting) is being typed in.
func (m Model) editingSettingRow() bool {
	return (m.picker.kind == pickerSetting && m.prompt.kind == promptSetting) ||
		(m.picker.kind == pickerSettingEntries && m.prompt.kind == promptSettingEntry)
}

// editedRow is a settings row whose value is being typed: the label, then the value
// with the caret, drawn selected while untouched (the first key typed replaces it).
func (m Model) editedRow(lead, label string, width int) string {
	sep := " : "
	if m.picker.kind == pickerSettingEntries {
		label, sep = "", "" // an entry is its value: the row is the editor alone
	}
	line := m.theme.Row(true, true).Render(clamp(lead+drawLine(label, lineSpec{width: max(width-ansi.StringWidth(lead), 1), sentence: true}), width))
	room := width - ansi.StringWidth(line) - len(sep)
	if room < 1 {
		return line
	}
	value := editedLine(m.editorFor(fieldPrompt), room, m.typingField() == fieldPrompt)
	if m.prompt.fresh {
		value = lipgloss.NewStyle().Reverse(true).Render(m.prompt.input) + caretMark
	}
	return line + sep + value
}

// pickerHeader is what a picker has to say that its rows cannot, or "": for a list,
// only the filter; for a grid, the selected emoji's shortcode too.
func (m Model) pickerHeader() string {
	item, ok := m.picker.selected()
	if !ok {
		if m.picker.filter != "" {
			return fmt.Sprintf("nothing matches %s — backspace to widen", quoted(m.picker.filter))
		}
		return "(nothing to choose from)"
	}
	if !m.picker.spec.grid {
		if m.picker.filter == "" {
			return ""
		}
		return fmt.Sprintf("filter: %s   (%d of %d)", isolate(m.picker.filter), m.picker.cursor+1, len(m.picker.items))
	}
	// Through emojiCell as the grid is: a column of mismeasure wraps this full-width
	// line and leaves a duplicated row (seen with toned ☝ ✌ ✍).
	label := emojiCell(item.label)
	if item.detail != "" {
		label += "  " + item.detail
	}
	if m.picker.filter != "" {
		label += "   filter: " + isolate(m.picker.filter)
	}
	return fmt.Sprintf("%s   (%d of %d)", label, m.picker.cursor+1, len(m.picker.items))
}

// padTo fills lines out to n with blanks.
func padTo(lines []string, n int) []string {
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines
}

// reactableItems silently drops the rows this room's network has refused.
func (m Model) reactableItems(items []pickerItem) []pickerItem {
	protocol := m.roomProtocol()
	if !protocol.IsBridged() || len(m.glyphs.refused[protocol.String()]) == 0 {
		return items
	}
	out := make([]pickerItem, 0, len(items))
	for _, item := range items {
		if !m.refusedIn(protocol, item.value) {
			out = append(out, item)
		}
	}
	return out
}

// checkbox is a row's tick in a multi picker; brackets because ☑/☐ mismeasure.
func checkbox(on bool) string {
	if on {
		return "[x]"
	}
	return "[ ]"
}
