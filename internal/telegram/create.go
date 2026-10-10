package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A chat is made on one account as a channel of Telegram's: a group is a supergroup, a
// forum a supergroup with topics, a channel a broadcast one. Its members are added
// after, by the access hashes the account has seen; one it has never seen cannot be
// added, and is named in the partial failure.

// CreateRoom makes a group, a forum or a channel on the account spec.On names.
func (a *Adapter) CreateRoom(ctx context.Context, spec domain.NewRoom) (domain.RoomID, error) {
	c, err := a.connOf(spec.On)
	if err != nil {
		return "", err
	}
	req := &tg.ChannelsCreateChannelRequest{Title: strings.TrimSpace(spec.Name)}
	switch spec.Kind {
	case domain.ChatGroup:
		req.Megagroup = true
	case domain.ChatForum:
		req.Megagroup, req.Forum = true, true
	case domain.ChatChannel:
		req.Broadcast = true
	default:
		return "", fmt.Errorf("telegram: makes groups, forums and channels, not kind %d", spec.Kind)
	}
	res, err := c.client.API().ChannelsCreateChannel(ctx, req)
	if err != nil {
		return "", fmt.Errorf("telegram: create %q: %w", req.Title, err)
	}
	channel, ok := madeChannel(res)
	if !ok {
		return "", fmt.Errorf("telegram: create %q: Telegram named no channel", req.Title)
	}
	if a.store != nil {
		if err := a.store.SetChannelAccessHash(ctx, c.user, channel.ID, channel.AccessHash); err != nil {
			a.log.Warn("keep a new channel's access hash failed", "err", err)
		}
	}
	if c.live != nil {
		_ = c.live.manager.Handle(ctx, res) // the new chat reaches the room list as an update
	}
	room := roomID(c.user, -(channelMark + channel.ID))
	return room, a.addMembers(ctx, c, channel, spec.Invite)
}

// connOf is the connection of the account an owner names.
func (a *Adapter) connOf(on domain.RoomOwner) (conn, error) {
	id := domain.ParseID(string(on))
	self, err := strconv.ParseInt(id.Account, 10, 64)
	if id.Network != domain.ProtocolTelegram || err != nil {
		return conn{}, fmt.Errorf("telegram: %q is no Telegram account", on)
	}
	conns := a.connected()
	i := slices.IndexFunc(conns, func(c conn) bool { return c.user == self })
	if i < 0 {
		return conn{}, fmt.Errorf("telegram: account %d is not connected", self)
	}
	return conns[i], nil
}

// madeChannel is the channel a creation's updates name.
func madeChannel(res tg.UpdatesClass) (*tg.Channel, bool) {
	var chats []tg.ChatClass
	switch u := res.(type) {
	case *tg.Updates:
		chats = u.Chats
	case *tg.UpdatesCombined:
		chats = u.Chats
	}
	for _, c := range chats {
		if ch, ok := c.(*tg.Channel); ok {
			return ch, true
		}
	}
	return nil, false
}

// addMembers adds people to a new channel by their person IDs; who could not be added
// is the error.
func (a *Adapter) addMembers(ctx context.Context, c conn, channel *tg.Channel, people []string) error {
	var users []tg.InputUserClass
	var unknown []string
	for _, person := range people {
		id, err := strconv.ParseInt(domain.ParseID(person).Native, 10, 64)
		if err != nil || domain.NetworkOf(person) != domain.ProtocolTelegram {
			unknown = append(unknown, person)
			continue
		}
		peer, err := a.inputPeer(ctx, c, id)
		user, ok := peer.(*tg.InputPeerUser)
		if err != nil || !ok {
			unknown = append(unknown, person)
			continue
		}
		users = append(users, &tg.InputUser{UserID: user.UserID, AccessHash: user.AccessHash})
	}
	var errs []error
	if len(users) > 0 {
		_, err := c.client.API().ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
			Users:   users,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("adding its members: %w", err))
		}
	}
	if len(unknown) > 0 {
		errs = append(errs, fmt.Errorf("%s could not be added: this account has not seen them", strings.Join(unknown, ", ")))
	}
	return errors.Join(errs...)
}
