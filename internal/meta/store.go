package meta

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/EugeneShtoka/kith/internal/logging"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// Store keeps the devices that read Messenger's end-to-end encrypted chats: their keys
// and Signal sessions, as whatsmeow keeps a linked WhatsApp device's. Anyone holding
// it can read those chats, so it is encrypted at rest with a key the OS keyring holds;
// only the daemon opens it. It is Messenger's own file, not WhatsApp's.
type Store struct {
	db        *sql.DB
	container *sqlstore.Container
}

// OpenStore opens the store at path, encrypted with key, creating and upgrading it;
// a plain store from before is encrypted first (sealeddb.OpenMigrating).
func OpenStore(ctx context.Context, path string, key []byte, log *slog.Logger) (*Store, error) {
	db, err := sealeddb.OpenMigrating(ctx, path, key)
	if err != nil {
		return nil, fmt.Errorf("meta: open the store: %w", err)
	}
	// One connection: whatsmeow's advice for SQLite.
	db.SetMaxOpenConns(1)
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	container := sqlstore.NewWithDB(db, sealeddb.Dialect, waLog.Zerolog(logging.Zerolog(log.With("network", "messenger", "component", "store"), zerolog.WarnLevel)))
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("meta: migrate the store: %w", err)
	}
	return &Store{db: db, container: container}, nil
}

// Close closes the store.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("meta: close the store: %w", err)
	}
	return nil
}
