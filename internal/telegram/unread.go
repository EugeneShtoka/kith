package telegram

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Unread is counted two ways. Telegram's counts (from the listing, reads on any
// client, +1 per arrival) and read_inbox_max_id; the cache counts after that position
// when it holds the message, and badges prefer that. A live change between a
// listing's fetch and write wins over the listing.

// unreadSpan is how long a background read-position fetch may take.
const unreadSpan = 30 * time.Second

// loadUnread lazily loads cached unread rows. Caller holds unreadMu.
func (a *Adapter) loadUnread(ctx context.Context) {
	if a.unreadState != nil {
		return
	}
	a.unreadState = map[domain.RoomID]domain.Unread{}
	rows, err := a.cache.Unread(ctx)
	if err != nil {
		a.log.Warn("read unread state failed", "err", err)
		return
	}
	for _, u := range rows {
		if domain.NetworkOf(string(u.RoomID)) == domain.ProtocolTelegram {
			a.unreadState[u.RoomID] = u
		}
	}
}

// changeUnread applies change to room's Telegram unread, stores and streams it.
// change returns the read position's time in Unix ms (0 keeps the cached one).
// fetched is the listing's fetch time, zero for live; live changes since fetched win.
func (a *Adapter) changeUnread(ctx context.Context, room domain.RoomID, fetched time.Time, change func(u *domain.Unread) int64) {
	if a.cache == nil {
		return
	}
	a.unreadMu.Lock()
	a.loadUnread(ctx)
	if live := a.unreadAt[room]; !fetched.IsZero() && !live.IsZero() && !live.Before(fetched) {
		a.unreadMu.Unlock()
		return
	}
	u := a.unreadState[room]
	u.RoomID = room
	readTS := change(&u)
	if err := a.cache.SaveUnread(ctx, u, readTS); err != nil {
		a.unreadMu.Unlock()
		a.log.Warn("save unread state failed", "room", room, "err", err)
		return
	}
	a.unreadState[room] = u
	if fetched.IsZero() {
		a.unreadAt[room] = time.Now()
	}
	a.unreadMu.Unlock()
	a.recount(ctx, room)
}

// counted bumps unread (and mentions) for an arriving message as Telegram does;
// our own message reads the chat.
func (a *Adapter) counted(ctx context.Context, msg domain.Message, mine bool) {
	a.changeUnread(ctx, msg.RoomID, time.Time{}, func(u *domain.Unread) int64 {
		switch {
		case mine:
			u.Notifications, u.Highlights = 0, 0
		default:
			u.Notifications++
			if msg.Mentioned {
				u.Highlights++
			}
		}
		return 0
	})
}

// readTime is event's cached time in Unix ms; 0 if not cached.
func (a *Adapter) readTime(ctx context.Context, room domain.RoomID, event domain.EventID) int64 {
	if a.cache == nil || event == "" {
		return 0
	}
	ts, ok, err := a.cache.MessageTS(ctx, room, event)
	if err != nil || !ok {
		return 0
	}
	return ts
}

// readInbox moves a chat's or topic's read position to max (from any client). If max
// isn't cached, placeRead fetches it.
func (a *Adapter) readInbox(ctx context.Context, room domain.RoomID, max, still int) {
	event := inRoom(room, max)
	ts := a.readTime(ctx, room, event)
	a.changeUnread(ctx, room, time.Time{}, func(u *domain.Unread) int64 {
		u.ReadEvent, u.Notifications = event, still
		if still == 0 {
			u.Highlights = 0
		}
		return ts
	})
	if ts == 0 {
		go a.placeRead(context.WithoutCancel(ctx), room, event)
	}
}

// placeRead fetches an uncached read-position message so the cache can count from it;
// on failure the badge uses Telegram's count.
func (a *Adapter) placeRead(ctx context.Context, room domain.RoomID, event domain.EventID) {
	ctx, cancel := context.WithTimeout(ctx, unreadSpan)
	defer cancel()
	msg, err := a.FetchEvent(ctx, room, event)
	if err != nil {
		return
	}
	a.changeUnread(ctx, room, time.Time{}, func(*domain.Unread) int64 { return msg.Timestamp.UnixMilli() })
}

// markedUnread applies a chat's manual unread mark from any client.
func (a *Adapter) markedUnread(ctx context.Context, room domain.RoomID, marked bool) {
	a.changeUnread(ctx, room, time.Time{}, func(u *domain.Unread) int64 {
		u.Marked = marked
		return 0
	})
}

