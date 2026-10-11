package telegram

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Groups and channels are left on Telegram; a forum is left with all its topics.
// Topics and private chats cannot be left.

// errTopicNotLeft is a topic's room asked to be left.
var errTopicNotLeft = fmt.Errorf("%w: a forum's topic cannot be left on Telegram, only the whole forum (its General room); archive the topic to put it away", api.ErrNotOnNetwork)

// errPrivateNotLeft is a private chat asked to be left.
var errPrivateNotLeft = fmt.Errorf("%w: a private chat cannot be left on Telegram; delete it, or archive it to put it away", api.ErrNotOnNetwork)

// errGroupNotDeleted is a group or channel asked to be deleted as a chat.
var errGroupNotDeleted = fmt.Errorf("%w: only a private chat is deleted on Telegram; leave a group or channel", api.ErrNotOnNetwork)

// errSavedForMe is your saved messages asked to be deleted for someone else too.
var errSavedForMe = fmt.Errorf("%w: your saved messages are yours alone; delete them for you only", api.ErrNotOnNetwork)

// deletingOf is how a chat that can't be left is deleted: a DM for you and optionally
// the peer, Saved Messages for you. Groups, channels and topics are left instead.
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

// withDeleting sets each room's Deleting.
func withDeleting(rooms []domain.Room) []domain.Room {
	for i := range rooms {
		rooms[i].Deleting = deletingOf(rooms[i].ID)
	}
	return rooms
}

// DeleteChat deletes a private chat's history for you (and the peer if forEveryone)
// and drops it from the cache; it reappears when someone writes.
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
	// Telegram deletes long histories in parts, reporting the rest in Offset.
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

// LeaveRoom leaves a group or channel and drops it (and any forum topics) from the cache.
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

// forgetLeft drops left or deleted rooms from the cache and signals the change.
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
