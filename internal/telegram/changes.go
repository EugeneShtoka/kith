package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An edit is the whole message with its edit time; the cache keeps the newest, so an
// older copy never undoes it. Deletions name message numbers, per channel for
// channels and per account otherwise, so the latter are looked up across its rooms.

// keepDeletedIf sets [display.deleted] keep: whether deleted text and edit history
// stay cached.
func (a *Adapter) keepDeletedIf(keep bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keepDeleted = keep
}

func (a *Adapter) keepsDeleted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keepDeleted
}

// edited caches and streams a new version; a cache failure holds the position.
func (a *Adapter) edited(ctx context.Context, account Account, self int64, msg domain.Message) error {
	if err := a.heardLive(ctx, account, self, msg); errors.Is(err, errNotOurs) {
		return nil
	} else if err != nil {
		return err
	}
	if a.onChanged != nil {
		a.onChanged(msg.RoomID)
	}
	emit(a, a.messages, msg)
	return nil
}

// deletedIn marks a chat's messages deleted, in its room or whichever topic room
// holds them.
func (a *Adapter) deletedIn(ctx context.Context, self, chat int64, ids []int) {
	own := roomID(self, chat)
	for _, id := range ids {
		a.markDeleted(ctx, own, messageID(self, chat, id), time.Now())
	}
	if a.cache == nil || len(ids) == 0 {
		return
	}
	tails := make([]string, len(ids))
	for i, id := range ids {
		tails[i] = strconv.Itoa(id)
	}
	found, err := a.cache.MessagesEndingIn(ctx, domain.AccountRooms(domain.ProtocolTelegram, strconv.FormatInt(self, 10)), tails)
	if err != nil {
		a.log.Warn("find deleted messages failed", "err", err)
		return
	}
	for i := range found {
		if forum, inTopic := forumOf(found[i].RoomID); inTopic && forum == own {
			a.markDeleted(ctx, found[i].RoomID, found[i].ID, time.Now())
		}
	}
}

// deleted marks messages deleted by account-wide number in whichever non-channel
// chat holds them; uncached ones are ignored.
func (a *Adapter) deleted(ctx context.Context, self int64, ids []int) {
	if a.cache == nil || len(ids) == 0 {
		return
	}
	tails := make([]string, len(ids))
	for i, id := range ids {
		tails[i] = strconv.Itoa(id)
	}
	found, err := a.cache.MessagesEndingIn(ctx, domain.AccountRooms(domain.ProtocolTelegram, strconv.FormatInt(self, 10)), tails)
	if err != nil {
		a.log.Warn("find deleted messages failed", "err", err)
		return
	}
	for i := range found {
		chat, err := strconv.ParseInt(domain.ParseID(string(found[i].RoomID)).Native, 10, 64)
		if err == nil && chat > -channelMark {
			a.markDeleted(ctx, found[i].RoomID, found[i].ID, time.Now())
		}
	}
}

// markDeleted marks a message deleted, keeping its text only under [display.deleted]
// keep. Telegram doesn't say who deleted it.
func (a *Adapter) markDeleted(ctx context.Context, room domain.RoomID, id domain.EventID, at time.Time) {
	keep := a.keepsDeleted()
	gone := domain.Message{ID: id, RoomID: room, Redacted: true, RedactedAt: at}
	if a.cache != nil {
		if err := a.cache.MarkRedacted(ctx, room, id, "", "", at, keep); err != nil {
			a.log.Warn("mark a message deleted failed", "room", room, "err", err)
		}
		if keep {
			if kept, ok, err := a.cache.MessageByID(ctx, room, id); err == nil && ok {
				gone.Body, gone.Format = kept.Body, kept.Format
			}
		}
		if a.onChanged != nil {
			a.onChanged(room)
		}
		a.recount(ctx, room)
	}
	emit(a, a.messages, gone)
}

// Redact deletes a message for everyone, where permitted, and here.
func (a *Adapter) Redact(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, _ string) error {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return err
	}
	id, ok := messageNumber(roomID, eventID)
	if !ok {
		return fmt.Errorf("telegram: %s is no message of %s", eventID, roomID)
	}
	if channel, ok := ch.peer.(*tg.InputPeerChannel); ok {
		_, err = ch.conn.client.API().ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, ID: []int{id},
		})
	} else {
		_, err = ch.conn.client.API().MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{id}})
	}
	if err != nil {
		return fmt.Errorf("telegram: delete in %s: %w", roomID, err)
	}
	a.markDeleted(ctx, roomID, eventID, time.Now())
	return nil
}

// edit sends draft as an edit and caches the answer (its update echo is the same
// version).
func (a *Adapter) edit(ctx context.Context, ch chat, roomID domain.RoomID, draft domain.Draft) error {
	id, ok := messageNumber(roomID, draft.Edits)
	if !ok {
		return fmt.Errorf("telegram: %s is no message of %s", draft.Edits, roomID)
	}
	text, entities, _ := outgoing(draft, a.userHash(ctx, ch.conn))
	req := &tg.MessagesEditMessageRequest{Peer: ch.peer, ID: id}
	req.SetMessage(text)
	req.SetEntities(entities) // none clears what the old version had
	res, err := ch.conn.client.API().MessagesEditMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("telegram: edit in %s: %w", roomID, err)
	}
	if ch.conn.live != nil {
		_ = ch.conn.live.manager.Handle(ctx, res)
	}
	for _, m := range updatedMessages(res) {
		if msg, ok := incoming(ch.conn.user, m, peerEntities(res)); ok && msg.ID == draft.Edits {
			return a.edited(ctx, ch.conn.account, ch.conn.user, msg)
		}
	}
	return nil // edited; it arrives through the updates
}

// MessageHistory is a message's cached earlier versions (under [display.deleted]
// keep); Telegram keeps none.
func (a *Adapter) MessageHistory(ctx context.Context, roomID domain.RoomID, eventID domain.EventID) ([]domain.Revision, domain.Deletion, error) {
	if a.cache == nil {
		return nil, domain.Deletion{}, nil
	}
	revisions, err := a.cache.Revisions(ctx, roomID, eventID)
	if err != nil {
		return nil, domain.Deletion{}, fmt.Errorf("telegram: versions of %s: %w", eventID, err)
	}
	return revisions, domain.Deletion{}, nil
}
