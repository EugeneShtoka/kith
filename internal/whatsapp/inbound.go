package whatsapp

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// onMessage caches a message an account received, or sent from another of its
// devices, and hands it to the clients.
func (a *Adapter) onMessage(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Message) {
	lookup := a.pnLookup(client)
	own := selfOf(client)
	chat := chatOf(ctx, &e.Info, lookup)
	room := roomID(account.Digits, chat)
	from := own.pn
	if !e.Info.IsFromMe {
		from = person(ctx, e.Info.Sender, e.Info.SenderAlt, lookup)
	}
	msg, ok := incoming(account.Digits, room, &e.Info, e.Message, domain.NativePerson(domain.ProtocolWhatsApp, from.String()), own)
	if !ok {
		return
	}
	msg.SenderName = a.names(client)(ctx, from)
	if msg.SenderName == "" && !e.Info.IsFromMe {
		msg.SenderName = e.Info.PushName
	}
	if a.cache != nil {
		newGroup := false
		a.record(ctx, account, msg, func() {
			if !e.Info.IsGroup {
				a.knowDirectChat(ctx, account, client, room, chat, e.Info.PushName, e.Info.IsFromMe)
				return
			}
			newGroup = a.joinGroup(ctx, account, room)
		})
		if newGroup {
			a.refreshLater(account, client) // for its name and members
		}
	}
	emit(a, a.messages, msg)
}

// joinGroup makes a group a message came to one of the account's rooms, and reports
// whether it was not before (a group just joined, or one the listing has not caught
// up with).
func (a *Adapter) joinGroup(ctx context.Context, account Account, room domain.RoomID) bool {
	n, err := a.cache.JoinRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits), []domain.RoomID{room})
	if err != nil {
		a.log.Warn("record a group failed", "account", account.Name, "room", room, "err", err)
		return false
	}
	return n > 0
}

// record caches a message, with whatever its room needs first, where no listing's
// sweep can come between the room and the message (see saveListing).
func (a *Adapter) record(ctx context.Context, account Account, msg domain.Message, room func()) {
	a.listing.Lock()
	a.heard[msg.RoomID] = time.Now()
	room()
	err := a.cache.SaveMessages(ctx, msg.RoomID, []domain.Message{msg})
	a.listing.Unlock()
	if err != nil {
		a.log.Warn("cache a message failed", "account", account.Name, "room", msg.RoomID, "err", err)
		return
	}
	if a.onCached != nil {
		a.onCached(msg)
	}
}

// knowDirectChat records a direct chat as a room named after the person, with them as
// its member, the first time it is seen and whenever their name may have changed.
func (a *Adapter) knowDirectChat(ctx context.Context, account Account, client *whatsmeow.Client, room domain.RoomID, peer types.JID, pushName string, fromMe bool) {
	name := a.names(client)(ctx, peer)
	if name == "" && !fromMe {
		name = pushName
	}
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits)
	chat := domain.Room{ID: room, Name: name, IsDirect: true}
	if name != "" {
		chat.Members = []string{name}
	}
	if err := a.cache.AddRooms(ctx, owner, []domain.Room{chat}); err != nil {
		a.log.Warn("record a direct chat failed", "account", account.Name, "room", room, "err", err)
		return
	}
	member := domain.Member{UserID: domain.NativePerson(domain.ProtocolWhatsApp, peer.String()), DisplayName: name}
	if err := a.cache.SaveMember(ctx, room, member); err != nil {
		a.log.Warn("record a direct chat's member failed", "room", room, "err", err)
	}
}

// pnLookup finds a LID's phone number in an account's store.
func (a *Adapter) pnLookup(client *whatsmeow.Client) pnOf {
	return func(ctx context.Context, lid types.JID) types.JID {
		pn, err := client.Store.LIDs.GetPNForLID(ctx, lid)
		if err != nil {
			a.log.Debug("look up a LID's phone number failed", "err", err)
			return types.EmptyJID
		}
		return pn
	}
}
