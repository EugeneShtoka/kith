package whatsapp

import (
	"context"
	"fmt"
	"slices"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A WhatsApp community is a space: its groups (the announcement group among them)
// are the space's rooms. The community itself is a group WhatsApp lists, which
// nobody chats in, so it is the space and not a room. What is in it is the
// community's admins' to decide, so kith files nothing into it.

// communities splits an account's joined groups into its communities, as spaces, and
// the groups people chat in. A community the account is in a group of, but which the
// listing does not carry, is named by communityName (a round trip; "" leaves it
// unnamed).
func communities(ctx context.Context, account string, groups []*types.GroupInfo,
	communityName func(context.Context, types.JID) string,
) (spaces []domain.Space, chats []*types.GroupInfo) {
	byJID := map[types.JID]*domain.Space{}
	space := func(jid types.JID) *domain.Space {
		jid = jid.ToNonAD()
		if s := byJID[jid]; s != nil {
			return s
		}
		// Original: its groups call it home (it is their canonical parent), so it is
		// not a space to file rooms into.
		s := &domain.Space{ID: domain.SpaceID(roomID(account, jid)), Bridge: domain.ProtocolWhatsApp, Original: true}
		byJID[jid] = s
		return s
	}
	for _, g := range groups {
		if g.IsParent {
			space(g.JID).Name = g.Name
			continue
		}
		chats = append(chats, g)
		if parent := g.LinkedParentJID; !parent.IsEmpty() {
			s := space(parent)
			s.Children = append(s.Children, roomID(account, g.JID))
		}
	}
	for jid, s := range byJID {
		if s.Name == "" {
			s.Name = communityName(ctx, jid)
		}
		slices.Sort(s.Children)
		spaces = append(spaces, *s)
	}
	domain.SortSpaces(spaces)
	return spaces, chats
}

// whatsAppSpaces is every WhatsApp account's cached communities.
func (a *Adapter) whatsAppSpaces(ctx context.Context) ([]domain.Space, error) {
	if a.cache == nil {
		return nil, nil
	}
	spaces, err := a.cache.Spaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read cached communities: %w", err)
	}
	spaces = slices.DeleteFunc(spaces, func(s domain.Space) bool {
		return domain.NetworkOf(string(s.ID)) != domain.ProtocolWhatsApp
	})
	// Not stored (Matrix derives it on read too): every community is its groups' home,
	// and is left with them (LeaveRoom).
	for i := range spaces {
		spaces[i].Original = true
		spaces[i].Leaving = domain.LeftWithRooms
	}
	return spaces, nil
}

// Spaces is the WhatsApp communities, from the cache, then a space per account holding
// all its rooms, as a Slack workspace holds its.
func (a *Adapter) Spaces(ctx context.Context) ([]domain.Space, error) {
	spaces, err := a.whatsAppSpaces(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := a.accountSpaces(ctx)
	if err != nil {
		return nil, err
	}
	return append(spaces, accounts...), nil
}

// accountSpaceID is the space an account is: its own number within its rooms' prefix.
func accountSpaceID(digits string) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolWhatsApp, digits, digits))
}

// accountSpaces is a space per configured account with cached rooms, named after the
// account, its children every room it sees. Derived on read, not stored: a chat
// begun or a group joined is in it at once.
func (a *Adapter) accountSpaces(ctx context.Context) ([]domain.Space, error) {
	if a.cache == nil {
		return nil, nil
	}
	rooms, err := a.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	var spaces []domain.Space
	for _, account := range a.accountsNow() {
		owner := domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits)
		var children []domain.RoomID
		for i := range rooms {
			if owner.Owns(rooms[i].ID) {
				children = append(children, rooms[i].ID)
			}
		}
		if len(children) == 0 {
			continue
		}
		spaces = append(spaces, domain.Space{
			ID: accountSpaceID(account.Digits), Name: "WhatsApp " + account.Name, Children: children,
			// The home of every room no community claims.
			Bridge: domain.ProtocolWhatsApp, Original: true,
		})
	}
	return spaces, nil
}

// RefreshSpaces refetches every connected account's groups, which carry its
// communities (one listing writes both), then answers from the cache. A client asks
// for rooms and spaces at once: answering from the cache alone could beat the room
// refresh that writes the communities.
func (a *Adapter) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	if _, err := a.RefreshRooms(ctx); err != nil {
		return nil, err
	}
	return a.Spaces(ctx)
}

// communityOf is the community a WhatsApp room is in, "" when none.
func (a *Adapter) communityOf(ctx context.Context, roomID domain.RoomID) (domain.SpaceID, error) {
	spaces, err := a.whatsAppSpaces(ctx)
	if err != nil {
		return "", err
	}
	for i := range spaces {
		if slices.Contains(spaces[i].Children, roomID) {
			return spaces[i].ID, nil
		}
	}
	return "", nil
}
