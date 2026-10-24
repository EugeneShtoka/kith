package matrix

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/event"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Marked unread is MSC2867's m.marked_unread room account data, shared across the
// account's clients. Reading a room clears it.

// markedUnread reports one room's m.marked_unread from a sync, and whether it was
// present (absence is silence, not false). The last event wins.
func markedUnread(events []*event.Event) (flag, present bool) {
	for _, evt := range events {
		if evt == nil || evt.Type != event.AccountDataMarkedUnread {
			continue
		}
		parse(evt)
		flag, present = evt.Content.AsMarkedUnread().Unread, true
	}
	return flag, present
}

// MarkRoomUnread sets or clears m.marked_unread. The change reaches the UI via the
// sync echo, not locally.
func (b *InProc) MarkRoomUnread(ctx context.Context, roomID domain.RoomID, unread bool) error {
	content := event.MarkedUnreadEventContent{Unread: unread}
	if err := b.client.SetRoomAccountData(ctx, b.ids.nativeRoom(roomID), event.AccountDataMarkedUnread.Type, &content); err != nil {
		return fmt.Errorf("matrix: mark %s unread=%v: %w", roomID, unread, err)
	}
	return nil
}
