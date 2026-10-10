package tui

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// peerBackend answers with each room's members and who this person is.
type peerBackend struct {
	apitest.Nop
	members map[domain.RoomID][]domain.Member
	selves  []string
}

func (b peerBackend) Members(_ context.Context, room domain.RoomID, _ int) ([]domain.Member, error) {
	return b.members[room], nil
}

func (b peerBackend) Selves(context.Context) ([]string, error) { return b.selves, nil }

const (
	peerMe     = "@me:x"
	peerPuppet = "@linkedin_me:x"
	peerBot    = "@linkedinbot:x"
	peerDana   = "@linkedin_dana:x"
	peerEli    = "@linkedin_eli:x"
	peerLong   = "Dana Levi ✨Hiring✨"
)

// peerRooms is a bridged private chat named after Dana, a group of two others, and a
// private chat whose own name is not its member's.
func peerRooms(danaRoomName string) []domain.Room {
	return []domain.Room{
		{ID: "!dm:x", Name: danaRoomName},
		{ID: "!group:x", Name: peerLong},
		{ID: "!other:x", Name: "Eli's chat"},
	}
}

func peerMembers(danaName string) map[domain.RoomID][]domain.Member {
	return map[domain.RoomID][]domain.Member{
		"!dm:x":    {{UserID: peerMe, DisplayName: "Me"}, {UserID: peerPuppet, DisplayName: "Me"}, {UserID: peerBot, DisplayName: "Bridge Bot"}, {UserID: peerDana, DisplayName: danaName}},
		"!group:x": {{UserID: peerMe}, {UserID: peerDana, DisplayName: peerLong}, {UserID: peerEli, DisplayName: "Eli"}},
		"!other:x": {{UserID: peerMe}, {UserID: peerBot}, {UserID: peerEli, DisplayName: "Eli"}},
	}
}

func peerDisplay(names ...config.DisplayName) config.Display {
	return config.Display{Names: names}
}

var allNamed = []config.DisplayName{
	{Target: "!dm:x", Name: "Dana"},
	{Target: "!group:x", Name: "Team"},
	{Target: "!other:x", Name: "Eli L"},
}

// peerModel is a model that has the room list, the members it asked for and, when
// selves is set, who this person is.
func peerModel(t *testing.T, b peerBackend, display config.Display, rooms []domain.Room, selves bool) Model {
	t.Helper()
	m := New(context.Background(), b, display)
	m, cmd := asModel(m.Update(roomsMsg{rooms: rooms}))
	if read, ok := msgOf[peerMembersMsg](t, cmd); ok {
		m = update(t, m, read)
	}
	if selves {
		m = update(t, m, selvesMsg{ids: b.selves})
	}
	return m
}

func danaShown(m Model, room domain.RoomID) string {
	return m.processedName(domain.Message{RoomID: room, Sender: peerDana, SenderName: peerLong})
}

// A private chat you named after the one person in it names that person, in every
// room; a group, a chat named for someone else, and you yourself are left alone.
func TestARoomNamedAfterItsPeerNamesThePeer(t *testing.T) {
	t.Parallel()
	b := peerBackend{members: peerMembers(peerLong), selves: []string{peerMe, peerPuppet}}
	m := peerModel(t, b, peerDisplay(allNamed...), peerRooms(peerLong), true)

	for _, room := range []domain.RoomID{"!dm:x", "!group:x"} {
		if got := danaShown(m, room); got != "Dana" {
			t.Errorf("Dana in %s = %q, want Dana", room, got)
		}
	}
	if got := m.processedName(domain.Message{RoomID: "!other:x", Sender: peerEli, SenderName: "Eli"}); got != "Eli" {
		t.Errorf("Eli, in a chat not named after him = %q, want Eli", got)
	}
	if got := m.processedName(domain.Message{RoomID: "!dm:x", Sender: peerPuppet, SenderName: "Me"}); got != "Me" {
		t.Errorf("your own puppet = %q, want Me", got)
	}
	if got := m.aliasOf(peerDana); got != "Dana" {
		t.Errorf("Dana's alias for mentions = %q, want Dana", got)
	}
}

// The peer follows every input it is derived from: who this person is arriving after
// the members, the room's name being cleared, the bridge renaming the room with the
// person, and an identity naming them.
func TestARoomNamedPeerFollowsItsInputs(t *testing.T) {
	t.Parallel()
	b := peerBackend{members: peerMembers(peerLong), selves: []string{peerMe, peerPuppet}}

	t.Run("selves after members", func(t *testing.T) {
		t.Parallel()
		m := peerModel(t, b, peerDisplay(allNamed...), peerRooms(peerLong), false)
		if got := danaShown(m, "!dm:x"); got != peerLong {
			t.Fatalf("before selves, your puppet makes two others: %q, want %q", got, peerLong)
		}
		m = update(t, m, selvesMsg{ids: b.selves})
		if got := danaShown(m, "!dm:x"); got != "Dana" {
			t.Errorf("after selves = %q, want Dana", got)
		}
	})

	t.Run("room name cleared", func(t *testing.T) {
		t.Parallel()
		m := peerModel(t, b, peerDisplay(allNamed...), peerRooms(peerLong), true)
		m, _ = m.applyDisplay(peerDisplay(allNamed[1:]...), "")
		if got := danaShown(m, "!group:x"); got != peerLong {
			t.Errorf("after the room's name was cleared = %q, want %q", got, peerLong)
		}
	})

	t.Run("room named later", func(t *testing.T) {
		t.Parallel()
		m := peerModel(t, b, peerDisplay(), peerRooms(peerLong), true)
		m, cmd := m.applyDisplay(peerDisplay(allNamed[0]), "")
		read, ok := msgOf[peerMembersMsg](t, cmd)
		if !ok {
			t.Fatal("naming a room read no members")
		}
		// Only the members change here, so the timeline's rows must see them.
		before := m.keyFor()
		m = update(t, m, read)
		if m.keyFor() == before {
			t.Error("the timeline's derived key did not move with the peer's name")
		}
		if got := danaShown(m, "!group:x"); got != "Dana" {
			t.Errorf("after naming the room = %q, want Dana", got)
		}
	})

	t.Run("bridge renames", func(t *testing.T) {
		t.Parallel()
		m := peerModel(t, b, peerDisplay(allNamed...), peerRooms(peerLong), true)
		// The bridge renames both; the room list arrives before the members do.
		renamed := peerBackend{members: peerMembers("Dana Levi"), selves: b.selves}
		m.backend = renamed
		m, cmd := asModel(m.Update(roomsMsg{rooms: peerRooms("Dana Levi")}))
		read, ok := msgOf[peerMembersMsg](t, cmd)
		if !ok {
			t.Fatal("a renamed room read no members")
		}
		m = update(t, m, read)
		// An answer for the old names, landing late, is not kept.
		m = update(t, m, peerMembersMsg{key: peerKey(peerRooms(peerLong)), members: peerMembers(peerLong)})
		if got := m.processedName(domain.Message{RoomID: "!dm:x", Sender: peerDana, SenderName: "Dana Levi"}); got != "Dana" {
			t.Errorf("after the bridge renamed them = %q, want Dana", got)
		}
	})

	t.Run("identity first", func(t *testing.T) {
		t.Parallel()
		display := peerDisplay(allNamed...)
		display.Identities = []config.Identity{{Alias: "Dana L", IDs: []string{peerDana}}}
		m := peerModel(t, b, display, peerRooms(peerLong), true)
		if got := danaShown(m, "!dm:x"); got != "Dana L" {
			t.Errorf("with an identity = %q, want Dana L", got)
		}
	})
}
