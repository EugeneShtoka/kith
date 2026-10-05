package telegram

import (
	"runtime"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
)

// dialer makes a client of Telegram through an app, its session in a storage, its
// updates handed to a handler (nil: none heard).
type dialer func(App, session.Storage, telegram.UpdateHandler) *telegram.Client

// newClient is a client of Telegram's own servers through app, its session in
// storage, its updates handed to updates. It reconnects by itself while it runs.
func (a *Adapter) newClient(app App, storage session.Storage, updates telegram.UpdateHandler) *telegram.Client {
	return telegram.NewClient(app.ID, app.Hash, telegram.Options{
		SessionStorage: storage,
		UpdateHandler:  updates,
		Logger:         gotdLog{log: a.log},
		// What Telegram lists under the person's active sessions.
		Device: telegram.DeviceConfig{DeviceModel: "kith", SystemVersion: runtime.GOOS, AppVersion: "kith"},
	})
}
