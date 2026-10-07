package telegram

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An edit is the message again, as it now reads, with the time it was edited: the
// cache shows the newest version, so an older copy (a history page read before the
// edit) never undoes it. A deletion names messages by number: a channel's within the
// channel, the rest's within the account, so those are looked up among its rooms.

// keepDeletedIf sets [display.deleted] keep: whether a deleted message's words stay in
// the cache, and an edited one's earlier versions.
func (a *Adapter) keepDeletedIf(keep bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keepDeleted = keep
}

// keepsDeleted is [display.deleted] keep.
func (a *Adapter) keepsDeleted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keepDeleted
}

// edited folds a version of a message onto it and hands it to the clients; one that
// could not be cached holds the position, as a new message does.
func (a *Adapter) edited(ctx context.Context, account Account, self int64, msg domain.Message) error {
	if err := a.heardLive(ctx, account, self, msg); err != nil {
		return err
	}
	if a.onChanged != nil {
		a.onChanged(msg.RoomID)
	}
	emit(a, a.messages, msg)
	return nil
}

// deletedIn marks messages of one chat deleted, by number: in the chat's room, or in
// whichever of its forum topics' rooms the cache holds them.
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

// deleted marks messages deleted by their numbers within the account: in whichever of
// its chats (not channels, which number their own) the cache holds them. One not
// cached is no loss: nothing shows it.
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

// markDeleted marks a message deleted in the cache, keeping its words only under
// [display.deleted] keep, and tells the clients. Telegram does not say who deleted it.
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

// Redact deletes a message for everyone (yours, or another's where you may), and here.
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

// edit sends draft as the new version of the message it edits, and folds Telegram's
// answer in here (the update it also arrives as is the same version again).
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

// MessageHistory is a message's earlier versions as the cache kept them (under
// [display.deleted] keep); Telegram keeps none to ask for.
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
