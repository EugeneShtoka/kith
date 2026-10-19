package signal

import (
	"context"
	"fmt"
	"time"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// noteToSelf is what Signal's apps call the chat with the account itself.
const noteToSelf = "Note to Self"

// takeHistory keeps part of a chat's history from the phone's transfer: the chat as a
// room, its messages, how far the account had read, and its timer. It is history, not
// news: only the room is announced. A part kept again (a sync cut short and resumed) is
// the same messages.
func (a *Adapter) takeHistory(ctx context.Context, self string, h *signalv1.History) error {
	b, err := a.bookOf(ctx, self)
	if err != nil {
		return err
	}
	chat, group := h.GetChat(), h.GetGroup()
	a.booksMu.Lock()
	learned := false
	for _, m := range h.GetMessages() {
		learned = b.learn(person(m.GetSender()), m.GetSenderName(), m.GetSenderNumber()) || learned
		if !group {
			learned = b.learn(person(chat), m.GetChatName(), m.GetChatNumber()) || learned
		}
	}
	room, changed := b.chat(self, chat, group)
	members := b.members(self, chat, group)
	names, links := b.names(), b.links()
	a.booksMu.Unlock()
	if changed || learned {
		if err := a.keepChat(ctx, self, room, members); err != nil {
			return err
		}
	}
	if learned {
		if err := a.cache.SetPeople(ctx, "signal:"+self, names, links); err != nil {
			return fmt.Errorf("keep who people are: %w", err)
		}
	}
	if err := a.keepTimer(ctx, room.ID, time.Duration(h.GetTimer())*time.Second, h.GetTimerVersion()); err != nil {
		return err
	}
	msgs := make([]domain.Message, 0, len(h.GetMessages()))
	for _, m := range h.GetMessages() {
		msgs = append(msgs, message(self, m))
	}
	// Before the messages: a part cut short between the two is handed again with its
	// messages still new, so their reactions are added then.
	if err := a.keepHistoryReactions(ctx, room.ID, msgs, h.GetMessages()); err != nil {
		return err
	}
	if len(msgs) > 0 {
		if err := a.save(ctx, room.ID, msgs...); err != nil {
			return fmt.Errorf("cache a chat's history: %w", err)
		}
		for i, m := range h.GetMessages() {
			a.keepSource(ctx, msgs[i], m.GetAttachments())
			a.keepExpiry(ctx, self, msgs[i], m)
		}
	}
	if read := h.GetReadThrough(); read > 0 {
		if err := a.cache.SaveUnread(ctx, domain.Unread{RoomID: room.ID}, int64(read)); err != nil { //nolint:gosec // a Signal timestamp fits
			return fmt.Errorf("save the read position: %w", err)
		}
	}
	if a.onChanged != nil {
		a.onChanged(room.ID)
	}
	a.recount(ctx, room.ID)
	return nil
}

// keepHistoryReactions adds the reactions the archive has on messages kith does not
// hold yet. The archive is older than anything heard live: a message already held
// keeps the reactions kith has, and a sender's reaction heard live stays.
func (a *Adapter) keepHistoryReactions(ctx context.Context, room domain.RoomID, msgs []domain.Message, wire []*signalv1.Message) error {
	var held map[domain.EventID]bool
	var add []domain.Reaction
	for i, m := range wire {
		if len(m.GetReactions()) == 0 {
			continue
		}
		if _, cached, err := a.cache.MessageByID(ctx, room, msgs[i].ID); err != nil {
			return fmt.Errorf("find an archived message: %w", err)
		} else if cached {
			continue
		}
		if held == nil {
			rs, err := a.cache.Reactions(ctx, room)
			if err != nil {
				return fmt.Errorf("read a chat's reactions: %w", err)
			}
			held = make(map[domain.EventID]bool, len(rs))
			for _, r := range rs {
				held[r.ID] = true
			}
		}
		for _, r := range m.GetReactions() {
			id := reactionID(msgs[i].ID, r.GetSender())
			if held[id] {
				continue
			}
			held[id] = true
			add = append(add, domain.Reaction{ID: id, RoomID: room, Target: msgs[i].ID, Sender: person(r.GetSender()), Key: r.GetEmoji()})
		}
	}
	if len(add) == 0 {
		return nil
	}
	if err := a.cache.SaveReactions(ctx, add); err != nil {
		return fmt.Errorf("cache a chat's reactions: %w", err)
	}
	return nil
}
