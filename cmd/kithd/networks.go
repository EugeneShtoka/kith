package main

import (
	"context"
	"log/slog"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/route"
	"github.com/EugeneShtoka/kith/internal/session"
	"github.com/EugeneShtoka/kith/internal/setup"
	"github.com/EugeneShtoka/kith/internal/slack"
	"github.com/EugeneShtoka/kith/internal/telegram"
	"github.com/EugeneShtoka/kith/internal/whatsapp"
)

// network is an adapter as kithd runs it, whatever its network: it reads its own part
// of the config, says where each of its accounts is, and names the accounts it will
// resume. kithd builds every network it links and loops over them; what a network can
// do beyond this the router asks it for (capabilities.go).
type network interface {
	route.Adapter
	Network() domain.Protocol
	// UseConfig takes the config, before Start and each time it is re-read: the
	// network's accounts (one added is connected if it has a session, one removed is
	// let go) and the settings it follows.
	UseConfig(ctx context.Context, cfg config.Config)
	// OnStatus sets who hears an account's state change. Set before Start.
	OnStatus(changed func(domain.AccountStatus))
	// SavedSessions is the accounts with a session kept, by name: Start connects them,
	// and the daemon is ready once each has said where it is.
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
	// roomsRewriter rewrites its rooms in the cache itself (its own listing).
	roomsRewriter interface{ OnRoomsChanged(changed func()) }
	// configChecker says whether its part of the config is right, before kithd runs
	// it (at start, at each re-read, and when a client asks before saving).
	configChecker interface {
		CheckConfig(ctx context.Context, cfg config.Config) error
	}
	// roomsStaler learns of room changes the cache does not have until it is
	// refreshed (Matrix's sync).
	roomsStaler interface{ OnRoomsStale(stale func()) }
)

// checkConfig refuses a config kithd would not run: one setup refuses, or one a
// network refuses its part of.
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

// openNetworks builds every network kithd links, over one cache and this instance's
// keyring: Matrix when the config names its account (starting from saved), the rest
// always, each running whatever accounts the config gives it. whatsappStore is
// WhatsApp's session store, for Close; nil when it would not open (WhatsApp is then
// left out, logged, rather than the daemon down with it).
func openNetworks(
	ctx context.Context, cache *db.Cache, log *slog.Logger, cfg config.Config, storage domain.Storage, saved domain.Session,
) (networks []network, matrix *matrixAdapter, whatsappStore *whatsapp.Store) {
	if cfg.HasMatrix() {
		matrix = newMatrixAdapter(cache, log, matrixAccount{
			homeserver: cfg.Homeserver, user: cfg.User, allowTokenFile: cfg.AllowTokenFile,
			crypto: cryptoPlace{path: storage.CryptoPath(), keys: session.StoreFor(storage, cfg.User)},
		}, saved)
		networks = append(networks, matrix)
	}
	if store, err := whatsapp.OpenStore(ctx, storage.WhatsAppPath(), whatsapp.NewStoreLogger(log)); err != nil {
		log.Error("WhatsApp is off: its store will not open", "path", storage.WhatsAppPath(), "err", err)
	} else {
		whatsappStore = store
		networks = append(networks, whatsapp.New(cache, store, nil, log))
	}
	secrets := keyringSecrets{service: storage.KeyringService, scope: storage.Instance}
	networks = append(networks, slack.New(cache, secrets, nil, log), telegram.New(cache, secrets, nil, log))
	for _, n := range networks {
		n.UseConfig(ctx, cfg)
	}
	return networks, matrix, whatsappStore
}
