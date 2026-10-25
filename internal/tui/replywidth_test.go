package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A Hebrew+emoji reply's rows (the reply draws an extra row outside the body path)
// must never exceed the pane: an overflowing row wraps and leaves residue on screen.
func TestReplyRowsFitThePane(t *testing.T) {
	t.Parallel()

	const (
		hebrewEmoji = "וואלה מדהים, נשמע שאתה מוכן לפנסיה 🤣 איזה כיף זה עבודה ברגוע ולחיות נורמלי"
		quoted      = "אמא בסדר, היה לה ממש כשה, במיוחד שהינו צריכים לצאת פעמיים מבולגריה ל3 חודשים"
	)
	// Swept, since where the wrap falls decides the width; from 100 because below
	// that the date divider alone overflows.
	for termWidth := 100; termWidth <= 260; termWidth++ {
		m := withRooms(t, newModel())
		m = update(t, m, tea.WindowSizeMsg{Width: termWidth, Height: 30})
		m.openRoom = mx + "!a:x"
		m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
			{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@maya:x", SenderName: "Maya Rosen", Body: quoted, Timestamp: at(1)},
			{
				ID: mx + "$2", RoomID: mx + "!a:x", Sender: "matrix:@maya:x", SenderName: "Maya Rosen",
				Body: hebrewEmoji, ReplyTo: mx + "$1", Timestamp: at(2),
			},
		}}})

		width := m.contentWidth()
		for i, row := range m.layoutRows() {
			if got := ansi.StringWidth(row); got > width {
				t.Fatalf("terminal %d: row %d is %d columns in a %d-column pane: %q",
					termWidth, i, got, width, stripStyles(row))
			}
		}
	}
}
