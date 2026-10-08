package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// searchTitle names the results pane, so the pane header says what is being
// searched even once the prompt has closed.
func (m Model) searchTitle() string {
	title := m.search.showing().name() + " · " + m.scopeLabel(m.search.scope)
	if q := strings.TrimSpace(m.search.query); q != "" {
		return title + ": " + q
	}
	return title
}

// searchLines renders a summary row, then one windowed row per hit.
func (m Model) searchLines(width, rows int) []string {
	// A sentence of ours with the query in it, which is whatever was typed.
	header := []string{m.theme.Muted.Render(drawLine(m.searchSummary(), lineSpec{width: width, sentence: true}))}
	if rows <= 1 || len(m.search.hits) == 0 {
		return header
	}
	body := rows - 1
	start := windowStart(m.search.cursor, body, len(m.search.hits))
	end := min(start+body, len(m.search.hits))

	out := header
	for i := start; i < end; i++ {
		out = append(out, m.searchRow(m.search.hits[i], i == m.search.cursor, width))
	}
	return out
}

// searchRow renders one hit. The excerpt is flattened to a single line — a message
// body can contain newlines, and a results list is only legible one row per result.
func (m Model) searchRow(hit domain.SearchHit, selected bool, width int) string {
	when := m.prefs.clock.ShortDate(hit.Timestamp) + " " + m.prefs.clock.Time(hit.Timestamp)
	who := hit.SenderName
	if who == "" {
		who = hit.Sender
	}
	// Cut before reordering (see nameCell), so RTL names keep their beginning.
	prefix := cursor(selected) + when + "  " + nameCell(who, searchNameWidth)
	if m.search.global() {
		if room, ok := m.roomByID(hit.RoomID); ok {
			prefix += "  [" + nameCell(m.roomLabel(room), searchRoomWidth) + "]"
		}
	}
	// The row is styled as a unit and the excerpt's highlights are applied inside
	// it, so the selection and the match emphasis compose instead of fighting.
	row := m.theme.Row(selected, m.focus == paneTimeline).Render(clamp(prefix+"  ", width))
	room := width - ansi.StringWidth(row)
	excerpt := flatten(hit.Snippet)

	// The file list leads with the file name; elsewhere the words are the answer. Name
	// and caption are drawn separately so an ASCII name cannot make a Hebrew caption LTR.
	if label := m.search.showing().label(hit); label != "" {
		// The row's grouping label, in the tracked hue.
		tag := lipgloss.NewStyle().Foreground(m.theme.Palette.BadgeAlert).Render(displayName(label) + " ")
		room -= ansi.StringWidth(label) + 1
		row += tag
	}
	if m.search.showing().showsFileName() && hit.FileName != "" {
		name := nameCell(hit.FileName, room-2) + "  "
		// Bridges repeat the file name as the body; only a real caption is shown.
		rest := room - ansi.StringWidth(name)
		if excerpt == "" || stripHighlights(excerpt) == hit.FileName || rest <= 0 {
			return row + m.theme.Muted.Render(name)
		}
		return row + m.theme.Muted.Render(name) + m.excerptCell(excerpt, rest)
	}
	return row + m.excerptCell(excerpt, room)
}

// excerptCell renders a result's text into width: cut logically, reordered when RTL,
// with the engine's matched runs emphasized by logical offset.
func (m Model) excerptCell(excerpt string, width int) string {
	if width < 1 {
		return ""
	}
	plain, matches := highlightRanges(excerpt)
	muted := ansi.Style{}.ForegroundColor(ansiColor(m.theme.Palette.Muted))
	match := ansi.Style{}.ForegroundColor(ansiColor(m.theme.Palette.Accent)).Bold()
	return drawLine(plain, lineSpec{width: width, marks: func(off int) (styleKey, ansi.Style, string) {
		for _, r := range matches {
			if off >= r.start && off < r.end {
				return 2, match, ""
			}
		}
		return 1, muted, ""
	}})
}

// highlightRanges takes the match markers out of a search excerpt, returning the plain
// text and where the matches are in it, as logical byte ranges. A marker pair a
// truncated snippet cut open runs to the end.
func highlightRanges(snippet string) (string, []trackedRange) {
	var b strings.Builder
	var out []trackedRange
	for snippet != "" {
		plain, rest, found := strings.Cut(snippet, domain.HighlightStart)
		b.WriteString(strings.ReplaceAll(plain, domain.HighlightEnd, ""))
		if !found {
			break
		}
		match, after, _ := strings.Cut(rest, domain.HighlightEnd)
		start := b.Len()
		b.WriteString(strings.ReplaceAll(match, domain.HighlightStart, ""))
		if b.Len() > start {
			out = append(out, trackedRange{start: start, end: b.Len()})
		}
		snippet = after
	}
	return b.String(), out
}

// stripHighlights removes the match markers, giving the excerpt's plain text — for
// measuring it, and for the RTL path that cannot carry styling.
func stripHighlights(s string) string {
	return strings.NewReplacer(domain.HighlightStart, "", domain.HighlightEnd, "").Replace(s)
}

// searchNameWidth and searchRoomWidth keep the result rows' columns aligned without
// letting one long name push the excerpt off the pane.
const (
	searchNameWidth = 16
	searchRoomWidth = 14
)

// flatten collapses the whitespace in a snippet to single spaces, so a multi-line
// message body still occupies one row of the results list.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }
