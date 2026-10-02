package whatsapp

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// arrived is one message as kith keeps it, with what its room needs.
type arrived struct {
	msg      domain.Message
	source   *mediaSource // how to load its attachment; nil when it has none
	chat     types.JID    // the other person, for a direct chat; the group otherwise
	group    bool
	fromMe   bool
	pushName string
}

// convert is a WhatsApp message as kith keeps it, live or from history alike.
func (a *Adapter) convert(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Message) (arrived, bool) {
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
		return arrived{}, false
	}
	msg.SenderName = a.names(client)(ctx, from)
	if msg.SenderName == "" && !e.Info.IsFromMe {
		msg.SenderName = e.Info.PushName
	}
	media, source := attachment(e.Message)
	msg.Media = media
	return arrived{msg: msg, source: source, chat: chat, group: e.Info.IsGroup, fromMe: e.Info.IsFromMe, pushName: e.Info.PushName}, true
}

// onMessage caches a message an account received, or sent from another of its
// devices, and hands it to the clients.
func (a *Adapter) onMessage(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Message) {
	if a.onChange(ctx, account, client, e) {
		return
	}
	in, ok := a.convert(ctx, account, client, e)
	if !ok {
		return
	}
	if a.cache != nil {
		newGroup := false
		a.record(ctx, account, in.msg, func() {
			newGroup = a.ensureRoom(ctx, account, client, in, "")
		})
		a.keepSource(ctx, in.msg, in.source)
		if newGroup {
			a.refreshLater(account, client) // for its name and members
		}
		a.placeRead(ctx, in.msg.RoomID, in.msg.Timestamp.Add(-time.Millisecond))
		a.recount(ctx, in.msg.RoomID)
	}
	emit(a, a.messages, in.msg)
}

// ensureRoom makes the room a message belongs in one of the account's: a direct chat
// named after the person (or name, history's), a group joined. It reports a group
// that was not one of its rooms before. Caller holds listing.
func (a *Adapter) ensureRoom(ctx context.Context, account Account, client *whatsmeow.Client, in arrived, name string) bool {
	if !in.group {
		// A push name is the sender's own: it names the chat only when they sent it.
		if name == "" && !in.fromMe {
			name = in.pushName
		}
		a.knowDirectChat(ctx, account, client, in.msg.RoomID, in.chat, name)
		return false
	}
	return a.joinGroup(ctx, account, in.msg.RoomID)
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
	save := a.cache.SaveMessages
	if a.keepsDeleted() {
		save = a.cache.SaveMessagesWithRevisions // an edit keeps what it replaced
	}
	err := save(ctx, msg.RoomID, []domain.Message{msg})
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
func (a *Adapter) knowDirectChat(ctx context.Context, account Account, client *whatsmeow.Client, room domain.RoomID, peer types.JID, fallback string) {
	name := a.names(client)(ctx, peer)
	if name == "" {
		name = fallback
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
