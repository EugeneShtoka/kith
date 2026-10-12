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

// tagKeyHelp lists the [keys.tag] bindings, sorted.
func (m Model) tagKeyHelp(keyColumn int) []string {
	if len(m.keys.tagKeys) == 0 {
		return nil
	}
	lines := []string{"", m.theme.Title.Render("Into a tag, or out of it (your [keys.tag] bindings)")}
	for _, seq := range slices.Sorted(maps.Keys(m.keys.tagKeys)) {
		tag := m.keys.tagKeys[seq]
		label := "this room in " + isolate(tag) + ", or out of it"
		if _, ok := m.rail.tags.Index(tag); !ok {
			label = isolate(tag) + "  (no such tag)"
		}
		lines = append(lines, helpRow(keyColumn, spellSequence(seq), drawSentence(label)))
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
	if target.Kind == domain.JumpHome {
		if key, found := m.groupNamed(target.Name); found {
			return placeWord(key) + " " + isolate(m.rail.groups[indexOfGroup(m.rail.groups, key)].label)
		}
		return target.String() + "  (nothing in the rail here)"
	}
	if room, found := m.roomNamed(target); found {
		return m.roomName(room)
	}
	return target.String() + "  (nothing here matches)"
}

// readerBody is the title, body and paint of the open boxed reader; the scroll limit
// uses the same answer (readerContent).
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

// readerContent is the open reader's body laid out as drawn, so scroll limits count
// wrapped rows.
func (m Model) readerContent() []string {
	if !m.reader.boxed() {
		return m.helpBody()
	}
	_, lines, paint := m.readerBody()
	return m.readerLines(lines, m.readerWidth(), paint)
}

func (m Model) readerBox(title string, lines []string, paint func(string) string) string {
	width := m.readerWidth()
	return m.scrollBox(m.theme.TitleActive.Render(drawLine(title, lineSpec{width: width, sentence: true})),
		m.readerLines(lines, width, paint))
}

// scrollBox is a centered overlay: a title, as much of lines as fits from the scroll
// position, and the help footer.
func (m Model) scrollBox(title string, lines []string) string {
	start := min(m.reader.scroll, len(lines))
	end := min(start+m.helpRows(), len(lines))
	var b strings.Builder
	b.WriteString(title + "\n\n")
	b.WriteString(strings.Join(lines[start:end], "\n"))
	b.WriteString("\n\n" + m.theme.Muted.Render(m.helpFooter(len(lines), end)))
	return m.centeredBox(b.String())
}

func (m Model) centeredBox(content string) string {
	box := m.theme.PaneActive.Padding(1, 3).Render(content)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// readerWidth is the overlay text width: the terminal less border and padding, capped
// at readerMaxWidth.
func (m Model) readerWidth() int {
	const chrome = paneBorder + 6 // border, and three cells of padding on each side
	return max(min(m.width-chrome, readerMaxWidth), 20)
}

// readerMaxWidth caps the overlay's measure: a summary is prose.
const readerMaxWidth = 80

// readerLines lays out an overlay body like a message, each line through drawBlock,
// keeping its indent on wrapped rows. paint styles each drawn row.
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

// filterHelp keeps help lines containing query (case-insensitive, styling ignored).
// The help is blank-separated blocks of a title then rows: a block whose title matches
// stays whole, otherwise it keeps its title over the matching rows, or goes.
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

// helpFooter says how to leave, and that there is more when scrolled or clipped.
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

var scopeNotes = map[scope][]string{
	scopeEdit: {
		"  a field's own binding wins: {emoji.open} browses emoji in the composer,",
		"  because [keys] puts it there.",
		"  home and end are the line's ends in a field, not the timeline's.",
	},
}

// helpRows is how many binding rows the overlay shows: the frame height less border,
// padding, title and footer.
func (m Model) helpRows() int {
	const chrome = paneBorder + 2 + 2 + 2 // border, vertical padding, title + blank, blank + footer
	return max(m.height-chrome, 1)
}

// helpLines builds the overlay from keyActions, one section per scope in table order,
// then keymap config issues. Actions with no key are listed as "(unbound)" so a
// mistyped binding is visible.
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
	lines = append(lines, m.tagKeyHelp(keyColumn)...)
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
