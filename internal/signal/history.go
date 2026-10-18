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
	if len(msgs) > 0 {
		if err := a.save(ctx, room.ID, msgs...); err != nil {
			return fmt.Errorf("cache a chat's history: %w", err)
		}
		for i, m := range h.GetMessages() {
			a.keepSource(ctx, msgs[i], m.GetAttachments())
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