// listedUnread applies a listing's unread counts.
func (a *Adapter) listedUnread(ctx context.Context, self int64, elems []dialog, listed []domain.Room, fetched time.Time) {
	for _, e := range elems {
		if e.info == nil {
			continue
		}
		chat, ok := markedPeer(e.info.Peer)
		room := roomID(self, chat)
		if !ok || !slices.ContainsFunc(listed, func(r domain.Room) bool { return r.ID == room }) {
			continue
		}
		info, event := e.info, domain.EventID("")
		if info.ReadInboxMaxID > 0 {
			event = messageID(self, chat, info.ReadInboxMaxID)
		}
		var ts int64
		if top, ok := e.top.(*tg.Message); ok && info.ReadInboxMaxID >= top.ID {
			ts = time.Unix(int64(top.Date), 0).UnixMilli() // all read: up to the latest
		} else {
			ts = a.readTime(ctx, room, event)
		}
		a.changeUnread(ctx, room, fetched, func(u *domain.Unread) int64 {
			u.Notifications, u.Highlights, u.Marked = info.UnreadCount, info.UnreadMentionsCount, info.UnreadMark
			if event != "" {
				u.ReadEvent = event
			}
			return ts
		})
	}
}

// recount streams a room's unread: Telegram's counts plus the cache's when possible.
func (a *Adapter) recount(ctx context.Context, room domain.RoomID) {
	if a.cache == nil {
		return
	}
	a.unreadMu.Lock()
	a.loadUnread(ctx)
	u := a.unreadState[room]
	a.unreadMu.Unlock()
	u.RoomID = room
	messages, mentions, counted, err := a.cache.CountUnread(ctx, a.Me(), room)
	if err != nil {
		a.log.Warn("count unread failed", "room", room, "err", err)
		return
	}
	u.Messages, u.Mentions, u.Counted = messages, mentions, counted
	emit(a, a.unread, u)
}

// CachedUnread is every Telegram room's unread counts.
func (a *Adapter) CachedUnread(ctx context.Context) ([]domain.Unread, error) {
	if a.cache == nil {
		return nil, nil
	}
	rows, err := a.cache.Unread(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram: read unread state: %w", err)
	}
	rows = slices.DeleteFunc(rows, func(u domain.Unread) bool {
		return domain.NetworkOf(string(u.RoomID)) != domain.ProtocolTelegram
	})
	counts, err := a.cache.CountUnreadAll(ctx, a.Me())
	if err != nil {
		return nil, fmt.Errorf("telegram: count unread: %w", err)
	}
	for i := range rows {
		if c, ok := counts[rows[i].RoomID]; ok {
			rows[i].Messages, rows[i].Mentions, rows[i].Counted = c.Messages, c.Mentions, true
		}
	}
	return rows, nil
}

// MarkRead marks a chat read up to eventID on Telegram and here. private is ignored:
// Telegram doesn't reveal who read in groups.
func (a *Adapter) MarkRead(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, _ bool) error {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	id, ok := messageNumber(roomID, eventID)
	if !ok {
		return fmt.Errorf("telegram: %s is no message of %s", eventID, roomID)
	}
	switch channel, isChannel := ch.peer.(*tg.InputPeerChannel); {
	case ch.topic != 0: // a forum's topic is read as its own
		_, err = ch.conn.client.API().MessagesReadDiscussion(ctx, &tg.MessagesReadDiscussionRequest{
			Peer: ch.peer, MsgID: ch.topic, ReadMaxID: id,
		})
	case isChannel:
		_, err = ch.conn.client.API().ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, MaxID: id,
		})
	default:
		_, err = ch.conn.client.API().MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: ch.peer, MaxID: id})
	}
	if err != nil {
		return fmt.Errorf("telegram: mark %s read: %w", roomID, err)
	}
	ts := a.readTime(ctx, roomID, eventID)
	a.changeUnread(ctx, roomID, time.Time{}, func(u *domain.Unread) int64 {
		u.ReadEvent, u.Notifications, u.Highlights, u.Marked = eventID, 0, 0, false
		return ts
	})
	return nil
}

// MarkRoomsRead marks each room read up to its newest cached message.
func (a *Adapter) MarkRoomsRead(ctx context.Context, roomIDs []domain.RoomID, private bool) (domain.ReadResult, error) {
	var result domain.ReadResult
	if a.cache == nil {
		result.Skipped = len(roomIDs)
		return result, nil
	}
	latest, err := a.cache.LatestEvents(ctx, roomIDs)
	if err != nil {
		return domain.ReadResult{}, fmt.Errorf("telegram: newest messages: %w", err)
	}
	for _, room := range roomIDs {
		event, ok := latest[room]
		if !ok {
			result.Skipped++
			continue
		}
		if err := a.MarkRead(ctx, room, event, private); err != nil {
			result.Failed++
			if result.FirstError == "" {
				result.FirstError = err.Error()
			}
			continue
		}
		result.Marked++
	}
	return result, nil
}

// MarkRoomUnread sets a chat's unread mark on Telegram and here.
func (a *Adapter) MarkRoomUnread(ctx context.Context, roomID domain.RoomID, unread bool) error {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	if _, err := ch.conn.client.API().MessagesMarkDialogUnread(ctx, &tg.MessagesMarkDialogUnreadRequest{
		Unread: unread, Peer: &tg.InputDialogPeer{Peer: ch.peer},
	}); err != nil {
		return fmt.Errorf("telegram: mark %s unread: %w", roomID, err)
	}
	a.markedUnread(ctx, roomID, unread)
	return nil
}
