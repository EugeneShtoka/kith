package tui

import (
	"context"
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// editingWith opens a room holding mine, with members to complete from.
func editingWith(t *testing.T, mine domain.Message, members ...domain.Member) (Model, *sendingBackend) {
	t.Helper()
	backend := &sendingBackend{}
	m := update(t, New(context.Background(), backend, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	m.me = "@me:x"
	m, _ = m.selectRoom(m.filteredRooms()[0])
	mine.ID, mine.RoomID, mine.Sender, mine.Timestamp = "$mine", "!a:x", "@me:x", at(1)
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{mine}}})
	m = update(t, m, membersMsg{roomID: "!a:x", members: members})
	m.focus, m.compose.insertMode = paneTimeline, false
	return m, backend
}

// sentEdit sends the composer and returns the one revision that went out.
func sentEdit(t *testing.T, m Model, backend *sendingBackend) domain.Draft {
	t.Helper()
	m, cmd := press(t, m, sendKey())
	deliver(t, m, cmd)
	if len(backend.sent) != 1 || backend.sent[0].Edits != "$mine" {
		t.Fatalf("sent %+v, want one revision of $mine", backend.sent)
	}
	return backend.sent[0]
}

// Revising a message that names someone keeps naming them: the pill is not lost.
func TestAnEditKeepsTheMessagesMentions(t *testing.T) {
	t.Parallel()

	m, backend := editingWith(t, domain.Message{
		Body:     "Dana Levi can you look",
		Mentions: []domain.Mention{{UserID: "@dana:x", Name: "Dana Levi"}},
	})
	m, _ = press(t, m, keyText("E"))
	m = typeInto(t, m, " today")

	draft := sentEdit(t, m, backend)
	if live := draft.LiveMentions(); len(live) != 1 || live[0].UserID != "@dana:x" {
		t.Errorf("mentions = %+v, want dana's pill carried into the revision", live)
	}
}

// A name completed while editing becomes a mention, as it does in a new message.
func TestAMentionCompletedInAnEditIsSent(t *testing.T) {
	t.Parallel()

	m, backend := editingWith(t, domain.Message{Body: "can you look"}, people...)
	m, _ = press(t, m, keyText("E"))
	m = typeInto(t, m, " @dana")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})

	draft := sentEdit(t, m, backend)
	if draft.Body != "can you look Dana Levi" {
		t.Errorf("Body = %q", draft.Body)
	}
	if live := draft.LiveMentions(); len(live) != 1 || live[0].UserID != "@dana:x" {
		t.Errorf("mentions = %+v, want the completed name as a mention", live)
	}
}

// A message that mentions you draws its sender in their own color, not another.
func TestMentioningYouDoesNotRecolorTheSender(t *testing.T) {
	t.Parallel()

	m, _ := editingWith(t, domain.Message{Body: "x"})
	sagi := color.RGBA{R: 0xd0, G: 0xa0, B: 0x60, A: 0xff}
	plain := domain.Message{Sender: "@sagi:x", SenderName: "Sagi", Body: "hi", Timestamp: at(2)}
	naming := plain
	naming.Mentioned = true
	for _, selected := range []bool{false, true} {
		_, want := m.senderCells(plain, 8, sagi, selected)
		_, got := m.senderCells(naming, 8, sagi, selected)
		if got != want {
			t.Errorf("selected=%v: mentioning name = %q, want it drawn as %q", selected, got, want)
		}
	}
}
