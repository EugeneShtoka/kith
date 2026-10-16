package meta

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	// The pure-Go SQLite driver the cache uses; whatsmeow supports its dialect.
	_ "modernc.org/sqlite"

	"github.com/EugeneShtoka/kith/internal/logging"
)

// Store keeps the devices that read Messenger's end-to-end encrypted chats: their keys
// and Signal sessions, as whatsmeow keeps a linked WhatsApp device's. Anyone holding
// it can read those chats; only the daemon opens it. It is Messenger's own file, not
// WhatsApp's.
type Store struct {
	db        *sql.DB
	container *sqlstore.Container
}

// OpenStore opens the store at path, creating and migrating it.
func OpenStore(ctx context.Context, path string, log *slog.Logger) (*Store, error) {
	dsn := (&url.URL{Scheme: "file", Opaque: path, RawQuery: url.Values{
		"_pragma": {"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)"},
	}.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("meta: open the store: %w", err)
	}
	// One connection: whatsmeow's advice for SQLite.
	db.SetMaxOpenConns(1)
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	container := sqlstore.NewWithDB(db, "sqlite3", waLog.Zerolog(logging.Zerolog(log.With("network", "messenger", "component", "store"), zerolog.WarnLevel)))
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
