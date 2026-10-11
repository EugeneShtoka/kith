// Package whatsapp connects to WhatsApp through whatsmeow, each configured account as
// a linked device, writing into the shared cache. It is the only package that imports
// whatsmeow.
package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sync"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	// The pure-Go SQLite driver the cache uses; whatsmeow supports its dialect.
	_ "modernc.org/sqlite"
)

// Store is the WhatsApp session store: device keys, contacts and Signal sessions of
// every linked account. Anyone holding it can act as those accounts; only the daemon
// opens it.
type Store struct {
	db        *sql.DB
	container *sqlstore.Container
}

// askFullHistory requests full history rather than WhatsApp Web's recent slice. A
// phone sends history only at linking, and whatsmeow's device properties are global,
// so this is set once before any linking.
var askFullHistory = sync.OnceFunc(func() {
	store.DeviceProps.RequireFullSync = new(true)
})

// OpenStore opens the store at path, creating and migrating it.
func OpenStore(ctx context.Context, path string, log waLog.Logger) (*Store, error) {
	askFullHistory()
	dsn := (&url.URL{Scheme: "file", Opaque: path, RawQuery: url.Values{
		"_pragma": {"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)"},
	}.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: open the store: %w", err)
	}
	// One connection: whatsmeow's advice for SQLite.
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

// device is the account's linked device, nil when not linked.
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

// LIDNumbers maps every known LID's digits to its phone number's, across all linked
// accounts.
func (s *Store) LIDNumbers(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT lid, pn FROM whatsmeow_lid_map")
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read the LIDs' numbers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var lid, pn string
		if err := rows.Scan(&lid, &pn); err != nil {
			return nil, fmt.Errorf("whatsapp: read a LID's number: %w", err)
		}
		out[lid] = pn
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("whatsapp: read the LIDs' numbers: %w", err)
	}
	return out, nil
}
