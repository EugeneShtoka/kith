package whatsapp

import (
	"context"
	"fmt"
	"slices"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A WhatsApp community is a space whose rooms are its groups (including the
// announcement group). The community group itself is not a room. Its admins decide
// its contents, so kith files nothing into it.

// communities splits joined groups into communities (as spaces) and chat groups. A
// community missing from the listing is named by communityName ("" leaves it unnamed).
func communities(ctx context.Context, account string, groups []*types.GroupInfo,
	communityName func(context.Context, types.JID) string,
) (spaces []domain.Space, chats []*types.GroupInfo) {
	byJID := map[types.JID]*domain.Space{}
	space := func(jid types.JID) *domain.Space {
		jid = jid.ToNonAD()
		if s := byJID[jid]; s != nil {
			return s
		}
		// Original: it is its groups' canonical parent, not a space to file rooms into.
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

// whatsAppSpaces returns every account's cached communities.
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
	// Derived on read: every community is its groups' home and is left with them.
	for i := range spaces {
		spaces[i].Original = true
		spaces[i].Leaving = domain.LeftWithRooms
	}
	return spaces, nil
}

// Spaces returns the cached communities, then a space per account holding all its rooms.
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

// accountSpaceID is an account's space: its number within its rooms' prefix.
func accountSpaceID(digits string) domain.SpaceID {
	return domain.SpaceID(domain.NativeID(domain.ProtocolWhatsApp, digits, digits))
}

// accountSpaces is a space per account with cached rooms, holding all of them.
// Derived on read so a new chat or group appears in it at once.
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
			// Home of every room no community claims; left by signing out.
			Bridge: domain.ProtocolWhatsApp, Original: true, Leaving: domain.LeftBySigningOut,
		})
	}
	return spaces, nil
}

// RefreshSpaces refetches groups (which carry communities), then reads the cache.
// Reading the cache alone could race the concurrent room refresh that writes them.
func (a *Adapter) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	if _, err := a.RefreshRooms(ctx); err != nil {
		return nil, err
	}
	return a.Spaces(ctx)
}

// communityOf is a room's community, "" when none.
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
