package telegram

import (
	"context"
	"slices"
	"time"

	"github.com/gotd/td/tg"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Telegram repeats typing notices every few seconds and sends a cancel on stop; like
// its clients, a typist expires typingFor after the last notice.
const typingFor = 6 * time.Second

// typingNotice is a start or stop notice in a chat or topic.
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

// stoppedTyping removes a typist, streaming the room if it changed.
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

// SendTyping sends a typing or stop notice; best effort, so failures aren't errors.
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
