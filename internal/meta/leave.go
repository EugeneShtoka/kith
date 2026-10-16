package meta

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/socket"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Leaving a chat deletes it from your Messenger, as Messenger's own "Delete chat" does,
// leaving a group first so it stops reaching you; the others keep theirs. It then goes
// here at once: a page of the inbox fetched before does not bring it back, and a new
// message does.

// LeaveRoom leaves a chat and deletes it from your Messenger.
func (a *Adapter) LeaveRoom(ctx context.Context, room domain.RoomID) error {
	self, key, ok := threadOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAThread, room)
	}
	c := a.connOf(self)
	if c == nil {
		return errNotConnected
	}
	c.mu.Lock()
	client, wa := c.client, c.e2ee
	c.mu.Unlock()
	if client == nil {
		return errNotConnected
	}
	c.applying.Lock()
	t := c.inbox.threads[key]
	var kind table.ThreadType
	metaKey := key // the thread as Meta keys it: an encrypted chat's own key
	if t != nil {
		kind = t.kind
	}
	for k, chat := range c.inbox.alias {
		if chat == key {
			metaKey = k
		}
	}
	c.applying.Unlock()
	if t == nil {
		return fmt.Errorf("%w: %s", errNotCached, room)
	}
	if kind == table.ENCRYPTED_OVER_WA_GROUP && wa == nil {
		return errNoEncrypted
	}
	err := a.guard("leaving a chat", func() error {
		if err := client.WaitUntilCanSendMessages(ctx, sendWait); err != nil {
			return fmt.Errorf("%w: %w", errNotConnected, err)
		}
		switch {
		case kind == table.ENCRYPTED_OVER_WA_GROUP:
			if err := wa.LeaveGroup(ctx, types.JID{User: itoa(key), Server: types.GroupServer}); err != nil {
				return fmt.Errorf("leave the group: %w", err)
			}
		case !kind.IsOneToOne():
			if _, err := client.ExecuteTasks(ctx, &socket.RemoveParticipantTask{ThreadID: key, ContactID: self}); err != nil {
				return fmt.Errorf("leave the group: %w", err)
			}
		}
		syncGroup := int64(1)
		if kind.IsWhatsApp() && metaKey != key {
			syncGroup = 95 // an encrypted chat's thread
		}
		_, err := client.ExecuteTasks(ctx, &socket.DeleteThreadTask{ThreadKey: metaKey, SyncGroup: syncGroup})
		return err //nolint:wrapcheck // wrapped below
	})
	if err != nil {
		return fmt.Errorf("meta: leave %s: %w", room, err)
	}
	a.left(ctx, c, key, time.Now())
	return nil
}

// left drops the chat keyed key, which you left at at.
func (a *Adapter) left(ctx context.Context, c *conn, key int64, at time.Time) {
	c.applying.Lock()
	defer c.applying.Unlock()
	ch := change{read: map[int64]int64{}, left: map[string]int{}}
	c.inbox.leave(key, at.UnixMilli(), &ch)
	a.store(ctx, c, ch)
}
