package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Layout constants. minWidth/minHeight are the smallest frame rendered rather than a
// corrupted layout.
const (
	railWidth  = 18
	roomsWidth = 30
	minWidth   = railWidth + roomsWidth + 26
	minHeight  = 12
	// paneBorder is what a rounded border costs a pane per axis (lipgloss v2 counts it
	// inside Width; see framePane).
	paneBorder = 2
)

// View renders the three-pane frame with a status line beneath it. It writes nothing
// shared. For the Model Update returned, the shared cache is final (primeTimeline, and
// TestViewLeavesTheSharedCacheAlone), so View only reads it; any other Model (an older
// copy, one built by hand) is laid out on a private scratch cache.
func (m Model) View() tea.View {
	if m.derived == nil || !m.frames.isPrimed(m.frame) {
		m.derived = m.derived.scratch()
	}
	var content string
	switch {
	case !m.ready || m.width < minWidth || m.height < minHeight:
		content = m.tooSmallView()
	case m.reader.showing(readerWhy):
		content = m.whyView()
	case m.reader.showing(readerHelp):
		content = m.helpView()
	case m.reader.boxed():
		title, lines, paint := m.readerBody()
		content = m.readerBox(title, lines, paint)
	case m.verify.active:
		content = m.verifyView()
	default:
		content = m.frameView()
	}
	content, cursor := takeCaret(content)
	view := tea.NewView(content)
	view.AltScreen = true
	view.MouseMode = m.mouseMode()
	view.Cursor = cursor
	return view
}

// takeCaret strips the caret marker from a frame and turns its position into the
// terminal cursor; a frame without one leaves the cursor hidden.
func takeCaret(frame string) (string, *tea.Cursor) {
	before0, _, ok := strings.Cut(frame, caretMark)
	if !ok {
		return frame, nil
	}
	// Measured off the frame, so it cannot disagree with the pane layout.
	before := before0
	cursor := tea.NewCursor(
		ansi.StringWidth(before[strings.LastIndexByte(before, '\n')+1:]),
		strings.Count(before, "\n"),
	)
	cursor.Shape = tea.CursorBar
	return strings.Replace(frame, caretMark, "", 1), cursor
}

func (m Model) tooSmallView() string {
	if !m.ready {
		return "kith — starting…"
	}
	return "kith — terminal too small; please enlarge the window"
}

func (m Model) frameView() string {
	// Every part of one frame (the list, its cursor, the legend) reads one room list.
	m.frameRows, m.hasFrameRows = m.roomRows(), true
	timeWidth := m.width - railWidth - roomsWidth
	// A loaded player takes a row from the panes rather than covering the newest message.
	player := m.renderPlayer(m.width)
	paneHeight := m.height - 1
	if player != "" {
		paneHeight--
	}

	panes := lipgloss.JoinHorizontal(
		lipgloss.Top,
		m.renderRail(paneHeight),
		m.renderRooms(roomsWidth, paneHeight),
		m.renderTimeline(timeWidth, paneHeight),
	)
	if player != "" {
		panes += "\n" + player
	}
	return panes + "\n" + m.renderStatus()
}

// verifyView renders the interactive device-verification overlay as a centered
// box: a request prompt, or the SAS emoji to compare with the other device.
func (m Model) verifyView() string {
	var b strings.Builder
	b.WriteString(m.theme.TitleActive.Render("Device verification") + "\n\n")

	switch m.verify.stage {
	case domain.VerificationDone, domain.VerificationCanceled, domain.VerificationRestored:
		// Terminal stages: the overlay is dismissed on arrival.
	case domain.VerificationSAS:
		b.WriteString("Compare these emoji with the other device:\n\n")
		b.WriteString(sasGrid(m.verify.emojis))
		if len(m.verify.decimals) > 0 {
			b.WriteString("\n\n" + m.theme.Muted.Render(decimalLine(m.verify.decimals)))
		}
		if m.verify.waiting {
			b.WriteString("\n\n" + m.theme.Muted.Render("confirming…"))
		} else {
			b.WriteString("\n\nDo they match?   [y] yes   [n] no")
		}
	case domain.VerificationRequested:
		if m.verify.ours {
			// Our own request: nothing to accept, only to cancel.
			b.WriteString("Asked your other sessions to verify this one.\n\n")
			b.WriteString(m.theme.Muted.Render("Accept it there — the emoji appear here when you do."))
			b.WriteString("\n\n[n] cancel")
			break
		}
		// Both are names somebody chose — the account's and the device's.
		who := isolate(m.verify.from)
		if m.verify.device != "" {
			who += " (" + isolate(m.verify.device) + ")"
		}
		b.WriteString(drawSentence(who) + "\nwants to verify this session.\n\n")
		if m.verify.waiting {
			b.WriteString(m.theme.Muted.Render("waiting for the other device…"))
		} else {
			b.WriteString("[y] accept   [n] reject")
		}
	}

	return m.centeredBox(b.String())
}

