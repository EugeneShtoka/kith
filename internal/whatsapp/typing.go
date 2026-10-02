package whatsapp

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// onTyping keeps who is typing in each room and streams the room's set when it moves.
// Our own typing (from another device) is not reported back.
func (a *Adapter) onTyping(ctx context.Context, account Account, client *whatsmeow.Client, e *events.ChatPresence) {
	if selfOf(client).is(e.Sender) {
		return
	}
	lookup := a.pnLookup(client)
	info := types.MessageInfo{MessageSource: e.MessageSource}
	room := roomID(account.Digits, chatOf(ctx, &info, lookup))
	who := domain.NativePerson(domain.ProtocolWhatsApp, person(ctx, e.Sender, e.SenderAlt, lookup).String())
	a.mu.Lock()
	now := a.typing[room]
	was := slices.Clone(now)
	now = slices.DeleteFunc(now, func(id string) bool { return id == who })
	if e.State == types.ChatPresenceComposing {
		now = append(now, who)
	}
	a.typing[room] = now
	a.mu.Unlock()
	if !slices.Equal(was, now) {
		emit(a, a.activity, domain.Activity{RoomID: room, Typing: slices.Clone(now)})
	}
}

// SendTyping tells the chat we are typing, or have stopped. WhatsApp ends a typing
// notice by itself after a while, so the timeout is not passed on.
func (a *Adapter) SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, _ time.Duration) error {
	id := domain.ParseID(string(roomID))
	chat, err := types.ParseJID(id.Native)
	if err != nil {
		return fmt.Errorf("whatsapp: %s is not a chat: %w", roomID, err)
	}
	_, client, ok := a.clientFor(id.Account)
	if !ok || chat.Server == types.NewsletterServer {
		return nil // a typing notice is a courtesy; nothing to say it failed to, or to whom in a channel
	}
	state := types.ChatPresencePaused
	if typing {
		state = types.ChatPresenceComposing
	}
	if err := client.SendChatPresence(ctx, chat, state, types.ChatPresenceMediaText); err != nil {
		return fmt.Errorf("whatsapp: typing in %s: %w", roomID, err)
	}
	return nil
}
