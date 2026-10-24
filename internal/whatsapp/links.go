package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Links to chats no listing holds are looked up on the first linked account, in the
// config's order. A number is a direct chat to write in: WhatsApp has no chat before
// the first message, so it is opened before it exists and recorded once a message of
// ours starts it (startedChat). A group's invite is joined only when asked.

// errJoinRequested: the group's admins approve who joins, so joining sent a request.
var errJoinRequested = errors.New("whatsapp: asked to join; an admin of the group has to approve it")

// linkClient is the account links are looked up on: the first connected, in the
// config's order, so a lookup and the join after it use the same one.
func (a *Adapter) linkClient() (Account, *whatsmeow.Client, error) {
	conns := a.connected()
	if len(conns) == 0 {
		return Account{}, nil, fmt.Errorf("whatsapp: no account to look a link up on: %w", api.ErrNetworkOff)
	}
	return conns[0].account, conns[0].client, nil
}

// ResolveLink looks up the chat a WhatsApp link names: a person to write to, or a
// group, joined or to be joined.
func (a *Adapter) ResolveLink(ctx context.Context, uri string) (domain.LinkTarget, error) {
	link, ok := domain.ParseWhatsAppLink(uri)
	if !ok {
		return domain.LinkTarget{}, fmt.Errorf("whatsapp: not a WhatsApp link: %s", uri)
	}
	account, client, err := a.linkClient()
	if err != nil {
		return domain.LinkTarget{}, err
	}
	if link.Kind == domain.WhatsAppLinkInvite {
		info, lookupErr := client.GetGroupInfoFromLink(ctx, link.Invite)
		if lookupErr != nil {
			return domain.LinkTarget{}, fmt.Errorf("whatsapp: look up the invite: %w", lookupErr)
		}
		return inviteTarget(account.Digits, selfOf(client), info), nil
	}
	found, err := client.IsOnWhatsApp(ctx, []string{"+" + link.Phone})
	if err != nil {
		return domain.LinkTarget{}, fmt.Errorf("whatsapp: look up +%s: %w", link.Phone, err)
	}
	jid, ok := registered(link.Phone, found)
	if !ok {
		return domain.LinkTarget{}, fmt.Errorf("whatsapp: +%s is not on WhatsApp", link.Phone)
	}
	room := roomID(account.Digits, jid)
	name := chatName(ctx, a.names(client), selfOf(client), jid, "")
	return domain.LinkTarget{Room: domain.Room{ID: room, Name: name, IsDirect: true}}, nil
}

// JoinLink joins the group a WhatsApp invite names, and returns its room.
func (a *Adapter) JoinLink(ctx context.Context, uri string) (domain.RoomID, error) {
	link, ok := domain.ParseWhatsAppLink(uri)
	if !ok || link.Kind != domain.WhatsAppLinkInvite {
		return "", fmt.Errorf("whatsapp: not a group invite: %s", uri)
	}
	account, client, err := a.linkClient()
	if err != nil {
		return "", err
	}
	// Asked first: a join that only sent a request answers like one that joined.
	info, err := client.GetGroupInfoFromLink(ctx, link.Invite)
	if err != nil {
		return "", fmt.Errorf("whatsapp: look up the invite: %w", err)
	}
	jid, err := client.JoinGroupWithLink(ctx, link.Invite)
	if err != nil {
		return "", fmt.Errorf("whatsapp: join by the invite: %w", err)
	}
	return joined(account.Digits, selfOf(client), info, jid)
}

// registered is the chat of the number a lookup found on WhatsApp: its phone-number
// JID, as direct chats are kept (messages.go), normalized as WhatsApp answered.
func registered(digits string, found []types.IsOnWhatsAppResponse) (types.JID, bool) {
	for _, r := range found {
		if !r.IsIn {
			continue
		}
		switch {
		case r.JID.Server == types.DefaultUserServer:
			return r.JID.ToNonAD(), true
		case r.PhoneNumber.Server == types.DefaultUserServer:
			return r.PhoneNumber.ToNonAD(), true
		}
		return types.NewJID(digits, types.DefaultUserServer), true
	}
	return types.JID{}, false
}

// inviteTarget is the group an invite is to: one the account is in, or one to join.
func inviteTarget(account string, own self, info *types.GroupInfo) domain.LinkTarget {
	room := domain.Room{ID: roomID(account, info.JID), Name: info.Name}
	if slices.ContainsFunc(info.Participants, func(p types.GroupParticipant) bool {
		return own.is(p.JID) || own.is(p.PhoneNumber)
	}) {
		return domain.LinkTarget{Room: room}
	}
	about := "group"
	if info.IsParent {
		about = "community"
	}
	if n := max(info.ParticipantCount, len(info.Participants)); n > 0 {
		about = fmt.Sprintf("%s, %d members", about, n)
	}
	if info.IsJoinApprovalRequired {
		about += "; an admin approves who joins"
	}
	return domain.LinkTarget{Room: room, Join: true, About: about}
}

// joined is the room a join by invite reached, or that it only sent a request.
func joined(account string, own self, info *types.GroupInfo, jid types.JID) (domain.RoomID, error) {
	if inviteTarget(account, own, info).Join && info.IsJoinApprovalRequired {
		return "", errJoinRequested
	}
	if jid.IsEmpty() {
		jid = info.JID
	}
	return roomID(account, jid), nil
}
