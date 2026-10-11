package daemon

import (
	"context"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// captureCode copies a verification code out of msg when the [autocopy] rules say
// to. Off by default: a self-rewriting clipboard is remotely triggerable. Ignores
// do-not-disturb and always announces the copy: a silent clipboard change is worse.
func (n *Notifications) captureCode(ctx context.Context, msg domain.Message, mine bool) {
	n.mu.Lock()
	rules, clip := n.autocopy, n.clip
	n.mu.Unlock()
	if !rules.Enabled || msg.IsUpdate() {
		return
	}
	facts := n.scope.Facts(ctx, msg.RoomID)
	code, ok := rules.Decide(msg, domain.Arrival{
		CaughtUp: n.caughtUp(),
		Mine:     mine,
		Room:     facts,
	})
	if !ok {
		return
	}
	if err := clip.copy(ctx, code.Value); err != nil {
		// With no clipboard, the notification carrying the code is the delivery. The log
		// names the room, never the code.
		if n.log != nil {
			n.log.Log(ctx, levelFor(ctx), "auto-copy: copy to the clipboard failed", "room", msg.RoomID, "err", err)
		}
		n.notify(codeAlert(msg, facts, code, "could not copy it: "+err.Error()))
		return
	}
	n.notify(codeAlert(msg, facts, code, "copied to the clipboard"))
}

// codeAlert announces a captured code, with the code in the body so the user can check
// the clipboard or copy it when the copy failed.
func codeAlert(msg domain.Message, room domain.RoomFacts, code domain.Code, outcome string) notify.Notification {
	label := "code"
	if code.Label != "" {
		label = code.Label
	}
	return notify.Notification{
		Sender:   label,
		MXID:     msg.Sender,
		Room:     room.Name,
		Body:     code.Value + " — " + outcome,
		Protocol: domain.ProtocolOf(msg.Sender).String(),
		Sent:     msg.Timestamp,
	}
}
