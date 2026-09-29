package tui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/unicode/bidi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// commandHelp lists the slash commands for the help overlay.
func (m Model) commandHelp(keyColumn int) []string {
	const usageColumn = 14 // "/upload <path[ | caption]>" is wider than any key
	lines := []string{"", m.theme.Title.Render("Typed into the composer")}
	for _, cmd := range slashCommands {
		usage := cmd.name
		if cmd.arg != "" {
			usage += " " + cmd.arg
		}
		lines = append(lines, helpRow(keyColumn+usageColumn, usage, m.liveKeys(cmd.summary)))
	}
	return append(lines,
		"  //text            sends a literal leading slash",
		"  anything else starting with a slash is a message that starts with a slash")
}

// jumpHelp lists the [keys.jump] bindings, sorted.
func (m Model) jumpHelp(keyColumn int) []string {
	if len(m.keys.jumps) == 0 {
		return nil
	}
	lines := []string{"", m.theme.Title.Render("Go to (your [keys.jump] bindings)")}
	for _, seq := range slices.Sorted(maps.Keys(m.keys.jumps)) {
		target := m.keys.jumps[seq]
		lines = append(lines, helpRow(keyColumn, spellSequence(seq), drawSentence(m.jumpLabel(target))))
	}
	return lines
}

// scriptHelp lists keys bound to scripts, with what each script is given.
func (m Model) scriptHelp(keyColumn int) []string {
	if len(m.keys.scripts) == 0 {
		return nil
	}
	lines := []string{"", m.theme.Title.Render("Your commands (keys on [[commands.script]])")}
	for _, seq := range slices.Sorted(maps.Keys(m.keys.scripts)) {
		name := m.keys.scripts[seq]
		label := "/" + name
		if needs := m.scriptNeeds("/" + name); len(needs) > 0 {
			written := make([]string, 0, len(needs))
			for _, need := range needs {
				written = append(written, need.String())
			}
			label += " — " + strings.Join(written, ", ")
		}
		lines = append(lines, helpRow(keyColumn, spellSequence(seq), drawSentence(label)))
	}
	return lines
}

// jumpLabel says where a jump target leads, or that it leads nowhere.
func (m Model) jumpLabel(target domain.JumpTarget) string {
	if target.Kind == domain.JumpSpace {
		if key, found := m.groupNamed(target.Name); found {
			return "space " + isolate(m.rail.groups[indexOfGroup(m.rail.groups, key)].label)
		}
		return target.String() + "  (no such space here)"
	}
	if room, found := m.roomNamed(target); found {
		return m.roomName(room)
	}
	return target.String() + "  (nothing here matches)"
}

// readerBody is the title, body and paint of whichever boxed reader is up; the scroll
// limit needs the same answer (readerContent).
func (m Model) readerBody() (string, []string, func(string) string) {
	switch m.reader.kind {
	case readerSummary:
		// The room's colors: a summary or topic is about its people.
		return "Summary", m.summaryLines(), paintNames(m.roomPalette())
	case readerTopic:
		return m.topicTitle(), m.topicLines(), paintNames(m.roomPalette())
	case readerTodo:
		// No palette: a todo list spans rooms, where a person has different colors.
		return "Waiting on you", m.todoLines(), nil
	case readerAsk:
		return "What would be sent", m.modelPreviewLines(), nil
	case readerScript:
		// No palette: a script's words are not known to be names.
		return m.pager.title, m.pager.lines, nil
	case readerNone, readerHelp, readerWhy:
	}
	return "", nil, nil
}

// readerContent is the body of the reader that is up, laid out as drawn, so scroll
// limits count wrapped rows.
func (m Model) readerContent() []string {
	if !m.reader.boxed() {
		return m.helpBody()
	}
	_, lines, paint := m.readerBody()
	return m.readerLines(lines, m.readerWidth(), paint)
}

// readerBox is a boxed reader overlay: a title and the body laid out by readerLines.
func (m Model) readerBox(title string, lines []string, paint func(string) string) string {
	width := m.readerWidth()
	return m.scrollBox(m.theme.TitleActive.Render(drawLine(title, lineSpec{width: width, sentence: true})),
		m.readerLines(lines, width, paint))
}

// scrollBox is a centered overlay: a rendered title, as much of lines as fits from the
// reader's scroll position, and the help footer.
func (m Model) scrollBox(title string, lines []string) string {
	start := min(m.reader.scroll, len(lines))
	end := min(start+m.helpRows(), len(lines))
	var b strings.Builder
	b.WriteString(title + "\n\n")
	b.WriteString(strings.Join(lines[start:end], "\n"))
	b.WriteString("\n\n" + m.theme.Muted.Render(m.helpFooter(len(lines), end)))
	return m.centeredBox(b.String())
}

