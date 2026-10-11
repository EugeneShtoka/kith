package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// searchTitle names the results pane, so the header says what is searched even after
// the prompt closes.
func (m Model) searchTitle() string {
	title := m.search.showing().name() + " · " + m.scopeLabel(m.search.scope)
	if q := strings.TrimSpace(m.search.query); q != "" {
		return title + ": " + q
	}
	return title
}

// searchLines renders a summary row, then one windowed row per hit.
func (m Model) searchLines(width, rows int) []string {
	// Our sentence with the typed query in it.
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

// searchRow renders one hit, the excerpt flattened to a single line.
func (m Model) searchRow(hit domain.SearchHit, selected bool, width int) string {
	when := m.prefs.clock.ShortDate(hit.Timestamp) + " " + m.prefs.clock.Time(hit.Timestamp)
	// Named as the timeline names people, the excerpt's mentions too.
	who := m.mentionedName(hit.Sender, hit.RoomID, hit.SenderName)
	// Cut before reordering (see nameCell), so RTL names keep their beginning.
	prefix := cursor(selected) + when + "  " + nameCell(who, searchNameWidth)
	if m.search.global() {
		if room, ok := m.roomByID(hit.RoomID); ok {
			prefix += "  [" + nameCell(m.roomLabel(room), searchRoomWidth) + "]"
		}
	}
	// The row is styled as a unit with highlights applied inside, so selection and match
	// emphasis compose.
	row := m.theme.Row(selected, m.focus == paneTimeline).Render(clamp(prefix+"  ", width))
	room := width - ansi.StringWidth(row)
	snippet, _ := domain.ResolveMentions(hit.Snippet, hit.Mentions, func(mn domain.Mention, words string) string {
		return m.mentionedName(mn.UserID, hit.RoomID, mn.Known, words)
	})
	excerpt := flatten(snippet)

	// The file list leads with the file name. Name and caption are drawn separately so an
	// ASCII name can't make a Hebrew caption LTR.
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
// the engine's matches emphasized by logical offset.
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

// highlightRanges strips the match markers from an excerpt, returning the plain text
// and the matches as logical byte ranges. A pair a truncated snippet cut open runs to
// the end.
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

// stripHighlights removes the match markers, for measuring and for the RTL path that
// can't carry styling.
func stripHighlights(s string) string {
	return strings.NewReplacer(domain.HighlightStart, "", domain.HighlightEnd, "").Replace(s)
}

// searchNameWidth and searchRoomWidth keep result columns aligned without letting one
// long name push the excerpt off the pane.
const (
	searchNameWidth = 16
	searchRoomWidth = 14
)

// flatten collapses whitespace to single spaces so a multi-line body takes one row.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }
