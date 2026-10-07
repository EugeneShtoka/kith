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
var errPrivateNotLeft = fmt.Errorf("%w: a private chat cannot be left on Telegram; archive it to put it away", api.ErrNotOnNetwork)

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
	if err := a.cache.ForgetRooms(ctx, gone); err != nil {
		a.log.Warn("forget the rooms left failed", "room", roomID, "err", err)
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
	return nil
}
