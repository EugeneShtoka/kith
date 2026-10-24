package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Links to chats no listing holds are looked up on the first connected account (by
// name). A person, by @username or phone number, is a private chat to write in:
// Telegram makes the dialog with the first message, so the chat is opened before it
// exists. A group or channel the account is not in is joined only when asked.

// linkConn is the connected account on names, which the caller picked
// (domain.PickAccount).
func (a *Adapter) linkConn(on domain.RoomOwner) (conn, error) {
	for _, c := range a.connected() {
		if domain.AccountRooms(domain.ProtocolTelegram, strconv.FormatInt(c.user, 10)) == on {
			return c, nil
		}
	}
	return conn{}, fmt.Errorf("telegram: %s is no connected account: %w", on, api.ErrNetworkOff)
}

// ResolveLink looks up the chat a Telegram link names: a person to write to, or a
// group or channel, joined or to be joined.
func (a *Adapter) ResolveLink(ctx context.Context, uri string, on domain.RoomOwner) (domain.LinkTarget, error) {
	link, ok := domain.ParseTelegramLink(uri)
	if !ok {
		return domain.LinkTarget{}, fmt.Errorf("telegram: not a Telegram link: %s", uri)
	}
	c, err := a.linkConn(on)
	if err != nil {
		return domain.LinkTarget{}, err
	}
	switch link.Kind {
	case domain.TelegramLinkUsername, domain.TelegramLinkPhone:
		res, err := resolvePeer(ctx, c, link)
		if err != nil {
			return domain.LinkTarget{}, err
		}
		return a.peerTarget(ctx, c, res)
	case domain.TelegramLinkInvite:
		invite, err := c.client.API().MessagesCheckChatInvite(ctx, link.Invite)
		if err != nil {
			return domain.LinkTarget{}, fmt.Errorf("telegram: check the invite: %w", err)
		}
		return a.inviteTarget(ctx, c, invite)
	case domain.TelegramLinkPrivateChannel, domain.TelegramLinkNone:
	}
	// A private channel's link opens it only for its members, whose listing has it.
	return domain.LinkTarget{}, errors.New("telegram: this account is not in that private channel")
}

// JoinLink joins the group or channel a Telegram link names (by @username or invite),
// and returns its room.
func (a *Adapter) JoinLink(ctx context.Context, uri string, on domain.RoomOwner) (domain.RoomID, error) {
	link, ok := domain.ParseTelegramLink(uri)
	if !ok {
		return "", fmt.Errorf("telegram: not a Telegram link: %s", uri)
	}
	c, err := a.linkConn(on)
	if err != nil {
		return "", err
	}
	var joined tg.MessagesChatInviteJoinResultClass
	switch link.Kind {
	case domain.TelegramLinkUsername:
		res, resolveErr := resolvePeer(ctx, c, link)
		if resolveErr != nil {
			return "", resolveErr
		}
		ch, ok := resolvedChannel(res)
		if !ok {
			return "", fmt.Errorf("telegram: @%s is not a group or channel to join", link.Username)
		}
		joined, err = c.client.API().ChannelsJoinChannel(ctx, &tg.InputChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash})
		if err != nil {
			return "", fmt.Errorf("telegram: join @%s: %w", link.Username, err)
		}
	case domain.TelegramLinkInvite:
		joined, err = c.client.API().MessagesImportChatInvite(ctx, link.Invite)
		if tgerr.Is(err, "USER_ALREADY_PARTICIPANT") {
			return a.alreadyIn(ctx, c, link.Invite)
		}
		if tgerr.Is(err, "INVITE_REQUEST_SENT") {
			return "", errors.New("telegram: asked to join; an admin of the chat has to approve it")
		}
		if err != nil {
			return "", fmt.Errorf("telegram: join by the invite: %w", err)
		}
	default:
		return "", errors.New("telegram: that link names no chat to join")
	}
	ok2, isOK := joined.(*tg.MessagesChatInviteJoinResultOk)
	if !isOK {
		return "", errors.New("telegram: this chat is joined through a bot's page; join it in a Telegram app")
	}
	room, found := a.joinedRoom(ctx, c, ok2.Updates)
	if !found {
		return "", errors.New("telegram: joined, but Telegram named no chat")
	}
	if c.live != nil {
		_ = c.live.manager.Handle(ctx, ok2.Updates) // the chat reaches the room list as an update
	}
	return room, nil
}

// resolvePeer asks Telegram who a username or phone number is.
func resolvePeer(ctx context.Context, c conn, link domain.TelegramLink) (*tg.ContactsResolvedPeer, error) {
	if link.Kind == domain.TelegramLinkPhone {
		res, err := c.client.API().ContactsResolvePhone(ctx, link.Phone)
		if err != nil {
			return nil, fmt.Errorf("telegram: look up +%s: %w", link.Phone, err)
		}
		return res, nil
	}
	res, err := c.client.API().ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: link.Username})
	if err != nil {
		return nil, fmt.Errorf("telegram: look up @%s: %w", link.Username, err)
	}
	return res, nil
}

// peerTarget is the chat a resolved username or number is, its access hash kept so
// the chat can be read and written before any listing holds it.
func (a *Adapter) peerTarget(ctx context.Context, c conn, res *tg.ContactsResolvedPeer) (domain.LinkTarget, error) {
	switch p := res.Peer.(type) {
	case *tg.PeerUser:
		for _, raw := range res.Users {
			u, ok := raw.(*tg.User)
			if !ok || u.ID != p.UserID {
				continue
			}
			if u.ID == c.user {
				return domain.LinkTarget{Room: domain.Room{ID: roomID(c.user, c.user), Name: "Saved Messages", IsDirect: true}}, nil
			}
			a.keepUserHash(ctx, c, u)
			return domain.LinkTarget{Room: domain.Room{
				ID: roomID(c.user, u.ID), Name: personName(u), IsDirect: true, Handle: userHandle(u),
			}}, nil
		}
	case *tg.PeerChannel:
		if ch, ok := resolvedChannel(res); ok {
			a.keepChannelHash(ctx, c, ch)
			return channelTarget(c.user, ch, ch.Left), nil
		}
	case *tg.PeerChat:
	}
	return domain.LinkTarget{}, errors.New("telegram: Telegram named no chat for that link")
}

