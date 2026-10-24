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

// typingIn reports who is typing (excluding us) and whether m.typing, which carries
// the complete set, was present; absence means unchanged.
func (x ids) typingIn(events []*event.Event, self id.UserID) (users []string, present bool) {
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
			users = append(users, x.user(u))
		}
	}
	return users, present
}

// SendTyping starts or stops our typing notice; the caller, which refreshes it, owns the timeout.
func (b *InProc) SendTyping(ctx context.Context, roomID domain.RoomID, typing bool, timeout time.Duration) error {
	if _, err := b.client.UserTyping(ctx, b.ids.nativeRoom(roomID), typing, timeout); err != nil {
		return fmt.Errorf("matrix: typing notice for %s: %w", roomID, err)
	}
	return nil
}
