package daemon

import (
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// An account that goes down stops its messages without a word: Google refusing a
// pairing's cookies, a phone unlinking kith or a session failing at start all leave
// the rooms as they were. So an account that had a session (connecting or online)
// going logged out or failed raises one popup with why, once per fall: retries stay
// connecting and say nothing, and an account never logged in is not down.

// AccountChanged is s reported by its network after was (State.Report).
func (n *Notifications) AccountChanged(s domain.AccountStatus, was domain.AccountPhase) {
	up := was == domain.AccountConnecting || was == domain.AccountOnline
	down := s.Phase == domain.AccountLoggedOut || s.Phase == domain.AccountFailed
	if !up || !down {
		return
	}
	body := s.Detail
	if body == "" {
		body = "not connected"
	}
	name := s.Network.String() + " " + s.Account
	n.notify(notify.Notification{
		Sender:   name,
		Room:     name,
		Body:     body,
		Protocol: s.Network.String(),
		Sent:     time.Now(),
		Replaces: "account:" + name,
	})
}
