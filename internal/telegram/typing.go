package telegram

import (
	"context"
	"slices"
	"time"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Telegram says someone is typing (or recording, or choosing a sticker) every few
// seconds while they do, and that they stopped when they send or cancel: a typist is
// forgotten typingFor after their last notice otherwise, as Telegram's clients do.
const typingFor = 6 * time.Second

// typingNotice is someone's notice in a chat, or in one forum topic of it: typing, or
// stopped.
func (a *Adapter) typingNotice(self, chat int64, topic int, who int64, action tg.SendMessageActionClass) {
	if who == 0 || who == self {
		return
	}
	room, person := chatRoom(self, chat, topic), personID(who)
	if _, stopped := action.(*tg.SendMessageCancelAction); stopped {
		a.stoppedTyping(room, person)
		return
	}
	a.mu.Lock()
	if a.typing[room] == nil {
		a.typing[room] = map[string]*time.Timer{}
	}
	timer, already := a.typing[room][person]
	if already {
		timer.Reset(typingFor)
	} else {
		a.typing[room][person] = time.AfterFunc(typingFor, func() { a.stoppedTyping(room, person) })
	}
	now := a.typistsLocked(room)
	a.mu.Unlock()
	if !already {
		emit(a, a.activity, domain.Activity{RoomID: room, Typing: now})
	}
}

// stoppedTyping forgets a typist, streaming the room's typists when that changed them.
func (a *Adapter) stoppedTyping(room domain.RoomID, who string) {
	a.mu.Lock()
	timer, was := a.typing[room][who]
	if was {
		timer.Stop()
		delete(a.typing[room], who)
	}
	now := a.typistsLocked(room)
	a.mu.Unlock()
	if was {
		emit(a, a.activity, domain.Activity{RoomID: room, Typing: now})
	}
}

// typistsLocked is who is typing in room, in order. Caller holds mu.
func (a *Adapter) typistsLocked(room domain.RoomID) []string {
	out := make([]string, 0, len(a.typing[room]))
	for who := range a.typing[room] {
		out = append(out, who)
	}
	slices.Sort(out)
	return out
}

// SendTyping tells the chat we are typing, or stopped. A typing notice is a courtesy,
// so an account not connected, or a refusal, is no error.
func (a *Adapter) SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, _ time.Duration) error {
	ch, err := a.chatOf(ctx, roomID)
	if err != nil {
		return nil //nolint:nilerr // a courtesy, as above
	}
	var action tg.SendMessageActionClass = &tg.SendMessageTypingAction{}
	if !typing {
		action = &tg.SendMessageCancelAction{}
	}
	req := &tg.MessagesSetTypingRequest{Peer: ch.peer, Action: action}
	if ch.topic != 0 {
		req.SetTopMsgID(ch.topic)
	}
	if _, err := ch.conn.client.API().MessagesSetTyping(ctx, req); err != nil {
		a.log.Debug("send typing failed", "room", roomID, "err", err)
	}
	return nil
}
