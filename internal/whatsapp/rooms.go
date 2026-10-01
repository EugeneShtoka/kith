package whatsapp

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// heroes bounds how many member names a group without a name is shown by.
const heroes = 5

// nameOf is a person's name as this account knows them, "" when it knows none.
type nameOf func(ctx context.Context, person types.JID) string

// roomID is a chat as an account sees it.
func roomID(account string, chat types.JID) domain.RoomID {
	return domain.RoomID(domain.NativeID(domain.ProtocolWhatsApp, account, chat.ToNonAD().String()))
}

// personID is a person, whichever account sees them. A participant known by phone
// number is named by it, so the same contact folds with one seen through a bridge or
// another account; one WhatsApp shows only by LID is named by that.
func personID(p types.GroupParticipant) (string, types.JID) {
	jid := p.JID
	if !p.PhoneNumber.IsEmpty() {
		jid = p.PhoneNumber
	}
	jid = jid.ToNonAD()
	return domain.NativePerson(domain.ProtocolWhatsApp, jid.String()), jid
}

// groupRooms is an account's groups as rooms, with each one's members. Our own
// account is a member but not a hero: a room is never named after yourself.
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

// contactName is the name a contact goes by: the one saved in the phone's address
// book, then the one they set themselves, then their business name.
func contactName(c types.ContactInfo) string {
	for _, name := range []string{c.FullName, c.FirstName, c.PushName, c.BusinessName} {
		if name != "" {
			return name
		}
	}
	return ""
}

// errNotYet refuses what the adapter does not do yet.
func errNotYet(what string) error {
	return fmt.Errorf("%w: WhatsApp cannot %s yet", errNotOnWhatsApp, what)
}
