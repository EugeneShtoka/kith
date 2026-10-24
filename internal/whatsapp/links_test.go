package whatsapp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A number found on WhatsApp is its phone-number chat, as WhatsApp normalized it; one
// not found is nobody's.
func TestARegisteredNumberIsItsPhoneChat(t *testing.T) {
	t.Parallel()
	lid := types.NewJID("100000000000042", types.HiddenUserServer)
	for name, c := range map[string]struct {
		found []types.IsOnWhatsAppResponse
		want  types.JID
		ok    bool
	}{
		"not on WhatsApp":      {[]types.IsOnWhatsAppResponse{{JID: pn(danaPhone)}}, types.JID{}, false},
		"by its number":        {[]types.IsOnWhatsAppResponse{{JID: pn("15000000020"), IsIn: true}}, pn("15000000020"), true},
		"a LID, with a number": {[]types.IsOnWhatsAppResponse{{JID: lid, PhoneNumber: pn("15000000020"), IsIn: true}}, pn("15000000020"), true},
		"a LID alone":          {[]types.IsOnWhatsAppResponse{{JID: lid, IsIn: true}}, pn(danaPhone), true},
		"no answer":            {nil, types.JID{}, false},
	} {
		got, ok := registered(danaPhone, c.found)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: registered = (%v, %v), want (%v, %v)", name, got, ok, c.want, c.ok)
		}
	}
}

// An invite to a group the account is in opens it; to another, it says what joining
// joins, and a join that only sent a request says so.
func TestAnInviteSaysWhetherToJoin(t *testing.T) {
	t.Parallel()
	own := self{pn: pn(ownDigits), lid: types.NewJID("100000000000009", types.HiddenUserServer)}
	group := types.NewJID("1203", types.GroupServer)
	info := func(change func(*types.GroupInfo)) *types.GroupInfo {
		i := &types.GroupInfo{JID: group, GroupName: types.GroupName{Name: "Hikers"}, ParticipantCount: 12,
			Participants: []types.GroupParticipant{{JID: types.NewJID("100000000000002", types.HiddenUserServer)}}}
		change(i)
		return i
	}
	room := roomID(ownDigits, group)

	in := inviteTarget(ownDigits, own, info(func(i *types.GroupInfo) {
		i.Participants = append(i.Participants, types.GroupParticipant{JID: own.lid})
	}))
	if in.Join || in.Room.ID != room || in.Room.Name != "Hikers" {
		t.Errorf("a group the account is in = %+v, want it opened", in)
	}
	out := inviteTarget(ownDigits, own, info(func(*types.GroupInfo) {}))
	if !out.Join || out.Room.ID != room || out.About != "group, 12 members" {
		t.Errorf("a group to join = %+v, want it asked about", out)
	}
	if c := inviteTarget(ownDigits, own, info(func(i *types.GroupInfo) { i.IsParent = true })); !strings.HasPrefix(c.About, "community") {
		t.Errorf("a community's about = %q", c.About)
	}

	approved := info(func(i *types.GroupInfo) { i.IsJoinApprovalRequired = true })
	if a := inviteTarget(ownDigits, own, approved); !strings.Contains(a.About, "approves") {
		t.Errorf("about = %q, want it to say an admin approves", a.About)
	}
	if _, err := joined(ownDigits, own, approved, group); !errors.Is(err, errJoinRequested) {
		t.Errorf("a join an admin approves = %v, want the request reported", err)
	}
	if got, err := joined(ownDigits, own, info(func(*types.GroupInfo) {}), types.EmptyJID); err != nil || got != room {
		t.Errorf("a join naming no group = (%q, %v), want the invite's group", got, err)
	}
}

// The first message to somebody records the chat it started, named after them, so a
// chat opened from a link joins the room list; later ones change nothing.
func TestAMessageThatStartsAChatRecordsIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, _, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	changed := 0
	a.OnRoomsChanged(func() { changed++ })

	chat := pn(danaPhone)
	room := roomID(ownDigits, chat)
	send := func(id string, to domain.RoomID, peer types.JID) {
		a.cacheSent(ctx, account, client, peer, domain.Message{
			ID: domain.EventID("whatsapp:" + ownDigits + "/" + id), RoomID: to,
			Sender: "whatsapp:" + ownDigits + "@s.whatsapp.net", Body: "hi", Timestamp: time.Now(),
		})
	}
	send("A1", room, chat)
	rooms, err := a.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rooms, func(r domain.Room) bool { return r.ID == room })
	if i < 0 || !rooms[i].IsDirect || rooms[i].Name != "+"+danaPhone {
		t.Fatalf("rooms = %+v, want the started chat with Dana by her number", rooms)
	}
	if changed != 1 {
		t.Errorf("rooms changed %d times, want once for the started chat", changed)
	}
	send("A2", room, chat)
	send("A3", waRoom, types.NewJID("1203", types.GroupServer))
	if changed != 1 {
		t.Errorf("rooms changed %d times, want no more: the chat was known, and a group is not started", changed)
	}
}

// With nothing connected, or for another network's link, nothing is looked up.
func TestALinkNeedsAConnectedAccount(t *testing.T) {
	t.Parallel()
	a, _, _ := offline(t, Account{Name: "home", Digits: ownDigits})
	if _, err := a.ResolveLink(context.Background(), "https://wa.me/"+danaPhone, waOn); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("ResolveLink with nothing connected = %v, want the network off", err)
	}
	if _, err := a.JoinLink(context.Background(), "https://wa.me/"+danaPhone, waOn); err == nil {
		t.Error("joined a person's link")
	}
	if _, err := a.ResolveLink(context.Background(), "tg://resolve?domain=novosalsa", waOn); err == nil {
		t.Error("looked up a Telegram link")
	}
}

// waOn is the test account, which links are looked up on.
var waOn = domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits)
