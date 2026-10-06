package telegram

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A forum's topics are kith's threads. A topic is numbered by the message that began
// it (the service message saying its title), which is the thread's root, its words
// the topic's title; a message in a topic names the topic in its reply header. The
// General topic is the room's own timeline. Each topic is read as far as its own
// read position says, the room's as the floor beneath them, and a forum's badge counts
// every topic, as Telegram's does.

// topicsPage is how many topics one listing of a forum asks for.
const topicsPage = 100

// topicCreated is the message that began a topic, as its thread's root: its title;
// ok false for any other service message (someone joined, a title changed).
func topicCreated(self int64, svc *tg.MessageService, ent peer.Entities) (domain.Message, bool) {
	created, ok := svc.Action.(*tg.MessageActionTopicCreate)
	if !ok {
		return domain.Message{}, false
	}
	chat, ok := markedPeer(svc.PeerID)
	if !ok {
		return domain.Message{}, false
	}
	root := domain.Message{
		ID: messageID(self, chat, svc.ID), RoomID: roomID(self, chat),
		Body: created.Title, Timestamp: time.Unix(int64(svc.Date), 0),
	}
	if from, ok := svc.FromID.(*tg.PeerUser); ok {
		root.Sender = personID(from.UserID)
		if u, ok := ent.User(from.UserID); ok {
			root.SenderName = personName(u)
		}
	}
	return root, true
}

// listTopics caches a forum's topics, each as its thread's root, and where each was
// read up to. A forum whose topics could not be read keeps what it had.
func (a *Adapter) listTopics(ctx context.Context, client *tg.Client, self int64, e dialog) {
	channel, ok := e.peer.(*tg.InputPeerChannel)
	if !ok || a.cache == nil {
		return
	}
	if c, ok := e.entities.Channel(channel.ChannelID); !ok || !c.Forum {
		return
	}
	chat := -(channelMark + channel.ChannelID)
	res, err := client.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{Peer: e.peer, Limit: topicsPage})
	if err != nil {
		a.log.Warn("list a forum's topics failed", "room", roomID(self, chat), "err", err)
		return
	}
	room := roomID(self, chat)
	var roots []domain.Message
	var reads []*tg.ForumTopic
	for _, t := range res.Topics {
		topic, ok := t.(*tg.ForumTopic)
		if !ok || topic.ID == 1 { // General is the room's own timeline
			continue
		}
		roots = append(roots, domain.Message{
			ID: messageID(self, chat, topic.ID), RoomID: room, Body: topic.Title,
			Timestamp: time.Unix(int64(topic.Date), 0),
		})
		reads = append(reads, topic)
	}
	if len(roots) == 0 {
		return
	}
	if _, err := a.record(ctx, self, room, roots); err != nil {
		a.log.Warn("cache a forum's topics failed", "room", room, "err", err)
		return
	}
	for _, topic := range reads {
		if topic.ReadInboxMaxID > 0 {
			a.topicRead(ctx, room, messageID(self, chat, topic.ID), messageID(self, chat, topic.ReadInboxMaxID))
		}
	}
	a.recount(ctx, room)
}

// topicRead moves a topic's read position to event, when the cache holds it (else the
// room's floor counts for it).
func (a *Adapter) topicRead(ctx context.Context, room domain.RoomID, root, event domain.EventID) {
	ts := a.readTime(ctx, room, event)
	if ts == 0 {
		return
	}
	if err := a.cache.SaveThreadRead(ctx, room, root, event, ts); err != nil {
		a.log.Warn("save a topic's read position failed", "room", room, "err", err)
	}
}

// readTopic is a topic read up to max, on this or another client.
func (a *Adapter) readTopic(ctx context.Context, self, chat int64, topic, max int) {
	if a.cache == nil {
		return
	}
	room := roomID(self, chat)
	a.topicRead(ctx, room, messageID(self, chat, topic), messageID(self, chat, max))
	a.recount(ctx, room)
}

// ListThreads is a forum's topics as the cache holds them, newest activity first, each
// with what is unread in it.
func (a *Adapter) ListThreads(ctx context.Context, roomID domain.RoomID) ([]domain.Thread, error) {
	if a.cache == nil {
		return nil, nil
	}
	threads, err := a.cache.Threads(ctx, a.Me(), roomID)
	if err != nil {
		return nil, fmt.Errorf("telegram: topics of %s: %w", roomID, err)
	}
	unread, err := a.cache.CountThreadUnread(ctx, a.Me(), roomID)
	if err != nil {
		return nil, fmt.Errorf("telegram: count unread topics of %s: %w", roomID, err)
	}
	return domain.WithUnread(threads, domain.Unread{Threads: unread}), nil
}

// ThreadPage is a page of a topic's messages, oldest first, from where from left off
// ("" for the newest); Next continues it.
func (a *Adapter) ThreadPage(ctx context.Context, roomID domain.RoomID, root domain.EventID, from string, limit int) (domain.TimelinePage, error) {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return domain.TimelinePage{}, err
	}
	topic, ok := messageNumber(roomID, root)
	if !ok {
		return domain.TimelinePage{}, fmt.Errorf("telegram: %s is no topic of %s", root, roomID)
	}
	req := &tg.MessagesGetRepliesRequest{Peer: ch.peer, MsgID: topic, Limit: limit}
	if from != "" {
		if req.OffsetID, err = strconv.Atoi(from); err != nil {
			return domain.TimelinePage{}, fmt.Errorf("telegram: %q is no place in %s", from, root)
		}
	}
	read := time.Now()
	res, err := ch.conn.client.API().MessagesGetReplies(ctx, req)
	if err != nil {
		return domain.TimelinePage{}, fmt.Errorf("telegram: topic %s of %s: %w", root, roomID, err)
	}
	raw, ent, ok := messagesOf(res)
	if !ok {
		return domain.TimelinePage{}, nil
	}
	msgs := a.cachePage(ctx, ch, raw, ent)
	a.pageReactions(ctx, ch.conn.user, raw, read)
	page := domain.TimelinePage{Messages: msgs}
	if len(raw) == limit && len(raw) > 0 {
		page.Next = strconv.Itoa(raw[len(raw)-1].GetID())
	}
	return page, nil
}

// MarkThreadRead marks a topic read up to eventID, on Telegram and here.
func (a *Adapter) MarkThreadRead(ctx context.Context, roomID domain.RoomID, root, eventID domain.EventID, _ bool) error {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	topic, ok1 := messageNumber(roomID, root)
	id, ok2 := messageNumber(roomID, eventID)
	if !ok1 || !ok2 {
		return fmt.Errorf("telegram: %s in %s is no message of %s", eventID, root, roomID)
	}
	if _, err := ch.conn.client.API().MessagesReadDiscussion(ctx, &tg.MessagesReadDiscussionRequest{
		Peer: ch.peer, MsgID: topic, ReadMaxID: id,
	}); err != nil {
		return fmt.Errorf("telegram: mark topic %s read: %w", root, err)
	}
	a.readTopic(ctx, ch.conn.user, ch.id, topic, id)
	return nil
}

// ThreadParticipant reports whether we began a topic or wrote in it.
func (a *Adapter) ThreadParticipant(ctx context.Context, roomID domain.RoomID, root domain.EventID) bool {
	if a.cache == nil {
		return false
	}
	spoke, err := a.cache.SpokeInThread(ctx, roomID, root, a.Me())
	return err == nil && spoke
}
