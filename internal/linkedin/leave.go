package linkedin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Leaving a conversation deletes it from your inbox on LinkedIn, as LinkedIn's own
// "Delete conversation" does (the others keep theirs), then drops it here the way a
// deletion LinkedIn sends is dropped: a listing fetched before does not bring it back,
// and a new message does.

// leaveSpan bounds asking LinkedIn to delete a conversation.
const leaveSpan = 30 * time.Second

// errNotConnected is a request for an account that is not connected now.
var errNotConnected = fmt.Errorf("%w: the LinkedIn account is not connected", api.ErrNetworkOff)

// LeaveRoom deletes a conversation from your LinkedIn inbox.
func (a *Adapter) LeaveRoom(ctx context.Context, room domain.RoomID) error {
	self, thread, ok := threadOf(room)
	if !ok {
		return fmt.Errorf("%w: %s", errNotAConversation, room)
	}
	c := a.connOf(self)
	if c == nil {
		return errNotConnected
	}
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return errNotConnected
	}
	ctx, cancel := context.WithTimeout(a.withLog(ctx), leaveSpan)
	defer cancel()
	err := a.guard("deleting a conversation", func() error {
		return client.DeleteConversation(ctx, conversationURN(self, thread))
	})
	if err != nil {
		if errors.Is(err, errLibraryPanicked) {
			return err
		}
		return fmt.Errorf("linkedin: delete %s: %w", room, err)
	}
	a.takeConversations(ctx, c, nil, []string{thread})
	return nil
}
