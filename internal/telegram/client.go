package telegram

import (
	"runtime"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
)

// dialer makes a client of Telegram through an app, its session in a storage.
type dialer func(App, session.Storage) *telegram.Client

// newClient is a client of Telegram's own servers through app, its session in
// storage. It reconnects by itself while it runs.
func (a *Adapter) newClient(app App, storage session.Storage) *telegram.Client {
	return telegram.NewClient(app.ID, app.Hash, telegram.Options{
		SessionStorage: storage,
		Logger:         gotdLog{log: a.log},
		// What Telegram lists under the person's active sessions.
		Device: telegram.DeviceConfig{DeviceModel: "kith", SystemVersion: runtime.GOOS, AppVersion: "kith"},
	})
}
