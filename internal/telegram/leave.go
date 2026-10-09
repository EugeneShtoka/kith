package telegram

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Leaving a group or a channel is leaving it on Telegram, on every device; a forum is
// left whole, its topics' rooms with it. A topic is part of its forum, and a private
// chat has no one to leave: neither can be left.

// errTopicNotLeft is a topic's room asked to be left.
var errTopicNotLeft = fmt.Errorf("%w: a forum's topic cannot be left on Telegram, only the whole forum (its General room); archive the topic to put it away", api.ErrNotOnNetwork)

// errPrivateNotLeft is a private chat asked to be left.
var errPrivateNotLeft = fmt.Errorf("%w: a private chat cannot be left on Telegram; delete it, or archive it to put it away", api.ErrNotOnNetwork)

// errGroupNotDeleted is a group or channel asked to be deleted as a chat.
var errGroupNotDeleted = fmt.Errorf("%w: only a private chat is deleted on Telegram; leave a group or channel", api.ErrNotOnNetwork)

// errSavedForMe is your saved messages asked to be deleted for someone else too.
var errSavedForMe = fmt.Errorf("%w: your saved messages are yours alone; delete them for you only", api.ErrNotOnNetwork)

// deletingOf is how a Telegram chat that cannot be left is deleted: a private chat
// for you and, if you say so, for the other person; your saved messages for you. A
// group, a channel and a topic are left instead (LeaveRoom).
func deletingOf(roomID domain.RoomID) domain.ChatDeleting {
	parsed := domain.ParseID(string(roomID))
	chat, err := strconv.ParseInt(parsed.Native, 10, 64)
	switch {
	case parsed.Network != domain.ProtocolTelegram, err != nil, chat <= 0:
		return domain.ChatLeft
	case parsed.Native == parsed.Account:
		return domain.ChatDeletedForMe
	default:
		return domain.ChatDeletedForEither
	}
}

// withDeleting is rooms, each saying how it is deleted, if it is.
func withDeleting(rooms []domain.Room) []domain.Room {
	for i := range rooms {
		rooms[i].Deleting = deletingOf(rooms[i].ID)
	}
	return rooms
}

// DeleteChat deletes a private chat's history on Telegram, for you, and, with
// forEveryone, for the other person too, and drops it from the cache: Telegram lists
// it no more until someone writes in it again.
func (a *Adapter) DeleteChat(ctx context.Context, roomID domain.RoomID, forEveryone bool) error {
	switch deletingOf(roomID) {
	case domain.ChatLeft:
		return errGroupNotDeleted
	case domain.ChatDeletedForMe:
		if forEveryone {
			return errSavedForMe
		}
	case domain.ChatDeletedForEither:
	}
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	// Telegram deletes a long history in parts: it says how much is left (Offset).
	for {
		affected, err := ch.conn.client.API().MessagesDeleteHistory(ctx, &tg.MessagesDeleteHistoryRequest{
			Peer: ch.peer, Revoke: forEveryone,
		})
		if err != nil {
			return fmt.Errorf("telegram: delete %s: %w", roomID, err)
		}
		if affected.Offset <= 0 {
			break
		}
	}
	a.forgetLeft(ctx, []domain.RoomID{roomID})
	return nil
}

// LeaveRoom leaves a group or channel on Telegram and drops it, and a forum's topics,
// from the cache.
func (a *Adapter) LeaveRoom(ctx context.Context, roomID domain.RoomID) error {
	if _, inTopic := forumOf(roomID); inTopic {
		return errTopicNotLeft
	}
	if chat, err := strconv.ParseInt(domain.ParseID(string(roomID)).Native, 10, 64); err == nil && chat > 0 {
		return errPrivateNotLeft // a person's ID: a private chat
	}
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	switch p := ch.peer.(type) {
	case *tg.InputPeerChannel:
		_, err = ch.conn.client.API().ChannelsLeaveChannel(ctx, &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash})
	case *tg.InputPeerChat:
		_, err = ch.conn.client.API().MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{ChatID: p.ChatID, UserID: &tg.InputUserSelf{}})
	default:
		return errPrivateNotLeft
	}
	if err != nil {
		return fmt.Errorf("telegram: leave %s: %w", roomID, err)
	}
	if a.cache == nil {
		return nil
	}
	gone := []domain.RoomID{roomID}
	topics := a.cachedTopicRooms(ctx, roomID)
	for i := range topics {
		gone = append(gone, topics[i].ID)
	}
	a.forgetLeft(ctx, gone)
	return nil
}

// forgetLeft drops rooms left or deleted from the cache, and says the rooms changed.
func (a *Adapter) forgetLeft(ctx context.Context, gone []domain.RoomID) {
	if a.cache == nil {
		return
	}
	if err := a.cache.ForgetRooms(ctx, gone); err != nil {
		a.log.Warn("forget the rooms left failed", "rooms", gone, "err", err)
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
}
