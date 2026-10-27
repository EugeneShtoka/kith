package tui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// WhatsApp people: their mentions carry no link, so nothing else sends a row down the
// offset-mapped path (a Matrix pill's link does).
const (
	colorMe  = "whatsapp:15550001@s.whatsapp.net"
	colorBob = "whatsapp:15550002@s.whatsapp.net"
)

// colorRoom is a room where Bob has spoken (so he has a hue) and the given message follows.
func colorRoom(t *testing.T, msg domain.Message) Model {
	t.Helper()
	disp := config.Display{
		ColorMessages: true,
		Identities:    []config.Identity{{Alias: "Me", Color: "#8ff586", IDs: []string{colorMe}}},
	}
	m := sized(t, withRooms(t, starterNew(apitest.Nop{}, disp)))
	return loadPage(t, m, []domain.Message{
		{ID: mx + "$0", RoomID: mx + "!a:x", Sender: colorBob, Body: "hello", Timestamp: at(1)},
		msg,
	})
}

// sgr is the truecolor foreground escape parameter for c.
func sgr(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

// A mention on a right-to-left line is drawn in the mentioned person's color, in your
// own messages as in anyone's: Hebrew lines took a path that skipped mentions.
func TestAMentionOnARightToLeftLineHasThePersonsColor(t *testing.T) {
	t.Parallel()

	for _, sender := range []string{colorMe, "whatsapp:15550003@s.whatsapp.net"} {
		msg := domain.Message{
			ID: mx + "$1", RoomID: mx + "!a:x", Sender: sender, Body: "שלום Bob מה נשמע", Timestamp: at(2),
			Mentions: []domain.Mention{{UserID: colorBob, Name: "Bob"}},
		}
		m := colorRoom(t, msg)
		colors := m.senderColorMap()
		bob, ok := colors[colorBob]
		if !ok {
			t.Fatal("Bob has no hue though he spoke")
		}
		rows := strings.Join(m.messageRows(m.timeline.messages[1], 100, 15, colors, false, ""), "\n")
		if !strings.Contains(rows, "Bob") {
			t.Fatalf("%s: the mention is not drawn:\n%q", sender, rows)
		}
		if !strings.Contains(rows, sgr(bob)) {
			t.Errorf("%s: the mention of Bob on a right-to-left line is not in his color:\n%q", sender, rows)
		}
	}
}

// A mentioned name that wraps onto the next row is colored on both rows: each row was
// searched for the whole name.
func TestAMentionWrappedAcrossRowsHasThePersonsColor(t *testing.T) {
	t.Parallel()

	const lead = "aaaa bbbb cccc "
	msg := domain.Message{
		ID: mx + "$1", RoomID: mx + "!a:x", Sender: colorMe, Body: lead + "Bob Robertson ok", Timestamp: at(2),
		Mentions: []domain.Mention{{UserID: colorBob, Name: "Bob Robertson"}},
	}
	m := colorRoom(t, msg)
	colors := m.senderColorMap()
	const nameW = 15
	// The body column ends just after "Bob", so "Robertson" wraps to the next row.
	width := m.bodyColumn(nameW) + len(lead+"Bob") + 1
	drawn := m.messageRows(m.timeline.messages[1], width, nameW, colors, false, "")
	if len(drawn) < 2 {
		t.Fatalf("the body did not wrap at width %d:\n%q", width, drawn)
	}
	code := sgr(colors[colorBob])
	for _, part := range []string{"Bob", "Robertson"} {
		found := false
		for _, row := range drawn {
			if strings.Contains(row, part) && strings.Contains(row, code) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q of the wrapped mention is not in Bob's color:\n%q", part, drawn)
		}
	}
}
