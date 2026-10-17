package whatsapp

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// arrived is a converted message plus what its room needs.
type arrived struct {
	msg      domain.Message
	source   *mediaSource // how to load its attachment; nil when it has none
	chat     types.JID    // the other person, for a direct chat; the group otherwise
	group    bool
	channel  bool   // posted in a channel: the channel is its sender
	quoted   *quote // what it quotes, when a reply
	fromMe   bool
	pushName string
}

// convert converts a live or history WhatsApp message.
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
	channelPost := chat.Server == types.NewsletterServer
	// Mentions name a LID; map to the number, as senders are, so the name resolves.
	for i := range msg.Mentions {
		if jid, err := types.ParseJID(domain.ParseID(msg.Mentions[i].UserID).Native); err == nil {
			msg.Mentions[i].UserID = domain.NativePerson(domain.ProtocolWhatsApp, person(ctx, jid, types.EmptyJID, lookup).String())
		}
	}
	msg.SenderName = a.names(client)(ctx, from)
	if channelPost {
		if ch, ok := a.channelOf(room); ok {
			msg.SenderName = ch.name
		}
	}
	if msg.SenderName == "" && !e.Info.IsFromMe {
		msg.SenderName = e.Info.PushName
	}
	media, source := attachment(e.Message)
	msg.Media = media
	if poll := pollOf(e.Message); poll != nil {
		msg.Poll, msg.Body = poll, poll.Summary()
	}
	return arrived{
		msg: msg, source: source, chat: chat, group: e.Info.IsGroup, channel: channelPost,
		quoted: quotedBy(ctx, account.Digits, e.Message, lookup),
		fromMe: e.Info.IsFromMe, pushName: e.Info.PushName,
	}, true
}

// onMessage caches a received message (or one sent from another device) and emits it.
func (a *Adapter) onMessage(ctx context.Context, account Account, client *whatsmeow.Client, e *events.Message) {
	if a.onChange(ctx, account, client, e) {
		return
	}
	in, ok := a.convert(ctx, account, client, e)
	if !ok {
		return
	}
	in.msg.Seq = a.arrival()
	if a.cache != nil {
		newGroup := false
		a.record(ctx, account, in.msg, func() {
			newGroup = a.ensureRoom(ctx, account, client, in, "")
		})
		a.keepSource(ctx, in.msg, in.source)
		a.keepQuote(ctx, in.msg.RoomID, in.quoted)
		if newGroup {
			a.refreshLater(account, client) // for its name and members
		}
		a.placeRead(ctx, in.msg.RoomID, in.msg.Timestamp.Add(-time.Millisecond))
		a.recount(ctx, in.msg.RoomID)
		// A message without a timer says nothing: only a change of timer turns it off.
		if timer := timerOf(e.Message); timer > 0 {
			a.keepTimers(ctx, map[domain.RoomID]time.Duration{in.msg.RoomID: timer})
		}
	}
	emit(a, a.messages, in.msg)
}

// arrival orders live messages within a second: WhatsApp timestamps are whole seconds
// and IDs are unordered, but delivery is in send order. Strictly increasing
// nanoseconds, so order also holds across restarts.
func (a *Adapter) arrival() int64 {
	for {
		last := a.arrived.Load()
		next := max(time.Now().UnixNano(), last+1)
		if a.arrived.CompareAndSwap(last, next) {
			return next
		}
	}
}

// ensureRoom makes a message's room one of the account's, reporting a group or
// channel that was new. Caller holds listing.
func (a *Adapter) ensureRoom(ctx context.Context, account Account, client *whatsmeow.Client, in arrived, name string) bool {
	if !in.group && !in.channel {
		// A push name is the sender's, so it names the chat only on their messages.
		if name == "" && !in.fromMe {
			name = in.pushName
		}
		a.knowDirectChat(ctx, account, client, in.msg.RoomID, in.chat, name)
		return false
	}
	return a.joinGroup(ctx, account, in.msg.RoomID)
}

// joinGroup adds a message's group to the account's rooms, reporting whether it was new.
func (a *Adapter) joinGroup(ctx context.Context, account Account, room domain.RoomID) bool {
	n, err := a.cache.JoinRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits), []domain.RoomID{room})
	if err != nil {
		a.log.Warn("record a group failed", "account", account.Name, "room", room, "err", err)
		return false
	}
	return n > 0
}

// record caches a message after its room, under listing so no sweep comes between.
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

// knowDirectChat records a direct chat named after the person, with them as member.
func (a *Adapter) knowDirectChat(ctx context.Context, account Account, client *whatsmeow.Client, room domain.RoomID, peer types.JID, fallback string) {
	a.nameDirectChat(ctx, account, room, peer, chatName(ctx, a.names(client), selfOf(client), peer, fallback))
}

// nameDirectChat records a direct chat under name. Caller holds listing.
func (a *Adapter) nameDirectChat(ctx context.Context, account Account, room domain.RoomID, peer types.JID, name string) {
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

// pnLookup maps a LID to its phone number via the account's store.
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