// sasGrid lays the SAS emoji out a few per row, each as its glyph plus name.
func sasGrid(emojis []domain.SASEmoji) string {
	const perRow = 4
	var b strings.Builder
	for i, e := range emojis {
		fmt.Fprintf(&b, "%s %-11s", e.Glyph, e.Name)
		if (i+1)%perRow == 0 && i != len(emojis)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// decimalLine formats the SAS decimal fallback as a space-separated line.
func decimalLine(decimals []int) string {
	parts := make([]string, len(decimals))
	for i, d := range decimals {
		parts[i] = strconv.Itoa(d)
	}
	return "decimals: " + strings.Join(parts, " ")
}

// paneBodyRows is how many content rows a pane of height h shows (less border and
// header). framePane clips silently past it.
func paneBodyRows(h int) int {
	return max(h-3, 1)
}

// windowRows is the slice of rows that keeps cursor visible, scrolled the least.
func windowRows(rows []string, cursor, visible int) []string {
	if len(rows) <= visible {
		return rows
	}
	first := windowStart(cursor, visible, len(rows))
	return rows[first:min(first+visible, len(rows))]
}

func containsKey(keys []string, k string) bool { return slices.Contains(keys, k) }

// framePane draws a titled, bordered pane: a header row, the given body lines,
// then blank padding to fill the height. Every line is clamped to the interior
// width so lipgloss never wraps (a wrapped line corrupts the fixed layout), and
// the accent border/title marks the focused pane.
func (m Model) framePane(title string, lines []string, w, h int, active bool) string {
	inner := w - paneBorder
	innerH := h - paneBorder
	frame, titleStyle := m.theme.Pane, m.theme.Title
	if active {
		frame, titleStyle = m.theme.PaneActive, m.theme.TitleActive
	}

	rows := make([]string, 0, innerH)
	// The title is logical text, drawn here for every pane.
	rows = append(rows, clamp(titleStyle.Render(drawLine(title, lineSpec{width: inner, sentence: true})), inner))
	for _, line := range lines {
		if len(rows) >= innerH {
			break
		}
		rows = append(rows, clamp(line, inner))
	}
	for len(rows) < innerH {
		rows = append(rows, "")
	}
	// lipgloss v2 counts the border within Width/Height, so pass the outer
	// dimensions; the interior resolves to inner×innerH for the clamped content.
	return frame.Width(w).Height(h).Render(strings.Join(rows, "\n"))
}

// oneLine collapses newlines and tabs so a string occupies one row.
var oneLine = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

// clamp flattens a (possibly styled) string to a single row and hard-truncates
// it to w cells without wrapping, so a long or multi-line message can't break
// the pane geometry.
func clamp(s string, w int) string {
	if w < 1 {
		w = 1
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(oneLine.Replace(s))
}

// cursorWidth is what cursor() renders, marked or not. The column is reserved on every
// row so selection cannot shift labels sideways as it moves down a list.
const cursorWidth = 2

// cursor is the selection marker prefix for a list row.
func cursor(selected bool) string {
	if selected {
		return "▸ "
	}
	return "  "
}

// escapeByte starts every ANSI sequence; a line containing one is already styled.
const escapeByte = '\x1b'
