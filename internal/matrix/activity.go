package matrix

import (
	"context"
	"fmt"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Other people's activity (typing) from ephemeral events; never stored.

// typingIn reports who is typing (excluding us) and whether m.typing was present:
// it carries the complete set, and absence means unchanged.
func typingIn(events []*event.Event, self id.UserID) (users []string, present bool) {
	for _, evt := range events {
		if evt == nil || evt.Type != event.EphemeralEventTyping {
			continue
		}
		parse(evt)
		present = true
		users = users[:0]
		for _, u := range evt.Content.AsTyping().UserIDs {
			if u == self {
				continue
			}
			users = append(users, string(u))
		}
	}
	return users, present
}

// SendTyping starts or stops our typing notice; the caller owns the timeout because
// it refreshes it.
// a homeserver-side lifetime chosen here could not be kept in step with either.
func (b *InProc) SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, timeout time.Duration) error {
	if _, err := b.client.UserTyping(ctx, id.RoomID(roomID), typing, timeout); err != nil {
		return fmt.Errorf("matrix: typing notice for %s: %w", roomID, err)
	}
	return nil
}
