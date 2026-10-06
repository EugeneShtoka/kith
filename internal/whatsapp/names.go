package whatsapp

import (
	"context"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A direct chat is named after the person: as the phone's address book has them, else
// as they named themselves, else their business name. Right after linking, WhatsApp
// sends the history before the names, so a chat first seen then has none yet: it reads
// as the number until the names arrive, and each batch of names that does arrive names
// the account's direct chats again. The chat with yourself is You.

// renameAfter gathers a batch of names (a sync brings thousands) before the chats are
// named again.
const renameAfter = 2 * time.Second

// youName is the chat with yourself.
const youName = "You"

// chatName is what a direct chat with peer is called: the person's name, else
// fallback (the name a message carried), else their number; You for yourself.
func chatName(ctx context.Context, names nameOf, own self, peer types.JID, fallback string) string {
	if own.is(peer) {
		return youName
	}
	if name := names(ctx, peer); name != "" {
		return name
	}
	if fallback != "" {
		return fallback
	}
	if peer.Server == types.DefaultUserServer && peer.User != "" {
		return "+" + peer.User
	}
	return ""
}

// renamer names each account's direct chats again a little after names arrive, once
// per batch.
type renamer struct {
	mu      sync.Mutex
	pending map[string]bool // by account digits
}

// renameLater names the account's direct chats again once this batch of names is in.
func (a *Adapter) renameLater(account Account, client *whatsmeow.Client) {
	a.renames.mu.Lock()
	if a.renames.pending == nil {
		a.renames.pending = map[string]bool{}
	}
	if a.renames.pending[account.Digits] {
		a.renames.mu.Unlock()
		return
	}
	a.renames.pending[account.Digits] = true
	a.renames.mu.Unlock()
	time.AfterFunc(renameAfter, func() {
		a.renames.mu.Lock()
		delete(a.renames.pending, account.Digits)
		a.renames.mu.Unlock()
		a.renameChats(a.lifetime(), account, client)
	})
}

// renameChats names the account's cached direct chats as the store knows the people
// now, changing only those whose name changed.
func (a *Adapter) renameChats(ctx context.Context, account Account, client *whatsmeow.Client) {
	if a.cache == nil {
		return
	}
	rooms, err := a.cache.Rooms(ctx)
	if err != nil {
		a.log.Warn("read the chats to name failed", "account", account.Name, "err", err)
		return
	}
	owner := domain.AccountRooms(domain.ProtocolWhatsApp, account.Digits)
	names, own := a.names(client), selfOf(client)
	changed := 0
	for i := range rooms {
		r := rooms[i]
		if !owner.Owns(r.ID) || !isDirect(r.ID) {
			continue
		}
		peer, err := types.ParseJID(domain.ParseID(string(r.ID)).Native)
		if err != nil {
			continue
		}
		name := chatName(ctx, names, own, peer, "")
		if name == "" || name == r.Name || (r.Name != "" && name == "+"+peer.User && !own.is(peer)) {
			continue // nothing better known than what it has
		}
		a.listing.Lock()
		a.nameDirectChat(ctx, account, r.ID, peer, name)
		a.listing.Unlock()
		changed++
	}
	if changed > 0 && a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
}
