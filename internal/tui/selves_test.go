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

type selvesBackend struct {
	apitest.Nop
	ids  []string
	asks *int
}

func (b selvesBackend) Selves(context.Context) ([]string, error) {
	*b.asks++
	return b.ids, nil
}

const ownWhatsApp = "whatsapp:44880000001@s.whatsapp.net"

func TestIsMeIsEveryAccount(t *testing.T) {
	t.Parallel()
	m := newModel()
	m, _ = m.handleSelves(selvesMsg{ids: []string{"matrix:@me:x", ownWhatsApp}})
	for _, id := range []string{"matrix:@me:x", ownWhatsApp} {
		if !m.isMe(id) || !m.fromMe(id) {
			t.Errorf("isMe(%q) = false", id)
		}
	}
	for _, id := range []string{"", "matrix:@dana:x", "whatsapp:44880000002@s.whatsapp.net"} {
		if m.isMe(id) {
			t.Errorf("isMe(%q) = true", id)
		}
	}
	if got := m.selfIDs(); !slices.Equal(got, []string{"matrix:@me:x", ownWhatsApp}) {
		t.Errorf("selfIDs = %v, want the Matrix account first, once", got)
	}

	// A failed ask keeps the last IDs.
	m, _ = m.handleSelves(selvesMsg{err: errors.New("daemon gone")})
	if !m.isMe(ownWhatsApp) {
		t.Error("a failed ask forgot the WhatsApp account")
	}
}

// Without Matrix, the WhatsApp account alone is this person.
func TestWithoutMatrixTheWhatsAppAccountIsMe(t *testing.T) {
	t.Parallel()
	m := newModel()
	m, _ = m.handleSelves(selvesMsg{ids: []string{ownWhatsApp}})
	m = m.setMessages([]domain.Message{{ID: "whatsapp:44880000001/A", Sender: ownWhatsApp, Body: "hi"}})
	if !m.isMe(ownWhatsApp) || !m.spokeInThread("whatsapp:44880000001/A") {
		t.Error("the WhatsApp account's own message is not counted as its own")
	}
}

// Asked again only for an account with no rooms before; not on every refresh, nor on
// the first list (the startup ask covers it).
func TestSelvesAreAskedWhenAnAccountAppears(t *testing.T) {
	t.Parallel()
	asks := 0
	m := starterNew(selvesBackend{Nop: apitest.Nop{}, ids: []string{ownWhatsApp}, asks: &asks}, config.Display{})
	matrixRooms := []domain.Room{{ID: mx + "!a:x"}, {ID: mx + "!b:x"}}
	withWhatsApp := append(slices.Clone(matrixRooms), domain.Room{ID: "whatsapp:44880000001/1203@g.us"})

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
		if selves, ok := drainFor[selvesMsg](cmd); ok {
			m, _ = m.handleSelves(selves)
		}
	}
	if asks != 1 || !m.isMe(ownWhatsApp) {
		t.Errorf("asked %d times, isMe(WhatsApp) = %t; want one ask that taught it", asks, m.isMe(ownWhatsApp))
	}
}

// Rows drawn as someone else's must redraw once their account turns out to be ours.
func TestTheDerivedKeyCoversSelves(t *testing.T) {
	t.Parallel()
	m := newModel()
	before := m.keyFor()
	m, _ = m.handleSelves(selvesMsg{ids: []string{ownWhatsApp}})
	if m.keyFor() == before {
		t.Error("the derived key is the same after the selves changed")
	}
}

// With no rooms at all, the logged-out notice survives the startup loads; closing a
// room still clears its status.
func TestTheLoggedOutNoticeOutlivesAnEmptyStart(t *testing.T) {
	t.Parallel()
	notice := "WhatsApp home is logged out: not linked yet; run `kith login whatsapp home`"
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

// With no room open nothing is asked about a room: a call naming none reached the
// daemon as a Matrix room and was refused.
func TestNoRoomAsksNothing(t *testing.T) {
	t.Parallel()
	m := newModel()
	if m.loadTimelineCmd("", "") != nil || m.refreshMembersCmd("") != nil {
		t.Error("a load was issued for no room")
	}
}

// $KITH_USER is you on the room's network, or nobody where you have no account.
func TestSelfInIsYouOnTheRoomsNetwork(t *testing.T) {
	t.Parallel()
	m := newModel()
	m.selves = []string{"", "matrix:@me:x", "matrix:@whatsapp_44880000001:x", ownWhatsApp, "whatsapp:44880000002@s.whatsapp.net"}
	for room, want := range map[domain.RoomID]string{
		mx + "!a:x": "matrix:@me:x",
		domain.RoomID(domain.NativeID(domain.ProtocolWhatsApp, "44880000002", "120363000000000001@g.us")): ownWhatsApp,
		domain.RoomID(domain.NativeID(domain.ProtocolTelegram, "1000001", "-1000002")):                    "",
	} {
		if got := m.selfIn(room); got != want {
			t.Errorf("selfIn(%s) = %q, want %q", room, got, want)
		}
	}
}

// Anyone [[display.identity]] groups with any of your IDs is you; nobody is you
// through someone else's identity.
func TestAnIdentityWithAnyOfYourIDsIsYou(t *testing.T) {
	t.Parallel()
	m := newModel()
	m.selves = []string{ownWhatsApp}
	m.prefs.identities = buildIdentities([]config.Identity{
		{Alias: "me", IDs: []string{ownWhatsApp, "matrix:@whatsapp_44880000001:x"}},
		{Alias: "dana", IDs: []string{"matrix:@dana:x", "whatsapp:44880000009@s.whatsapp.net"}},
	})
	if !m.fromMe("matrix:@whatsapp_44880000001:x") {
		t.Error("your bridged puppet, grouped with your WhatsApp number, is not you")
	}
	if m.fromMe("whatsapp:44880000009@s.whatsapp.net") || m.fromMe("matrix:@dana:x") {
		t.Error("someone else's identity is you")
	}
}
