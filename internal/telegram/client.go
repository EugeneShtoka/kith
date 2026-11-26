package telegram

import (
	"runtime"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// dialer builds a client for app with session storage, an update handler (nil: none)
// and who hears its connection's state (nil: no one).
type dialer func(App, session.Storage, telegram.UpdateHandler, func(telegram.ConnectionState)) *telegram.Client

// newClient is a dialer for Telegram's production servers; the client reconnects by
// itself while running.
func (a *Adapter) newClient(app App, storage session.Storage, updates telegram.UpdateHandler, states func(telegram.ConnectionState)) *telegram.Client {
	return telegram.NewClient(app.ID, app.Hash, telegram.Options{
		SessionStorage:    storage,
		UpdateHandler:     updates,
		OnConnectionState: states,
		Logger:            gotdLog{log: a.log},
		Middlewares:       []telegram.Middleware{waitOutFloods()},
		// Shown in the person's active sessions list.
		Device: telegram.DeviceConfig{DeviceModel: "kith", SystemVersion: runtime.GOOS, AppVersion: "kith"},
	})
}

// pinged hears account's connection being remade under a running client: answering
// once ready again, silent from the moment it died.
func (a *Adapter) pinged(account Account) func(telegram.ConnectionState) {
	return func(s telegram.ConnectionState) {
		switch s {
		case telegram.ConnectionStateReady:
			a.beat(account, domain.HeardAnswering)
		case telegram.ConnectionStateDisconnected:
			a.beat(account, domain.HeardSilent)
		case telegram.ConnectionStateConnecting:
		}
	}
}
