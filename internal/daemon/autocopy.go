package daemon

import (
	"context"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// captureCode publishes a verification code out of msg, for kith-notify to copy, when
// the [autocopy] rules say to. Off by default: a self-rewriting clipboard is remotely
// triggerable. Ignores do-not-disturb, and the copy is always announced: a silent
// clipboard change is worse.
func (n *Notifications) captureCode(ctx context.Context, msg domain.Message, mine bool) {
	n.mu.Lock()
	rules := n.autocopy
	n.mu.Unlock()
	if !rules.Enabled || msg.IsUpdate() {
		return
	}
	facts := n.scope.Facts(ctx, msg.RoomID)
	code, ok := rules.Decide(msg, domain.Arrival{
		CaughtUp: n.caughtUp(msg.RoomID),
		Mine:     mine,
		Room:     facts,
	})
	if !ok {
		return
	}
	n.notify(codeAlert(msg, facts, code), code.Value, n.topics(ctx, facts, msg))
}

// codeAlert announces a captured code, with the code in the body so the user can check
// the clipboard or copy it when the copy failed; kith-notify adds which.
func codeAlert(msg domain.Message, room domain.RoomFacts, code domain.Code) notify.Notification {
	label := "code"
	if code.Label != "" {
		label = code.Label
	}
	return notify.Notification{
		Sender:   label,
		SenderID: msg.Sender,
		Room:     room.Name,
		Body:     code.Value,
		Protocol: domain.ProtocolOf(msg.Sender).String(),
		Sent:     msg.Timestamp,
	}
}
