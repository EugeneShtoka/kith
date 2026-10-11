package whatsapp

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Leaving a group or community is done on WhatsApp. A community's groups are left one
// by one first (the rail does that). Private chats cannot be left.

var errChatNotLeft = fmt.Errorf("%w: a private chat cannot be left on WhatsApp; archive it to put it away", api.ErrNotOnNetwork)

// LeaveRoom leaves a group or community on WhatsApp and drops it from the cache.
func (a *Adapter) LeaveRoom(ctx context.Context, roomID domain.RoomID) error {
	room := domain.ParseID(string(roomID))
	chat, err := types.ParseJID(room.Native)
	if err != nil {
		return fmt.Errorf("whatsapp: %s is not a chat: %w", roomID, err)
	}
	if chat.Server != types.GroupServer {
		return errChatNotLeft
	}
	_, client, ok := a.clientFor(room.Account)
	if !ok {
		return fmt.Errorf("whatsapp: account %s is not connected: %w", room.Account, errNetworkOff)
	}
	if err := client.LeaveGroup(ctx, chat); err != nil {
		return fmt.Errorf("whatsapp: leave %s: %w", roomID, err)
	}
	if a.cache == nil {
		return nil
	}
	if err := a.cache.ForgetRooms(ctx, []domain.RoomID{roomID}); err != nil {
		a.log.Warn("forget the room left failed", "room", roomID, "err", err)
	}
	if a.onRoomsChanged != nil {
		a.onRoomsChanged()
	}
	return nil
}
