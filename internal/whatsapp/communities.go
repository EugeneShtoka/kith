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
	// Not stored (Matrix derives it on read too): every community is its groups' home.
	for i := range spaces {
		spaces[i].Original = true
	}
	return spaces, nil
}

// Spaces is the WhatsApp communities, from the cache.
func (a *Adapter) Spaces(ctx context.Context) ([]domain.Space, error) { return a.whatsAppSpaces(ctx) }

// RefreshSpaces refetches every connected account's groups, which carry its
// communities (one listing writes both), then answers from the cache. A client asks
// for rooms and spaces at once: answering from the cache alone could beat the room
// refresh that writes the communities.
func (a *Adapter) RefreshSpaces(ctx context.Context) ([]domain.Space, error) {
	if _, err := a.RefreshRooms(ctx); err != nil {
		return nil, err
	}
	return a.whatsAppSpaces(ctx)
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
