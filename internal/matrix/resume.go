package matrix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// SavedSession reads the session saved by `kith login`: zero when there is none.
func SavedSession(cfg config.Config, keys session.Store) (domain.Session, error) {
	saved, found, err := session.Load(keys, cfg.AllowTokenFile)
	if err != nil {
		return domain.Session{}, fmt.Errorf("load session: %w", err)
	}
	if !found {
		return domain.Session{}, nil
	}
	return saved, nil
}

// resumeSession restores a session. prepare is the homeserver-dependent startup left
// to do, nil when the homeserver answered. A rejected session is
// api.ErrSessionRejected, and leaves the crypto store unopened.
func resumeSession(
	ctx context.Context, log *slog.Logger, backend *InProc, crypto cryptoPlace, saved domain.Session,
) (prepare func(context.Context) error, err error) {
	rerr := backend.Resume(ctx, saved)
	if rerr == nil || errors.Is(rerr, api.ErrUnreachable) {
		// Before anything reaches the adapter, and offline too: the state store must be
		// in place before any RPC runs (see matrix.InProc.OpenCryptoStore).
		openCryptoStore(ctx, log, backend, crypto.Path)
	}
	switch {
	case rerr == nil:
		enableEncryption(ctx, log, backend, crypto.Keys)
		return nil, nil
	case errors.Is(rerr, api.ErrUnreachable):
		// Serve the on-disk cache offline and connect once the homeserver is back.
		log.Warn("cannot reach the homeserver; serving cached history and connecting when it is back", "err", rerr)
		return func(ctx context.Context) error {
			if werr := waitForHomeserver(ctx, backend); werr != nil {
				return werr
			}
			log.Info("homeserver reachable again; syncing")
			enableEncryption(ctx, log, backend, crypto.Keys)
			return nil
		}, nil
	default:
		return nil, fmt.Errorf("saved session for %s is unusable (run `kith login` again): %w", saved.UserID, rerr)
	}
}

// waitForHomeserver polls until the homeserver answers, ctx ends, or the session is
// rejected. Backoff is capped at 30s so a laptop waking from suspend reconnects quickly.
func waitForHomeserver(ctx context.Context, backend *InProc) error {
	const (
		first = 2 * time.Second
		most  = 30 * time.Second
	)
	for wait := first; ; {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the homeserver: %w", ctx.Err())
		case <-time.After(wait):
		}
		switch err := backend.Reachable(ctx); {
		case err == nil:
			return nil
		case errors.Is(err, api.ErrUnreachable):
		default:
			return fmt.Errorf("saved session is unusable (stop kithd, run `kith login` again): %w", err)
		}
		if wait *= 2; wait > most {
			wait = most
		}
	}
}

// openCryptoStore opens the crypto/state store, logging a failure: encryption then
// stays off and encrypted rooms refuse sends.
func openCryptoStore(ctx context.Context, log *slog.Logger, backend *InProc, dbPath string) {
	if err := backend.OpenCryptoStore(ctx, dbPath); err != nil {
		log.Error("encryption disabled", "err", err)
	}
}

// enableEncryption turns on E2EE. Failures only warn: unencrypted rooms still work.
func enableEncryption(ctx context.Context, log *slog.Logger, backend *InProc, keys session.Store) {
	pickleKey, err := session.LoadOrCreatePickleKey(keys)
	if err != nil {
		log.Error("encryption disabled", "err", err)
		return
	}
	if err := backend.EnableEncryption(ctx, pickleKey); err != nil {
		log.Error("encryption disabled", "err", err)
		return
	}
	if verr := backend.VerificationUnavailable(); verr != nil {
		log.Warn("device verification unavailable", "err", verr)
	}
}
