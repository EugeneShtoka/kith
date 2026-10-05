package telegram

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

// What an account hears while connected comes through gotd's updates manager: it
// keeps the account's position (in the store) and, when a gap opens — downtime, a lost
// connection — asks Telegram for the difference, so a message sent while kith was not
// listening still arrives, in order.

// live is one connection's updates: the manager, and whom it hears them for.
type live struct {
	a       *Adapter
	account Account
	manager *updates.Manager
	// self is the account's own user ID, once Telegram said it.
	self atomic.Int64
}

// liveFor is a connection's updates for account, handing new messages to arrived.
func (a *Adapter) liveFor(account Account) *live {
	l := &live{a: a, account: account}
	dispatcher := tg.NewUpdateDispatcher()
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		return l.message(ctx, e, u.Message)
	})
	dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		return l.message(ctx, e, u.Message)
	})
	cfg := updates.Config{Handler: dispatcher, Logger: gotdLog{log: a.log}}
	if a.store != nil { // else gotd keeps the position in memory, for this run
		cfg.Storage, cfg.AccessHasher, cfg.UserAccessHasher = a.store, a.store, a.store
	}
	l.manager = updates.New(cfg)
	return l
}

// errNotYet is an update before the connection said whom it is for.
var errNotYet = errors.New("telegram: an update before the account was known")

// message is a new message heard.
func (l *live) message(ctx context.Context, e tg.Entities, m tg.MessageClass) error {
	self := l.self.Load()
	if self == 0 {
		return errNotYet
	}
	msg, ok := incoming(self, m, peer.EntitiesFromUpdate(e))
	if !ok {
		return nil
	}
	return l.a.arrived(ctx, l.account, self, msg)
}

// run hears self's updates over client until ctx ends: from the position last saved,
// which this connection may save again (Store.release). started is told once they
// flow: the account is connected then, not before.
func (l *live) run(ctx context.Context, client *telegram.Client, self int64, started func()) error {
	l.self.Store(self)
	if l.a.store != nil {
		l.a.store.release(self)
	}
	return l.manager.Run(ctx, client.API(), self, updates.AuthOptions{ //nolint:wrapcheck // read by kind in connectOnce
		OnStart: func(context.Context) { started() },
	})
}
