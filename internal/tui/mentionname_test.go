package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// WhatsApp writes a mention as "@" and a number. It is drawn as "@" and the name the
// person is known by: their number until a name is known, and the member list's name
// once it arrives, which may be after the row was first drawn.
func TestANumberMentionIsDrawnWithTheName(t *testing.T) {
	t.Parallel()
	const eli = "whatsapp:1500000005@s.whatsapp.net"
	m := withRooms(t, newModel())
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	m.openRoom = "!a:x"
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{{
		ID: "$1", RoomID: "!a:x", Sender: "@maya:x", SenderName: "Maya", Body: "@100000000000005 can you look?",
		Timestamp: at(1), Mentions: []domain.Mention{{UserID: eli, Name: "@100000000000005"}},
	}}}})
	m = settled(m) // drawn from the shared row cache from here on
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "100000000000005") || !strings.Contains(view, "@+1500000005 can you look?") {
		t.Errorf("before a name is known, the mention is not the number:\n%s", view)
	}

	m = settled(update(t, m, membersMsg{roomID: "!a:x", members: []domain.Member{{UserID: eli, DisplayName: "Eli Cohen"}}}))
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "@Eli Cohen can you look?") {
		t.Errorf("the member's name does not reach the drawn row:\n%s", view)
	}
}

// Every mention of a person is drawn with who they are to this person now, not the
// words it was sent with: a Telegram mention by the person's alias for them, a Matrix
// one by the member's name now; and where the room's space keeps first names only,
// a mention is a first name too, as the sender column is.
func TestEveryMentionIsDrawnWithTheNameKnownNow(t *testing.T) {
	t.Parallel()
	m := withRooms(t, newModel())
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 20})
	m.openRoom = "!a:x"
	m.prefs.identities = map[string]resolvedIdentity{"telegram:7": {alias: "Vanya Sokolov"}}
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{
			ID: "$1", RoomID: "!a:x", Sender: "@maya:x", SenderName: "Maya", Body: "Ivan Petrov, thoughts?", Timestamp: at(1),
			Mentions: []domain.Mention{{UserID: "telegram:7", Name: "Ivan Petrov"}},
		},
		{
			ID: "$2", RoomID: "!a:x", Sender: "@maya:x", SenderName: "Maya", Body: "ask Robert", Timestamp: at(2),
			Mentions: []domain.Mention{{UserID: "@rob:x", Name: "Robert"}},
		},
	}}})
	m = settled(update(t, m, membersMsg{roomID: "!a:x", members: []domain.Member{{UserID: "@rob:x", DisplayName: "Rob Stone"}}}))
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Vanya Sokolov, thoughts?") || !strings.Contains(view, "ask Rob Stone") {
		t.Errorf("the mentions are not drawn with the names known now:\n%s", view)
	}

	if got := m.mentionedName("telegram:7", "Ivan Petrov", "!a:x"); got != "Vanya Sokolov" {
		t.Fatalf("mentionedName = %q", got)
	}
	m.prefs.display.SpaceRules = []config.SpaceRule{{Space: m.ownSpace("!a:x"), FirstNameOnly: true}}
	if got := m.mentionedName("telegram:7", "Ivan Petrov", "!a:x"); got != "Vanya" {
		t.Errorf("in a first-names space the mention = %q, want Vanya", got)
	}
}
