package whatsapp

import (
	"context"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A direct chat is kept under the person's phone number, the identity kith matches
// people by across networks (planning/design-mesh.md, decision 17). One known only by
// the person's LID is kept under the LID until the number is learned; then the LID's
// room merges into the number's, and the cache keeps where it went (room_aliases), so
// what arrives under the LID later is filed under the number. The number's room lists
// the LID's as merged into it (domain.Room.Merged), for kithd to move what names it
// (the config's lists, scheduled messages) and a window to follow it.

// lidOf is the LID a direct chat's message names beside the number it is kept under,
// or the empty JID: the chat's own address, or the other side's alternative one.
func lidOf(info *types.MessageInfo) types.JID {
	if info.IsGroup {
		return types.EmptyJID
	}
	if info.Chat.Server == types.HiddenUserServer {
		return info.Chat.ToNonAD()
	}
	alt := info.SenderAlt
	if info.IsFromMe {
		alt = info.RecipientAlt
	}
	if alt.Server == types.HiddenUserServer {
		return alt.ToNonAD()
	}
	return types.EmptyJID
}

// mergeLID files the direct chat with lid under its number from now on, merging the
// LID's room into the number's. Nothing is done once that is kept already. Caller holds
// filing, either way.
func (a *Adapter) mergeLID(ctx context.Context, account Account, lid, number types.JID) {
	if a.cache == nil || number.Server != types.DefaultUserServer {
		return
	}
	from, into := roomID(account.Digits, lid), roomID(account.Digits, number)
	if was, ok := a.aliasOf(ctx, from); ok && was == into {
		return
	}
	m := domain.RoomMerge{
		From: from, Into: into,
		FromPerson: domain.NativePerson(domain.ProtocolWhatsApp, lid.ToNonAD().String()),
		IntoPerson: domain.NativePerson(domain.ProtocolWhatsApp, number.ToNonAD().String()),
	}
	// Under listing: a listing's sweep read before the merge would write the LID's room back.
	a.listing.Lock()
	changed, err := a.cache.MergeRoom(ctx, m)
	if err == nil {
		delete(a.heard, from)
	}
	a.listing.Unlock()
	if err != nil {
		a.log.Warn("merge a chat known by its LID into its number's failed", "account", account.Name, "room", from, "err", err)
		return
	}
	a.mu.Lock()
	if a.aliases != nil {
		a.aliases[from] = into
	}
	if at, ok := a.positions[from]; ok && at.After(a.positions[into]) {
		a.positions[into] = at
	}
	delete(a.positions, from)
	delete(a.typing, from)
	a.mu.Unlock()
	if !changed {
		return
	}
	a.recount(ctx, into)
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
}

// mergeKnown merges every chat of account kept by its LID whose number is known now:
// learned while kith was away, or before kith merged such chats. Run again after being
// cut off, it merges what is left.
func (a *Adapter) mergeKnown(ctx context.Context, account Account, client *whatsmeow.Client) {
	if a.cache == nil {
		return
	}
	a.filing.Lock()
	defer a.filing.Unlock()
	rooms, err := a.accountRooms(ctx, account, func(room domain.RoomID) bool {
		jid, err := types.ParseJID(domain.ParseID(string(room)).Native)
		return err == nil && jid.Server == types.HiddenUserServer
	})
	if err != nil {
		a.log.Warn("read the chats known by their LIDs failed", "account", account.Name, "err", err)
		return
	}
	lookup := a.pnLookup(account, client)
	for i := range rooms {
		lid, _ := types.ParseJID(domain.ParseID(string(rooms[i].ID)).Native)
		if number := lookup(ctx, lid); !number.IsEmpty() {
			a.mergeLID(ctx, account, lid, number)
		}
	}
}

// aliasOf is the room room was merged into, loading the merges on first use.
func (a *Adapter) aliasOf(ctx context.Context, room domain.RoomID) (domain.RoomID, bool) {
	a.mu.Lock()
	loaded := a.aliases != nil
	a.mu.Unlock()
	if !loaded {
		aliases, err := a.cache.RoomAliases(ctx)
		if err != nil {
			a.log.Warn("read the merged chats failed", "err", err)
			return "", false
		}
		a.mu.Lock()
		if a.aliases == nil {
			a.aliases = aliases
		}
		a.mu.Unlock()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	into, ok := a.aliases[room]
	return into, ok
}

// filedUnder is the room a write naming room goes to: the one it was merged into, if
// any.
func (a *Adapter) filedUnder(ctx context.Context, room domain.RoomID) domain.RoomID {
	if a.cache == nil {
		return room
	}
	if into, ok := a.aliasOf(ctx, room); ok {
		return into
	}
	return room
}
