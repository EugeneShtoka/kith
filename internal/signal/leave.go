package signal

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/EugeneShtoka/kith/internal/api"
	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

var errChatNotLeft = fmt.Errorf("%w: a one-to-one chat cannot be left on Signal", api.ErrNotOnNetwork)

// LeaveRoom leaves a group on Signal, which tells its other members, and drops it from
// the cache.
func (a *Adapter) LeaveRoom(ctx context.Context, room domain.RoomID) error {
	self, chat, group, err := a.chatTo(room)
	if err != nil {
		return err
	}
	if !group {
		return errChatNotLeft
	}
	h, err := a.sender(self)
	if err != nil {
		return err
	}
	if _, err := h.client.Leave(ctx, connect.NewRequest(&signalv1.LeaveRequest{Account: self, Group: chat})); err != nil {
		return fmt.Errorf("signal: leave %s: %w", room, err)
	}
	if a.cache == nil {
		return nil
	}
	if err := a.cache.ForgetRooms(ctx, []domain.RoomID{room}); err != nil {
		a.log.Warn("forget the group left failed", "room", room, "err", err)
	}
	a.roomsChanged()
	return nil
}