// inviteTarget is the chat an invite is to: one the account is in, or one to join.
func (a *Adapter) inviteTarget(ctx context.Context, c conn, invite tg.ChatInviteClass) (domain.LinkTarget, error) {
	switch i := invite.(type) {
	case *tg.ChatInviteAlready:
		return a.chatTarget(ctx, c, i.Chat, false)
	case *tg.ChatInvitePeek:
		return a.chatTarget(ctx, c, i.Chat, true)
	case *tg.ChatInvite:
		kind := "group"
		if i.Broadcast {
			kind = "channel"
		}
		about := kind
		if i.ParticipantsCount > 0 {
			about = fmt.Sprintf("%s, %d members", kind, i.ParticipantsCount)
		}
		if i.RequestNeeded {
			about += "; an admin approves who joins"
		}
		return domain.LinkTarget{Room: domain.Room{Name: i.Title}, Join: true, About: about}, nil
	}
	return domain.LinkTarget{}, errors.New("telegram: Telegram named no chat for that invite")
}

// chatTarget is a chat Telegram named, as a link's target.
func (a *Adapter) chatTarget(ctx context.Context, c conn, chat tg.ChatClass, join bool) (domain.LinkTarget, error) {
	switch ch := chat.(type) {
	case *tg.Channel:
		a.keepChannelHash(ctx, c, ch)
		return channelTarget(c.user, ch, join), nil
	case *tg.Chat:
		return domain.LinkTarget{Room: domain.Room{ID: roomID(c.user, -ch.ID), Name: ch.Title}, Join: join, About: "group"}, nil
	}
	return domain.LinkTarget{}, errors.New("telegram: Telegram named no chat for that link")
}

// channelTarget is a channel or supergroup as a link's target.
func channelTarget(self int64, ch *tg.Channel, join bool) domain.LinkTarget {
	room := domain.Room{
		ID: roomID(self, -(channelMark + ch.ID)), Name: ch.Title, Forum: ch.Forum, Handle: channelHandle(ch),
	}
	if !join {
		return domain.LinkTarget{Room: room}
	}
	kind := "group"
	if ch.Broadcast {
		kind = "channel"
	}
	if n, ok := ch.GetParticipantsCount(); ok && n > 0 {
		kind = fmt.Sprintf("%s, %d members", kind, n)
	}
	return domain.LinkTarget{Room: room, Join: true, About: kind}
}

// resolvedChannel is the channel a resolved username names, if it names one.
func resolvedChannel(res *tg.ContactsResolvedPeer) (*tg.Channel, bool) {
	p, ok := res.Peer.(*tg.PeerChannel)
	if !ok {
		return nil, false
	}
	for _, raw := range res.Chats {
		if ch, ok := raw.(*tg.Channel); ok && ch.ID == p.ChannelID {
			return ch, true
		}
	}
	return nil, false
}

// joinedRoom finds the chat a join's updates name, keeping a channel's access hash.
func (a *Adapter) joinedRoom(ctx context.Context, c conn, res tg.UpdatesClass) (domain.RoomID, bool) {
	var chats []tg.ChatClass
	switch u := res.(type) {
	case *tg.Updates:
		chats = u.Chats
	case *tg.UpdatesCombined:
		chats = u.Chats
	}
	for _, raw := range chats {
		switch ch := raw.(type) {
		case *tg.Channel:
			a.keepChannelHash(ctx, c, ch)
			return roomID(c.user, -(channelMark + ch.ID)), true
		case *tg.Chat:
			return roomID(c.user, -ch.ID), true
		}
	}
	return "", false
}

// alreadyIn is the room of an invite's chat the account turned out to be in already.
func (a *Adapter) alreadyIn(ctx context.Context, c conn, hash string) (domain.RoomID, error) {
	invite, err := c.client.API().MessagesCheckChatInvite(ctx, hash)
	if err != nil {
		return "", fmt.Errorf("telegram: check the invite: %w", err)
	}
	target, err := a.inviteTarget(ctx, c, invite)
	if err != nil || target.Room.ID == "" {
		return "", errors.New("telegram: already in that chat, but Telegram did not say which")
	}
	return target.Room.ID, nil
}

// keepUserHash stores a looked-up person's access hash, which the chat's reads and
// sends need (inputPeer) until a listing holds it.
func (a *Adapter) keepUserHash(ctx context.Context, c conn, u *tg.User) {
	if a.store == nil {
		return
	}
	if err := a.store.SetUserAccessHash(ctx, c.user, u.ID, u.AccessHash); err != nil {
		a.log.Warn("keep a looked-up person's access hash failed", "err", err)
	}
}

// keepChannelHash stores a looked-up channel's access hash, for joining and reading it.
func (a *Adapter) keepChannelHash(ctx context.Context, c conn, ch *tg.Channel) {
	if a.store == nil || ch.Min {
		return // a min channel's hash is not usable on its own
	}
	if err := a.store.SetChannelAccessHash(ctx, c.user, ch.ID, ch.AccessHash); err != nil {
		a.log.Warn("keep a looked-up channel's access hash failed", "err", err)
	}
}