// centeredBox frames content in the active pane style, centered on the screen.
func (m Model) centeredBox(content string) string {
	box := m.theme.PaneActive.Padding(1, 3).Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// readerWidth is the overlay text width: the terminal less the box's border and
// padding, capped at readerMaxWidth.
func (m Model) readerWidth() int {
	const chrome = paneBorder + 6 // border, and three cells of padding on each side
	return max(min(m.width-chrome, readerMaxWidth), 20)
}

// readerMaxWidth caps the overlay's measure: a summary is prose.
const readerMaxWidth = 80

// readerLines lays out an overlay body like a message: each line through drawBlock
// (wrap, direction, reorder, RTL flush), keeping its indent on every wrapped row.
// paint styles each drawn row.
func (m Model) readerLines(lines []string, width int, paint func(string) string) []string {
	spec := blockSpec{}
	if paint != nil {
		spec.paint = func(vis string, _ bidi.Direction) string { return paint(vis) }
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			out = append(out, "")
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		spec.width = max(width-len(indent), 8)
		for _, row := range drawBlock(line[len(indent):], spec) {
			out = append(out, indent+row)
		}
	}
	return out
}

// helpRow is one help line: keys padded to keyColumn cells, then what they do.
func helpRow(keyColumn int, keys, what string) string {
	return fmt.Sprintf("  %-*s %s", keyColumn, keys, what)
}

func (m Model) helpView() string {
	title := m.theme.TitleActive.Render("Keybindings")
	if m.reader.filtering || m.reader.filter != "" {
		query := "/" + m.reader.filter
		if m.reader.filtering {
			query += "▏"
		}
		title += "  " + m.theme.Prompt.Render(query)
	}
	return m.scrollBox(title, m.helpBody())
}

// helpBody is the help as shown: every line, or with a filter, the ones that match.
func (m Model) helpBody() []string {
	lines := m.helpLines()
	if m.reader.filter == "" {
		return lines
	}
	if matched := filterHelp(lines, m.reader.filter); len(matched) > 0 {
		return matched
	}
	return []string{m.theme.Muted.Render("nothing matches " + isolate(m.reader.filter))}
}

// filterHelp keeps the help lines that contain query (case-insensitive, styling
// ignored). The help is blocks, a title then its rows, separated by blank lines: a
// block whose title matches stays whole, and otherwise keeps its title over just the
// rows that match, or goes when none do.
func filterHelp(lines []string, query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	matches := func(line string) bool { return strings.Contains(strings.ToLower(ansi.Strip(line)), query) }
	var out []string
	emit := func(block []string) {
		if len(block) == 0 {
			return
		}
		keep := block
		if !matches(block[0]) {
			keep = []string{block[0]}
			for _, row := range block[1:] {
				if matches(row) {
					keep = append(keep, row)
				}
			}
			if len(keep) == 1 {
				return
			}
		}
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, keep...)
	}
	var block []string
	for _, line := range lines {
		if line == "" {
			emit(block)
			block = nil
			continue
		}
		block = append(block, line)
	}
	emit(block)
	return out
}

// helpFooter tells the reader how to leave, and that there is more to see when the
// list is scrolled or clipped.
func (m Model) helpFooter(total, shown int) string {
	leave := m.keys.keyHint(scopeCommand, actHelp)
	if leave == "" {
		leave = m.keys.keyHint(scopeNav, actBack)
	}
	if m.reader.showing(readerHelp) {
		switch {
		case m.reader.filtering:
			return m.keys.keyHint(scopePrompt, actSubmit) + " keep · " + m.keys.keyHint(scopePrompt, actCancel) + " clear"
		case m.reader.filter != "":
			leave = m.keys.keyHint(scopeNav, actBack) + " clear filter · " + leave
		default:
			leave = m.keys.keyHint(scopeCommand, actSearchRoom) + " search · " + leave
		}
	}
	if total > m.helpRows() {
		return fmt.Sprintf("%d–%d of %d · %s scroll · %s close",
			m.reader.scroll+1, shown, total,
			m.keys.keyHint(scopeNav, actDown), leave)
	}
	return leave + " close"
}

// scopeNotes follow a scope's rows in the help overlay.
var scopeNotes = map[scope][]string{
	scopeEdit: {
		"  a field's own binding wins: {emoji.open} browses emoji in the composer,",
		"  because [keys] puts it there.",
		"  home and end are the line's ends in a field, not the timeline's.",
	},
}

// helpRows is how many binding rows the overlay can show: the frame height less the
// box border, its padding, the title, and the footer.
func (m Model) helpRows() int {
	const chrome = paneBorder + 2 + 2 + 2 // border, vertical padding, title + blank, blank + footer
	return max(m.height-chrome, 1)
}

// helpLines builds the overlay's body from keyActions — one section per scope, in
// that table's order — followed by any keymap config issues. Actions left with no
// key at all are listed as "(unbound)" rather than hidden, so a binding you meant
// to set and mistyped is visible.
func (m Model) helpLines() []string {
	const keyColumn = 16
	var lines []string
	for _, section := range scopeTitles {
		var rows []string
		for _, row := range keyActions {
			if row.scope != section.scope {
				continue
			}
			keys := strings.Join(m.keys.keysFor(row.scope, row.act), ", ")
			if keys == "" {
				keys = "(unbound)"
			}
			rows = append(rows, helpRow(keyColumn, keys, row.label()))
		}
		if len(rows) == 0 {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.theme.Title.Render(section.title))
		lines = append(lines, rows...)
		for _, note := range scopeNotes[section.scope] {
			lines = append(lines, m.liveKeys(note))
		}
	}
	// Jump and script bindings are not in keyActions, so they are listed separately.
	lines = append(lines, m.jumpHelp(keyColumn)...)
	lines = append(lines, m.scriptHelp(keyColumn)...)
	lines = append(lines, m.commandHelp(keyColumn)...)
	if issues := m.keys.issues; len(issues) > 0 {
		lines = append(lines, "", m.theme.Badge(true).Render("Keybinding config issues"))
		for _, issue := range issues {
			// An issue quotes the config, which can name a Hebrew room.
			lines = append(lines, "  "+drawSentence(issue))
		}
	}
	return lines
}
