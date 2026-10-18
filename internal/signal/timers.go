package signal

import (
	"context"
	"fmt"
	"time"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/expiry"
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

// A message sent with a timer disappears once it has run: from its sending for the
// account's own, from the account's reading for others' (as Signal's apps count), or
// from when the phone had started it, for one in the history. It is then deleted as a
// deletion is.

// signalRooms are every Signal account's rooms: a message expires whether or not its
// account is connected.
var signalRooms = domain.NetworkRooms(domain.ProtocolSignal)

// keepExpiry keeps when msg, as m came, disappears.
func (a *Adapter) keepExpiry(ctx context.Context, self string, msg domain.Message, m *signalv1.Message) {
	if a.cache == nil || m.GetTimer() == 0 {
		return
	}
	var starts time.Time
	switch {
	case m.GetTimerStarted() > 0:
		starts = time.UnixMilli(int64(m.GetTimerStarted())) //nolint:gosec // a Signal timestamp fits
	case m.GetSender() == self:
		starts = msg.Timestamp
	}
	if err := a.cache.SetExpiry(ctx, msg.RoomID, msg.ID, time.Duration(m.GetTimer())*time.Second, starts); err != nil {
		a.log.Warn("keep when a message disappears failed", "room", msg.RoomID, "err", err)
	}
}

// keepExpiring deletes disappearing messages as they come due, until ctx ends.
func (a *Adapter) keepExpiring(ctx context.Context) {
	if a.cache == nil {
		return
	}
	expiry.Sweeper{
		Cache: a.cache, Rooms: signalRooms, Keep: a.keepsDeleted, Log: a.log,
		Gone: func(stub domain.Message) { emit(a, a.messages, stub) },
		Changed: func(room domain.RoomID) {
			if a.onChanged != nil {
				a.onChanged(room)
			}
			a.recount(ctx, room)
		},
	}.Run(ctx)
}
