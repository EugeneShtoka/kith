package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/gmessages"
	"github.com/EugeneShtoka/kith/internal/linkedin"
	"github.com/EugeneShtoka/kith/internal/matrix"
	"github.com/EugeneShtoka/kith/internal/meta"
	"github.com/EugeneShtoka/kith/internal/route"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/slack"
	"github.com/EugeneShtoka/kith/internal/telegram"
	"github.com/EugeneShtoka/kith/internal/whatsapp"
)

// network is an adapter as kithd runs it: it reads its part of the config, reports each
// account's state, and names the accounts it will resume. Further capabilities are
// asked for by the router (capabilities.go).
type network interface {
	route.Adapter
	Network() domain.Protocol
	// UseConfig takes the config before Start and on each re-read: added accounts with a
	// session connect, removed ones are let go.
	UseConfig(ctx context.Context, cfg config.Config)
	// OnStatus sets who hears an account's state change. Set before Start.
	OnStatus(changed func(domain.AccountStatus))
	// SavedSessions is the accounts with a kept session: Start connects them, and the
	// daemon is ready once each has reported.
	SavedSessions(ctx context.Context) ([]string, error)
	// Every network leads its own login.
	api.LoginLeader
}

// Hooks a network may have.
type (
	// cacheWriter writes messages into the cache, which the service hears of.
	cacheWriter interface {
		OnCached(cached func(domain.Message), changed func(domain.RoomID))
	}
	// roomsRewriter rewrites its rooms in the cache itself.
	roomsRewriter interface{ OnRoomsChanged(changed func()) }
	// configChecker validates its part of the config before kithd runs it (at start, on
	// re-read, and when a client asks before saving).
	configChecker interface {
		CheckConfig(ctx context.Context, cfg config.Config) error
	}
	// roomsStaler learns of room changes the cache lacks until refreshed (Matrix's sync).
	roomsStaler interface{ OnRoomsStale(stale func()) }
)

// checkConfig refuses a config kithd would not run: one setup or any network refuses.
func (s served) checkConfig(ctx context.Context, cfg config.Config) error {
	if err := setup.Validate(cfg); err != nil {
		return err
	}
	for _, n := range s.networks {
		if c, ok := n.(configChecker); ok {
			if err := c.CheckConfig(ctx, cfg); err != nil {
				return err //nolint:wrapcheck // names the record itself
			}
		}
	}
	return nil
}

// openMessengerStore opens Messenger's store with its key from the keyring.
func openMessengerStore(ctx context.Context, storage domain.Storage, log *slog.Logger) (*meta.Store, error) {
	key, err := session.LoadOrCreateStoreKey(storage.KeyringService, storage.Instance, "messenger")
	if err != nil {
		return nil, fmt.Errorf("its key cannot be had from the keyring: %w", err)
	}
	return meta.OpenStore(ctx, storage.MessengerPath(), key, log) //nolint:wrapcheck // logged with what it was for
}

// store is a network's own store, closed with the backend.
type store struct {
	name string
	io.Closer
}

// openNetworks builds every network kithd links over one cache and this instance's
// keyring; stores are the networks' own, for Close. A WhatsApp session store that will
// not open leaves WhatsApp out (logged) instead of taking the daemon down; without
// Telegram's store, update positions are kept in memory.
func openNetworks(
	ctx context.Context, cache *db.Cache, log *slog.Logger, cfg config.Config, storage domain.Storage,
) (networks []network, mx *matrix.Adapter, stores []store) {
	mx = matrix.NewAdapter(cache, log, matrix.Place{
		CryptoPath: storage.CryptoPath(),
		Keys:       func(user string) session.Store { return session.StoreFor(storage, user) },
	})
	networks = append(networks, mx)
	if key, err := session.LoadOrCreateStoreKey(storage.KeyringService, storage.Instance, "whatsapp"); err != nil {
		log.Error("WhatsApp is off: its store's key cannot be had from the keyring", "err", err)
	} else if wa, err := whatsapp.OpenStore(ctx, storage.WhatsAppPath(), key, whatsapp.NewStoreLogger(log)); err != nil {
		log.Error("WhatsApp is off: its store will not open", "path", storage.WhatsAppPath(), "err", err)
	} else {
		stores = append(stores, store{name: "WhatsApp", Closer: wa})
		networks = append(networks, whatsapp.New(cache, wa, nil, log))
	}
	secrets := keyringSecrets{service: storage.KeyringService, scope: storage.Instance}
	tg, err := telegram.OpenStore(ctx, storage.TelegramPath())
	if err != nil {
		log.Warn("Telegram's store will not open; its updates positions are kept in memory", "path", storage.TelegramPath(), "err", err)
		tg = nil
	} else {
		stores = append(stores, store{name: "Telegram", Closer: tg})
	}
	msgr, err := openMessengerStore(ctx, storage, log)
	if err != nil {
		log.Error("Messenger's encrypted chats are off: its store will not open", "path", storage.MessengerPath(), "err", err)
		msgr = nil
	} else {
		stores = append(stores, store{name: "Messenger", Closer: msgr})
	}
	networks = append(networks, slack.New(cache, secrets, nil, log), telegram.New(cache, secrets, tg, nil, log),
		meta.New(cache, secrets, msgr, nil, log), linkedin.New(cache, secrets, nil, log),
		gmessages.New(cache, secrets, nil, log))
	for _, n := range networks {
		n.UseConfig(ctx, cfg)
	}
	return networks, mx, stores
}
