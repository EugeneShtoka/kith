package whatsapp

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// WhatsApp's archive is a chat's own fact, kept in the account's app state, which
// every linked device shares: the first sync says which chats are archived, a change
// on any device arrives as an event, and kith changes it with an app state patch.
// WhatsApp says them one at a time, in order, so no listing can come between.

// onArchive keeps a chat archived, or not, as WhatsApp says.
func (a *Adapter) onArchive(ctx context.Context, account Account, e *events.Archive) {
	if a.cache == nil || e.Action == nil {
		return
	}
	chat := e.JID.ToNonAD()
	if chat.Server != types.GroupServer {
		chat = person(ctx, chat, types.EmptyJID, a.lookupFor(account)) // a direct chat by its person
	}
	a.archived(ctx, roomID(account.Digits, chat), e.Action.GetArchived())
}

// archived keeps whether a chat is archived, and says the rooms changed.
func (a *Adapter) archived(ctx context.Context, room domain.RoomID, archived bool) {
	if err := a.cache.SetArchived(ctx, map[domain.RoomID]bool{room: archived}); err != nil {
		a.log.Warn("keep a chat's archive failed", "room", room, "err", err)
		return
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
}

// SetArchived archives a chat on WhatsApp, or unarchives it, on every linked device,
// and here. Archiving unpins it, as WhatsApp's own apps do.
func (a *Adapter) SetArchived(ctx context.Context, roomID domain.RoomID, archived bool) error {
	id := domain.ParseID(string(roomID))
	chat, err := types.ParseJID(id.Native)
	if err != nil || id.Network != domain.ProtocolWhatsApp {
		return fmt.Errorf("whatsapp: %s is not a chat", roomID)
	}
	_, client, ok := a.clientFor(id.Account)
	if !ok {
		return fmt.Errorf("whatsapp: account %s is not connected: %w", id.Account, errNetworkOff)
	}
	var last time.Time
	if a.cache != nil {
		if ms, err := a.cache.NewestTS(ctx, roomID); err == nil && ms > 0 {
			last = time.UnixMilli(ms)
		}
	}
	if err := client.SendAppState(ctx, appstate.BuildArchive(chat, archived, last, nil)); err != nil {
		return fmt.Errorf("whatsapp: archive %s: %w", roomID, err)
	}
	if a.cache != nil {
		a.archived(ctx, roomID, archived)
	}
	return nil
}
