package signal

import (
	"context"
	"fmt"
	"time"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A chat with disappearing messages expects every message in it to carry its timer:
// one without it does not disappear. kith learns a group's timer from the group (its
// details and its changes) and a one-to-one chat's from its changes and the timer its
// messages carry. Signal numbers a one-to-one chat's changes, and an older one never
// undoes a newer (a message sent before a change, delivered after it). What kith sends
// carries the chat's timer and its number.

// keepTimer keeps a chat's timer as something set at version says it.
func (a *Adapter) keepTimer(ctx context.Context, room domain.RoomID, timer time.Duration, version uint32) error {
	if a.cache == nil {
		return nil
	}
	if _, err := a.cache.KeepTimer(ctx, room, timer, version); err != nil {
		return fmt.Errorf("keep the disappearing-messages timer: %w", err)
	}
	return nil
}

// takeTimer keeps a chat's changed timer.
func (a *Adapter) takeTimer(ctx context.Context, self string, t *signalv1.Timer) error {
	b, err := a.bookOf(ctx, self)
	if err != nil {
		return err
	}
	a.booksMu.Lock()
	room, changed := b.chat(self, t.GetChat(), t.GetGroup())
	members := b.members(self, t.GetChat(), t.GetGroup())
	a.booksMu.Unlock()
	if changed {
		if err := a.keepChat(ctx, self, room, members); err != nil {
			return err
		}
	}
	return a.keepTimer(ctx, room.ID, time.Duration(t.GetSeconds())*time.Second, t.GetVersion())
}

// timerOf is a chat's timer (s) and its number, for a message sent there. A failure is
// logged: the message then goes without one.
func (a *Adapter) timerOf(ctx context.Context, room domain.RoomID) (uint32, uint32) {
	if a.cache == nil {
		return 0, 0
	}
	timer, version, err := a.cache.TimerVersion(ctx, room)
	if err != nil {
		a.log.Warn("read a disappearing-messages timer failed", "room", room, "err", err)
		return 0, 0
	}
	return uint32(timer / time.Second), version //nolint:gosec // Signal's timers fit
}
