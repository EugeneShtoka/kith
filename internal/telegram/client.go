package telegram

import (
	"runtime"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
)

// dialer builds a client for app with session storage and an update handler (nil:
// none).
type dialer func(App, session.Storage, telegram.UpdateHandler) *telegram.Client

// newClient is a dialer for Telegram's production servers; the client reconnects by
// itself while running.
func (a *Adapter) newClient(app App, storage session.Storage, updates telegram.UpdateHandler) *telegram.Client {
	return telegram.NewClient(app.ID, app.Hash, telegram.Options{
		SessionStorage: storage,
		UpdateHandler:  updates,
		Logger:         gotdLog{log: a.log},
		Middlewares:    []telegram.Middleware{waitOutFloods()},
		// Shown in the person's active sessions list.
		Device: telegram.DeviceConfig{DeviceModel: "kith", SystemVersion: runtime.GOOS, AppVersion: "kith"},
	})
}
