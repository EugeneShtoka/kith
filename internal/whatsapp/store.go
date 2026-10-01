// Package whatsapp is kith's own link to WhatsApp, through whatsmeow: each configured
// account is one of its phone's linked devices. It writes what it sees into the cache
// every network shares, so search, drafts, the assistant and notifications work for it
// as they do for Matrix. It is the only package that imports whatsmeow.
package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	// The pure-Go SQLite driver the cache already uses; whatsmeow speaks its dialect.
	_ "modernc.org/sqlite"
)

// Store is the WhatsApp session store: every linked account's device keys, contacts
// and Signal sessions, in one file. It is the session itself — whoever has it can
// read and send as those accounts — so only the daemon opens it.
type Store struct {
	db        *sql.DB
	container *sqlstore.Container
}

// OpenStore opens (creating and migrating) the store at path.
func OpenStore(ctx context.Context, path string, log waLog.Logger) (*Store, error) {
	dsn := (&url.URL{Scheme: "file", Opaque: path, RawQuery: url.Values{
		"_pragma": {"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)"},
	}.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: open the store: %w", err)
	}
	// One writer: whatsmeow's own advice for SQLite.
	db.SetMaxOpenConns(1)
	container := sqlstore.NewWithDB(db, "sqlite3", log)
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("whatsapp: migrate the store: %w", err)
	}
	return &Store{db: db, container: container}, nil
}

// Close closes the store.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("whatsapp: close the store: %w", err)
	}
	return nil
}

// device is the linked device for the account with these digits, or nil when it is
// not linked.
func (s *Store) device(ctx context.Context, digits string) (*store.Device, error) {
	devices, err := s.container.GetAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: list linked devices: %w", err)
	}
	for _, d := range devices {
		if d.ID != nil && d.ID.User == digits {
			return d, nil
		}
	}
	return nil, nil
}
