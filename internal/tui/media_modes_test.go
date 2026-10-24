package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Half-block rows land in the layout under their message, laid out like text.
func TestHalfBlocksAreLaidOutAsText(t *testing.T) {
	t.Parallel()

	m := inlineModel(t)
	m = update(t, m, oneImage())
	if !m.pics.imageLoading[mx+"$1"] {
		t.Fatal("inline mode did not start fetching the picture")
	}

	before := len(m.layoutRows())
	m = update(t, m, imageLoadedMsg{eventID: mx + "$1", rows: []string{"▄▄▄", "▄▄▄", "▄▄▄"}})
	if got := len(m.layoutRows()) - before; got != 3 {
		t.Errorf("a three-row drawing added %d rows to the layout, want 3", got)
	}
	if rows := strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "▄▄▄") {
		t.Errorf("the drawing is not in the timeline:\n%s", rows)
	}
}

// A picture running off the window edge is cut, like a long message.
func TestAHalfBlockPictureIsCutLikeText(t *testing.T) {
	t.Parallel()

	m := inlineModel(t)
	msgs := []domain.Message{oneImage().page.Messages[0]}
	for i := range 40 {
		msgs = append(msgs, domain.Message{
			ID: domain.EventID(mx + "$t" + string(rune('a'+i))), RoomID: mx + "!a:x",
			Sender: "matrix:@a:x", SenderName: "Alice", Timestamp: at(i + 2), Body: "talk",
		})
	}
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: msgs}})
	m = update(t, m, imageLoadedMsg{eventID: mx + "$1", rows: []string{"r1", "r2", "r3", "r4", "r5", "r6"}})

	rows, inner := m.msgAreaRows(), m.contentWidth()
	total := len(m.layoutRows())
	// At every scroll position the pane is exactly full, with no panic on a picture straddling
	// an edge.
	for scroll := range total {
		m.timeline.scroll = scroll
		if got := len(m.messageLines(rows, inner)); got != rows {
			t.Fatalf("scroll %d produced %d lines, want exactly %d", scroll, got, rows)
		}
	}
}

// Pictures are only fetched once a room is open.
func TestBrowsingTheRoomListFetchesNoPictures(t *testing.T) {
	t.Parallel()

	m := inlineModel(t)
	m.focus = paneRooms
	m = update(t, m, oneImage())
	if len(m.pics.imageLoading) != 0 {
		t.Errorf("glancing at a room fetched %d picture(s)", len(m.pics.imageLoading))
	}
	// Nor is space held for them, so the preview has no holes.
	m = update(t, m, imageLoadedMsg{eventID: mx + "$1", rows: []string{"r1", "r2", "r3"}})
	if rows := strings.Join(m.layoutRows(), "\n"); strings.Contains(rows, "r1") {
		t.Errorf("a room being glanced at drew its picture:\n%s", rows)
	}

	m.focus = paneTimeline
	if rows := strings.Join(m.layoutRows(), "\n"); !strings.Contains(rows, "r1") {
		t.Error("opening the room did not show the picture it had already drawn")
	}
}

// A resize re-fits pictures, which are drawn to a fixed cell count.
func TestResizeRefitsThePictures(t *testing.T) {
	t.Parallel()

	m := inlineModel(t)
	m = update(t, m, oneImage())
	m = update(t, m, imageLoadedMsg{eventID: mx + "$1", rows: []string{"r1", "r2"}})
	if len(m.pics.imageRows[mx+"$1"]) == 0 {
		t.Fatal("the picture did not land in the cache")
	}

	m = update(t, m, tea.WindowSizeMsg{Width: 60, Height: 30})
	if rows, cached := m.pics.imageRows[mx+"$1"]; cached && len(rows) > 0 {
		t.Error("a narrower window kept the picture drawn at the old width")
	}
	if !m.pics.imageLoading[mx+"$1"] {
		t.Error("a narrower window did not start re-fitting the picture")
	}
}

// inlineModel is a client drawing pictures with half-blocks, in an open room.
func inlineModel(t *testing.T) Model {
	t.Helper()

	disp := config.Display{Media: config.Media{Mode: "inline"}}
	m := sized(t, withRooms(t, starterNew(apitest.Nop{}, disp)))
	m.focus = paneTimeline
	return m
}

// oneImage is a timeline of a single picture.
func oneImage() timelineMsg {
	return timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{
		Messages: []domain.Message{{
			ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@a:x", Timestamp: at(1),
			Media: &domain.Media{Type: domain.MediaImage, Name: "cat.jpg", Mime: "image/jpeg", Width: 40, Height: 40},
		}},
	}}
}
