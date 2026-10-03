package tui

import (
	"context"
	"errors"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// selvesBackend answers who this person is with ids, counting the asks.
type selvesBackend struct {
	apitest.Nop
	ids  []string
	asks *int
}

func (b selvesBackend) Selves(context.Context) ([]string, error) {
	*b.asks++
	return b.ids, nil
}

const ownWhatsApp = "whatsapp:359880000001@s.whatsapp.net"

// Every ID the daemon names is this person, beside the Matrix account; nobody is "".
func TestIsMeIsEveryAccount(t *testing.T) {
	t.Parallel()
	m := newModel().WithRules(nil, "@me:x", false)
	m, _ = m.handleSelves(selvesMsg{ids: []string{"@me:x", ownWhatsApp}})
	for _, id := range []string{"@me:x", ownWhatsApp} {
		if !m.isMe(id) || !m.fromMe(id) {
			t.Errorf("isMe(%q) = false", id)
		}
	}
	for _, id := range []string{"", "@dana:x", "whatsapp:359880000002@s.whatsapp.net"} {
		if m.isMe(id) {
			t.Errorf("isMe(%q) = true", id)
		}
	}
	if got := m.selfIDs(); !slices.Equal(got, []string{"@me:x", ownWhatsApp}) {
		t.Errorf("selfIDs = %v, want the Matrix account first, once", got)
	}

	// A failed ask keeps who we were.
	m, _ = m.handleSelves(selvesMsg{err: errors.New("daemon gone")})
	if !m.isMe(ownWhatsApp) {
		t.Error("a failed ask forgot the WhatsApp account")
	}
}

// Without Matrix, the WhatsApp account alone is this person: its messages are its own,
// and a thread it spoke in is one it took part in.
func TestWithoutMatrixTheWhatsAppAccountIsMe(t *testing.T) {
	t.Parallel()
	m := newModel()
	m, _ = m.handleSelves(selvesMsg{ids: []string{ownWhatsApp}})
	m = m.setMessages([]domain.Message{{ID: "whatsapp:359880000001/A", Sender: ownWhatsApp, Body: "hi"}})
	if !m.isMe(ownWhatsApp) || !m.spokeInThread("whatsapp:359880000001/A") {
		t.Error("the WhatsApp account's own message is not counted as its own")
	}
}

// The room list asks who this person is again only when it brings an account it had
// no rooms from — a phone just linked — not on every refresh, nor the first time
// (the ask at startup covers that).
func TestSelvesAreAskedWhenAnAccountAppears(t *testing.T) {
	t.Parallel()
	asks := 0
	m := starterNew(selvesBackend{Nop: apitest.Nop{}, ids: []string{ownWhatsApp}, asks: &asks}, config.Display{})
	matrixRooms := []domain.Room{{ID: "!a:x"}, {ID: "!b:x"}}
	withWhatsApp := append(slices.Clone(matrixRooms), domain.Room{ID: "whatsapp:359880000001/1203@g.us"})

	steps := []struct {
		rooms []domain.Room
		asks  bool
	}{
		{matrixRooms, false},  // first list: startup's ask covers it
		{matrixRooms, false},  // a refresh: nothing new
		{withWhatsApp, true},  // a WhatsApp account's first rooms
		{withWhatsApp, false}, // and again: known now
	}
	for i, step := range steps {
		var cmd tea.Cmd
		m, cmd = m.selvesAfterRooms(step.rooms)
		if (cmd != nil) != step.asks {
			t.Fatalf("step %d: asks = %t, want %t", i, cmd != nil, step.asks)
		}
		if cmd != nil {
			m, _ = m.handleSelves(cmd().(selvesMsg))
		}
	}
	if asks != 1 || !m.isMe(ownWhatsApp) {
		t.Errorf("asked %d times, isMe(WhatsApp) = %t; want one ask that taught it", asks, m.isMe(ownWhatsApp))
	}
}

// Who this person is is part of the derived cache's key: rows drawn as someone else's
// must be redrawn once the account behind them turns out to be ours.
func TestTheDerivedKeyCoversSelves(t *testing.T) {
	t.Parallel()
	m := newModel()
	before := m.keyFor()
	m, _ = m.handleSelves(selvesMsg{ids: []string{ownWhatsApp}})
	if m.keyFor() == before {
		t.Error("the derived key is the same after the selves changed")
	}
}

// With no room open (a WhatsApp account not linked yet: no rooms at all), the startup
// loads leave the status line's word on what is logged out standing; closing a room
// that was open still takes its status with it.
func TestTheLoggedOutNoticeOutlivesAnEmptyStart(t *testing.T) {
	t.Parallel()
	notice := "WhatsApp bg is logged out: not linked yet; run `kith login whatsapp bg`"
	m := newModel().doing(notice)
	m, _ = m.handleCachedInvites(invitesMsg{})
	m, _ = m.handleRoomsThenOffer(roomsMsg{})
	if got := m.status(); got != notice {
		t.Errorf("status = %q after an empty start, want the notice", got)
	}

	m = withRooms(t, newModel())
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m = m.doing("loading earlier history…")
	m, _ = m.clearRoom()
	if got := m.status(); got != "" {
		t.Errorf("status = %q after closing the open room, want it gone with the room", got)
	}
}

// With no room open (an account with no rooms yet), nothing is asked about a room:
// a call naming none reached the daemon as a Matrix room and was refused, noisily.
func TestNoRoomAsksNothing(t *testing.T) {
	t.Parallel()
	m := newModel()
	if m.loadTimelineCmd("", "") != nil || m.refreshMembersCmd("") != nil {
		t.Error("a load was issued for no room")
	}
}
