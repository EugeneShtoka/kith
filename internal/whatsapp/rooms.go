package whatsapp

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// heroes bounds how many member names label an unnamed group.
const heroes = 5

// nameOf is a person's name as the account knows it, "" when unknown.
type nameOf func(ctx context.Context, person types.JID) string

func roomID(account string, chat types.JID) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolWhatsApp, account, chat.ToNonAD().String()))
}

// personID is a participant's account-independent ID: by phone number when known,
// so they fold across bridges and accounts; else by LID.
func personID(p types.GroupParticipant) (string, types.JID) {
	jid := p.JID
	if !p.PhoneNumber.IsEmpty() {
		jid = p.PhoneNumber
	}
	jid = jid.ToNonAD()
	return domain.NativePerson(domain.ProtocolWhatsApp, jid.String()), jid
}

// groupRooms converts groups into rooms and members. The account itself is a member
// but never a hero.
func groupRooms(ctx context.Context, account string, groups []*types.GroupInfo, names nameOf) ([]domain.Room, map[domain.RoomID][]domain.Member) {
	rooms := make([]domain.Room, 0, len(groups))
	members := make(map[domain.RoomID][]domain.Member, len(groups))
	for _, g := range groups {
		id := roomID(account, g.JID)
		room := domain.Room{ID: id, Name: g.Name, Topic: g.Topic}
		list := make([]domain.Member, 0, len(g.Participants))
		for i := range g.Participants {
			p := &g.Participants[i]
			userID, jid := personID(*p)
			name := names(ctx, jid)
			if name == "" && !p.LID.IsEmpty() && p.LID != jid {
				name = names(ctx, p.LID.ToNonAD())
			}
			if name == "" {
				name = p.DisplayName
			}
			list = append(list, domain.Member{UserID: userID, DisplayName: name})
			if name != "" && jid.User != account && len(room.Members) < heroes {
				room.Members = append(room.Members, name)
			}
		}
		domain.SortMembers(list)
		rooms = append(rooms, room)
		members[id] = list
	}
	domain.SortRooms(rooms)
	return rooms, members
}

// contactName is a contact's address book name, else push name, else business name.
func contactName(c types.ContactInfo) string {
	for _, name := range []string{c.FullName, c.FirstName, c.PushName, c.BusinessName} {
		if name != "" {
			return name
		}
	}
	return ""
}

func errNotYet(what string) error {
	return fmt.Errorf("%w: WhatsApp cannot %s yet", errNotOnWhatsApp, what)
}
