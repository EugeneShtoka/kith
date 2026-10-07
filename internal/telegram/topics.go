package telegram

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A forum is a space, and each of its topics a room in it: a topic's messages are its
// room's, read, counted and filed as any room's are. The General topic is the forum's
// own room. A topic is numbered by the message that began it, which a message in the
// topic names in its reply header (topicOf); that message itself only says the topic's
// title, which is its room's name.

// topicsPage is how many topics one ask for a forum's topics gets; topicsPages bounds
// how many pages are read.
const (
	topicsPage  = 100
	topicsPages = 20
)

// isForum reports whether a dialog is a forum.
func isForum(e dialog) bool {
	channel, ok := e.peer.(*tg.InputPeerChannel)
	if !ok {
		return false
	}
	c, ok := e.entities.Channel(channel.ChannelID)
	return ok && c.Forum
}

// forumListing is a forum's topics, the latest message in each, and whom they name.
type forumListing struct {
	topics []*tg.ForumTopic
	tops   []tg.MessageClass
	ent    peer.Entities
}

// forumTopics is every topic of a forum, page by page.
func forumTopics(ctx context.Context, client *tg.Client, forum tg.InputPeerClass) (forumListing, error) {
	out := forumListing{ent: peer.NewEntities(map[int64]*tg.User{}, map[int64]*tg.Chat{}, map[int64]*tg.Channel{})}
	req := &tg.MessagesGetForumTopicsRequest{Peer: forum, Limit: topicsPage}
	for range topicsPages {
		res, err := client.MessagesGetForumTopics(ctx, req)
		if err != nil {
			return forumListing{}, err //nolint:wrapcheck // the listing names the forum
		}
		out.tops = append(out.tops, res.Messages...)
		page := peer.EntitiesFromResult(res)
		maps.Copy(out.ent.Users(), page.Users())
		maps.Copy(out.ent.Chats(), page.Chats())
		maps.Copy(out.ent.Channels(), page.Channels())
		var last *tg.ForumTopic
		for _, t := range res.Topics {
			if topic, ok := t.(*tg.ForumTopic); ok {
				out.topics = append(out.topics, topic)
				last = topic
			}
		}
		if len(res.Topics) < topicsPage || last == nil {
			break
		}
		req.OffsetDate, req.OffsetID, req.OffsetTopic = last.Date, last.TopMessage, last.ID
	}
	return out, nil
}

// topicRooms is a forum's topics as rooms; General is the forum's own room, listed
// with the forum.
func topicRooms(self, forum int64, topics []*tg.ForumTopic) []domain.Room {
	var out []domain.Room
	for _, t := range topics {
		if t.ID == generalTopic {
			continue
		}
		out = append(out, domain.Room{ID: topicRoomID(self, forum, t.ID), Name: t.Title, Membership: domain.MembershipJoin})
	}
	return out
}

// cacheTopicTops caches the latest message of each topic, in its room: the room list
// shows it, and marking the forum read reads every topic up to it.
func (a *Adapter) cacheTopicTops(ctx context.Context, self int64, f forumListing) {
	if a.cache == nil {
		return
	}
	for _, m := range f.tops {
		msg, ok := incoming(self, m, f.ent)
		if !ok {
			continue
		}
		if err := a.cache.SaveMessages(ctx, msg.RoomID, []domain.Message{msg}); err != nil {
			a.log.Warn("cache a topic's latest message failed", "room", msg.RoomID, "err", err)
		}
	}
}

// listedTopicsUnread keeps what a listing fetched then says of each topic's unread:
// General's is the forum's own room's.
func (a *Adapter) listedTopicsUnread(ctx context.Context, self, forum int64, topics []*tg.ForumTopic, fetched time.Time) {
	for _, t := range topics {
		room := chatRoom(self, forum, topicNumber(t.ID))
		event := domain.EventID("")
		if t.ReadInboxMaxID > 0 {
			event = inRoom(room, t.ReadInboxMaxID)
		}
		ts := a.readTime(ctx, room, event)
		a.changeUnread(ctx, room, fetched, func(u *domain.Unread) int64 {
			u.Notifications, u.Highlights = t.UnreadCount, t.UnreadMentionsCount
			if event != "" {
				u.ReadEvent = event
			}
			return ts
		})
	}
}

// forgetForumThreads drops what was cached of a forum's topics while they were kept as
// threads of its room: they are refetched into their own rooms.
func (a *Adapter) forgetForumThreads(ctx context.Context, self, forum int64, topics []*tg.ForumTopic) {
	if a.cache == nil {
		return
	}
	room := roomID(self, forum)
	roots := make([]domain.EventID, 0, len(topics))
	for _, t := range topics {
		roots = append(roots, inRoom(room, t.ID))
	}
	if err := a.cache.ForgetThreads(ctx, room, roots); err != nil {
		a.log.Warn("forget a forum's old threads failed", "room", room, "err", err)
	}
}

// topicReplies is a page of a topic's messages: Telegram keeps a topic as the replies
// to the message that began it.
func (a *Adapter) topicReplies(ctx context.Context, ch chat, from string, limit int) (tg.MessagesMessagesClass, error) {
	req := &tg.MessagesGetRepliesRequest{Peer: ch.peer, MsgID: ch.topic, Limit: limit}
	if from != "" {
		var err error
		if req.OffsetID, err = strconv.Atoi(from); err != nil {
			return nil, fmt.Errorf("telegram: %q is no place in %s", from, ch.room())
		}
	}
	res, err := ch.conn.client.API().MessagesGetReplies(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("telegram: topic %s: %w", ch.room(), err)
	}
	return res, nil
}

// topicNumber is a topic as a room names it: 0 for General, which is the forum's own.
func topicNumber(topic int) int {
	if topic == generalTopic {
		return 0
	}
	return topic
}

// forumOf is the forum a topic's room is in, and whether it is one.
func forumOf(room domain.RoomID) (domain.RoomID, bool) {
	forum, _, ok := strings.Cut(string(room), topicSep)
	return domain.RoomID(forum), ok
}
