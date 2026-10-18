package whatsapp

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/expiry"
)

// A chat with disappearing messages expects every message in it to carry its timer:
// one without it does not disappear, and the phone shows a timer conflict. kith learns
// a chat's timer from what WhatsApp says of it (a group's listing, a direct chat's
// history, a change of timer) and from the messages that arrive in it, keeps it in the
// cache, and gives what it sends there the same.

// timerOf is the disappearing-messages timer a message carries; zero when none.
func timerOf(msg *waE2E.Message) time.Duration {
	_, info, _ := content(msg)
	return time.Duration(info.GetExpiration()) * time.Second
}

// groupTimers are groups' timers as listed, zero for one without, so a group whose
// timer was turned off loses it.
func groupTimers(account string, groups []*types.GroupInfo) map[domain.RoomID]time.Duration {
	timers := make(map[domain.RoomID]time.Duration, len(groups))
	for _, g := range groups {
		var timer time.Duration
		if g.IsEphemeral {
			timer = time.Duration(g.DisappearingTimer) * time.Second
		}
		timers[roomID(account, g.JID)] = timer
	}
	return timers
}

// keepTimers keeps chats' timers. A failure is logged: a send then goes without one.
func (a *Adapter) keepTimers(ctx context.Context, timers map[domain.RoomID]time.Duration) {
	if a.cache == nil {
		return
	}
	if err := a.cache.SetTimers(ctx, timers); err != nil {
		a.log.Warn("keep a disappearing-messages timer failed", "err", err)
	}
}

// withTimer gives a message sent to room the room's timer.
func (a *Adapter) withTimer(ctx context.Context, room domain.RoomID, msg *waE2E.Message) {
	if a.cache == nil {
		return
	}
	timer, err := a.cache.Timer(ctx, room)
	if err != nil {
		a.log.Warn("read a disappearing-messages timer failed", "room", room, "err", err)
		return
	}
	if timer <= 0 {
		return
	}
	if text := msg.Conversation; text != nil {
		// Plain text carries no context; the extended form does.
		msg.Conversation, msg.ExtendedTextMessage = nil, &waE2E.ExtendedTextMessage{Text: text}
	}
	info := contextOf(msg)
	if info == nil {
		return
	}
	if *info == nil {
		*info = &waE2E.ContextInfo{}
	}
	expiration := uint32(timer / time.Second) // #nosec G115 -- WhatsApp's timers are at most 90 days
	(*info).Expiration = &expiration
}

// contextOf is where an outgoing message keeps its context; nil for a kind kith does
// not send.
func contextOf(msg *waE2E.Message) **waE2E.ContextInfo {
	switch {
	case msg.GetExtendedTextMessage() != nil:
		return &msg.ExtendedTextMessage.ContextInfo
	case msg.GetImageMessage() != nil:
		return &msg.ImageMessage.ContextInfo
	case msg.GetVideoMessage() != nil:
		return &msg.VideoMessage.ContextInfo
	case msg.GetAudioMessage() != nil:
		return &msg.AudioMessage.ContextInfo
	case msg.GetDocumentMessage() != nil:
		return &msg.DocumentMessage.ContextInfo
	}
	return nil
}

// A message sent with a timer disappears once it has run from its sending (WhatsApp
// counts from there, for everyone), and is then deleted as a deletion is.

// keepExpiry keeps when msg, sent with timer, disappears.
func (a *Adapter) keepExpiry(ctx context.Context, msg domain.Message, timer time.Duration) {
	if a.cache == nil || timer <= 0 {
		return
	}
	if err := a.cache.SetExpiry(ctx, msg.RoomID, msg.ID, timer, msg.Timestamp); err != nil {
		a.log.Warn("keep when a message disappears failed", "room", msg.RoomID, "err", err)
	}
}

// keepExpiring deletes disappearing messages as they come due, until ctx ends.
func (a *Adapter) keepExpiring(ctx context.Context) {
	if a.cache == nil {
		return
	}
	expiry.Sweeper{
		Cache: a.cache, Rooms: domain.NetworkRooms(domain.ProtocolWhatsApp), Keep: a.keepsDeleted, Log: a.log,
		Gone: func(stub domain.Message) { emit(a, a.messages, stub) },
		Changed: func(room domain.RoomID) {
			if a.onChanged != nil {
				a.onChanged(room)
			}
			a.recount(ctx, room)
		},
	}.Run(ctx)
}
