package whatsapp

import (
	"context"
	"slices"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Invented numbers in WhatsApp's shapes.
const (
	ownDigits = "44000000001"
	danaPhone = "1500000002"
	danaLID   = "100000000000003"
	samLID    = "100000000000004"
)

func pn(digits string) types.JID  { return types.NewJID(digits, types.DefaultUserServer) }
func lid(digits string) types.JID { return types.NewJID(digits, types.HiddenUserServer) }

// A group is a room named by the account that sees it, with each participant a
// member named by phone number when known (so contacts fold across accounts and
// bridges) and by LID otherwise; the account itself is a member, never a hero.
func TestAGroupIsARoomOfItsParticipants(t *testing.T) {
	t.Parallel()
	group := &types.GroupInfo{
		JID:        types.NewJID("120363000000000001", types.GroupServer),
		GroupName:  types.GroupName{Name: "Team"},
		GroupTopic: types.GroupTopic{Topic: "weekly"},
		Participants: []types.GroupParticipant{
			{JID: lid(danaLID), LID: lid(danaLID), PhoneNumber: pn(danaPhone)},
			{JID: lid(samLID), LID: lid(samLID)}, // phone number hidden
			{JID: pn(ownDigits), PhoneNumber: pn(ownDigits)},
		},
	}
	names := map[types.JID]string{pn(danaPhone): "Dana", lid(samLID): "Sam", pn(ownDigits): "Me"}
	rooms, members := groupRooms(context.Background(), ownDigits, []*types.GroupInfo{group},
		func(_ context.Context, person types.JID) string { return names[person] })

	if len(rooms) != 1 {
		t.Fatalf("rooms = %+v", rooms)
	}
	room := rooms[0]
	if want := domain.RoomID("whatsapp:" + ownDigits + "/120363000000000001@g.us"); room.ID != want {
		t.Errorf("room ID = %s, want %s", room.ID, want)
	}
	if !domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits).Owns(room.ID) {
		t.Error("the room is not the account's own")
	}
	if room.Name != "Team" || room.Topic != "weekly" {
		t.Errorf("name/topic = %q/%q", room.Name, room.Topic)
	}
	if slices.Contains(room.Members, "Me") || !slices.Contains(room.Members, "Dana") || !slices.Contains(room.Members, "Sam") {
		t.Errorf("heroes = %v, want the others and never yourself", room.Members)
	}
	ids := map[string]string{}
	for _, m := range members[room.ID] {
		ids[m.UserID] = m.DisplayName
	}
	for id, name := range map[string]string{
		"whatsapp:" + danaPhone + "@s.whatsapp.net": "Dana",
		"whatsapp:" + samLID + "@lid":               "Sam",
		"whatsapp:" + ownDigits + "@s.whatsapp.net": "Me",
	} {
		if ids[id] != name {
			t.Errorf("member %s = %q, want %q (members: %v)", id, ids[id], name, ids)
		}
	}
	for id := range ids {
		if !domain.IsUserID(id) {
			t.Errorf("%s is not a person's ID", id)
		}
	}
}

// A participant listed by phone number but saved in contacts only under their LID is
// still named.
func TestANameKnownOnlyByLIDIsFound(t *testing.T) {
	t.Parallel()
	group := &types.GroupInfo{JID: types.NewJID("1203", types.GroupServer), Participants: []types.GroupParticipant{
		{JID: pn(danaPhone), PhoneNumber: pn(danaPhone), LID: lid(danaLID)},
	}}
	_, members := groupRooms(context.Background(), ownDigits, []*types.GroupInfo{group},
		func(_ context.Context, person types.JID) string {
			if person == lid(danaLID) {
				return "Dana"
			}
			return ""
		})
	for _, list := range members {
		if len(list) != 1 || list[0].DisplayName != "Dana" {
			t.Errorf("members = %+v, want Dana found by LID", list)
		}
	}
}

// A contact's name: the phone's address book first, then what they call themselves.
func TestAContactGoesByTheirSavedName(t *testing.T) {
	t.Parallel()
	for want, c := range map[string]types.ContactInfo{
		"Dana Levi": {FullName: "Dana Levi", FirstName: "Dana", PushName: "dl"},
		"Dana":      {FirstName: "Dana", PushName: "dl"},
		"dl":        {PushName: "dl", BusinessName: "Shop"},
		"Shop":      {BusinessName: "Shop"},
		"":          {},
	} {
		if got := contactName(c); got != want {
			t.Errorf("contactName(%+v) = %q, want %q", c, got, want)
		}
	}
}
