package telegram

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

// Live updates go through gotd's updates manager, which persists the position in the
// store and fills gaps (downtime, drops) with getDifference, in order.

// live is one connection's updates manager.
type live struct {
	a       *Adapter
	account Account
	manager *updates.Manager
	// self is the account's user ID, once known.
	self atomic.Int64
}

func (a *Adapter) liveFor(account Account) *live {
	l := &live{a: a, account: account}
	dispatcher := tg.NewUpdateDispatcher()
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		return l.message(ctx, e, u.Message)
	})
	dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		return l.message(ctx, e, u.Message)
	})
	l.changes(dispatcher)
	cfg := updates.Config{Handler: dispatcher, Logger: gotdLog{log: a.log}}
	if a.store != nil { // else gotd keeps the position in memory, for this run
		cfg.Storage, cfg.AccessHasher, cfg.UserAccessHasher = a.store, a.store, a.store
	}
	l.manager = updates.New(cfg)
	return l
}

// errNotYet is an update before the connection's user is known.
var errNotYet = errors.New("telegram: an update before the account was known")

func (l *live) message(ctx context.Context, e tg.Entities, m tg.MessageClass) error {
	self := l.self.Load()
	if self == 0 {
		return errNotYet
	}
	msg, ok := incoming(self, m, peer.EntitiesFromUpdate(e))
	if !ok {
		return nil
	}
	return l.a.arrived(ctx, l.account, self, msg)
}

// changes routes edits, deletions, reactions, reads and typing.
func (l *live) changes(d tg.UpdateDispatcher) {
	d.OnEditMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateEditMessage) error {
		return l.edit(ctx, e, u.Message)
	})
	d.OnEditChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateEditChannelMessage) error {
		return l.edit(ctx, e, u.Message)
	})
	d.OnDeleteMessages(func(ctx context.Context, _ tg.Entities, u *tg.UpdateDeleteMessages) error {
		return l.with(func(self int64) { l.a.deleted(ctx, self, u.Messages) })
	})
	d.OnDeleteChannelMessages(func(ctx context.Context, _ tg.Entities, u *tg.UpdateDeleteChannelMessages) error {
		return l.with(func(self int64) { l.a.deletedIn(ctx, self, -(channelMark + u.ChannelID), u.Messages) })
	})
	d.OnMessageReactions(func(ctx context.Context, _ tg.Entities, u *tg.UpdateMessageReactions) error {
		return l.with(func(self int64) {
			if chat, ok := markedPeer(u.Peer); ok {
				room := chatRoom(self, chat, topicNumber(u.TopMsgID))
				l.a.heardReactions(ctx, room, inRoom(room, u.MsgID), messageReactions(room, u.MsgID, u.Reactions))
			}
		})
	})
	d.OnReadHistoryInbox(func(ctx context.Context, _ tg.Entities, u *tg.UpdateReadHistoryInbox) error {
		return l.with(func(self int64) {
			chat, ok := markedPeer(u.Peer)
			if ok { // a chat, or one topic of a forum (TopMsgID)
				l.a.readInbox(ctx, chatRoom(self, chat, topicNumber(u.TopMsgID)), u.MaxID, u.StillUnreadCount)
			}
		})
	})
	d.OnReadChannelInbox(func(ctx context.Context, _ tg.Entities, u *tg.UpdateReadChannelInbox) error {
		return l.with(func(self int64) {
			l.a.readInbox(ctx, roomID(self, -(channelMark+u.ChannelID)), u.MaxID, u.StillUnreadCount)
		})
	})
	d.OnDialogUnreadMark(func(ctx context.Context, _ tg.Entities, u *tg.UpdateDialogUnreadMark) error {
		return l.with(func(self int64) {
			if p, ok := u.Peer.(*tg.DialogPeer); ok {
				if chat, ok := markedPeer(p.Peer); ok {
					l.a.markedUnread(ctx, roomID(self, chat), u.Unread)
				}
			}
		})
	})
	d.OnMessagePoll(func(ctx context.Context, _ tg.Entities, u *tg.UpdateMessagePoll) error {
		return l.with(func(self int64) { l.a.pollUpdated(ctx, self, u) })
	})
	d.OnFolderPeers(func(ctx context.Context, _ tg.Entities, u *tg.UpdateFolderPeers) error {
		return l.with(func(self int64) { l.a.folderPeers(ctx, self, u.FolderPeers) })
	})
	l.typing(d)
}

func (l *live) typing(d tg.UpdateDispatcher) {
	d.OnUserTyping(func(_ context.Context, _ tg.Entities, u *tg.UpdateUserTyping) error {
		return l.with(func(self int64) { l.a.typingNotice(self, u.UserID, 0, u.UserID, u.Action) })
	})
	d.OnChatUserTyping(func(_ context.Context, _ tg.Entities, u *tg.UpdateChatUserTyping) error {
		return l.with(func(self int64) {
			if who, ok := markedPeer(u.FromID); ok {
				l.a.typingNotice(self, -u.ChatID, 0, who, u.Action)
			}
		})
	})
	d.OnChannelUserTyping(func(_ context.Context, _ tg.Entities, u *tg.UpdateChannelUserTyping) error {
		return l.with(func(self int64) {
			if who, ok := markedPeer(u.FromID); ok {
				l.a.typingNotice(self, -(channelMark + u.ChannelID), topicNumber(u.TopMsgID), who, u.Action)
			}
		})
	})
}

// with runs f once the connection's user is known.
func (l *live) with(f func(self int64)) error {
	self := l.self.Load()
	if self == 0 {
		return errNotYet
	}
	f(self)
	return nil
}

// edit handles an edited message (reaction changes arrive this way too).
func (l *live) edit(ctx context.Context, e tg.Entities, m tg.MessageClass) error {
	self := l.self.Load()
	if self == 0 {
		return errNotYet
	}
	msg, ok := incoming(self, m, peer.EntitiesFromUpdate(e))
	if !ok {
		return nil
	}
	if err := l.a.edited(ctx, l.account, self, msg); err != nil {
		return err
	}
	if raw, ok := m.(*tg.Message); ok {
		if rs, ok := raw.GetReactions(); ok {
			l.a.reactionsChanged(ctx, msg.RoomID, msg.ID, messageReactions(msg.RoomID, raw.ID, rs))
		}
	}
	return nil
}

// run consumes self's updates until ctx ends, resuming from the saved position
// (Store.release). started fires once updates flow, which is when it counts as connected.
func (l *live) run(ctx context.Context, client *telegram.Client, self int64, started func()) error {
	l.self.Store(self)
	if l.a.store != nil {
		l.a.store.release(self)
	}
	return l.manager.Run(ctx, client.API(), self, updates.AuthOptions{ //nolint:wrapcheck // read by kind in connectOnce
		OnStart: func(context.Context) { started() },
	})
}
