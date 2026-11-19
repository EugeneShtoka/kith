package daemon

import (
	"time"

	"github.com/EugeneShtoka/kith/internal/notify"
)

// kithd's daily backup has no room or account a profile holds, so what it has to say
// (backups off for want of a key, a backup that failed) goes to every profile, as an
// account's fall does (AccountChanged). Each popup replaces the last. The config the
// mesh shares is no profile's either.

// BackupsSay raises a popup about the daily backups saying body.
func (n *Notifications) BackupsSay(body string) {
	n.notify(notify.Notification{
		Sender:   "kith backup",
		Room:     "kith backup",
		Body:     body,
		Sent:     time.Now(),
		Replaces: "kith:backups",
	}, "", n.everyTopic())
}

// ConfigSays raises a popup about the config the mesh's bridges share saying body: one
// set aside as this bridge took the mesh's, or a merged one that does not check here.
func (n *Notifications) ConfigSays(body string) {
	n.notify(notify.Notification{
		Sender:   "kith config",
		Room:     "kith config",
		Body:     body,
		Sent:     time.Now(),
		Replaces: "kith:config",
	}, "", n.everyTopic())
}
