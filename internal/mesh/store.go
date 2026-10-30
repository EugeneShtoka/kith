// Package mesh is a replica's store: its ops, the entries they wrote and its version
// vector, in a SQLite file of its own sealed under the cache's key (internal/sealeddb).
// Not the cache: a rebuild must not lose a replica's writes or unpair anyone. The data
// model, the merge and the roster's rules are internal/mesh/crdt's.
package mesh

import (
	"cmp"
	"context"
	"crypto"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// schemaVersion is the store's layout (PRAGMA user_version). A store of a later one was
// written by a newer kith, and is refused rather than read or rebuilt: it holds writes
// and pairings nothing else has.
const schemaVersion = 1

// ErrNewer is a store a newer kith wrote.
var ErrNewer = errors.New("mesh: the store was written by a newer kith; run that kith, or restore the store it replaced")

// schema is the layout of schemaVersion. A replica is a device ID, kept once in
// replicas and named by its index elsewhere.
const schema = `
CREATE TABLE replicas (
	idx INTEGER PRIMARY KEY,
	id  BLOB NOT NULL UNIQUE
);
CREATE TABLE ops (
	origin INTEGER NOT NULL REFERENCES replicas(idx),
	seq    INTEGER NOT NULL,
	clock  INTEGER NOT NULL,
	body   BLOB NOT NULL,
	sig    BLOB NOT NULL,
	PRIMARY KEY (origin, seq)
) WITHOUT ROWID;
CREATE TABLE entries (
	key    TEXT PRIMARY KEY,
	value  BLOB NOT NULL,
	origin INTEGER NOT NULL REFERENCES replicas(idx),
	seq    INTEGER NOT NULL,
	clock  INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE vv (
	origin INTEGER PRIMARY KEY REFERENCES replicas(idx),
	seq    INTEGER NOT NULL,
	hash   BLOB NOT NULL
);
`

// Store is one replica's store, its state held in memory as last committed.
type Store struct {
	db *sql.DB

	mu    sync.Mutex
	state crdt.State
	clock crdt.Clock // the latest op's
}

// Open opens (creating) the store at path, sealed under key. A key that does not open
// it is sealeddb.ErrWrongKey, and a store of a newer kith ErrNewer.
func Open(ctx context.Context, path string, key []byte) (*Store, error) {
	db, err := sealeddb.OpenWith(ctx, path, key, sealeddb.Setup{TxLock: "immediate"})
	if err != nil {
		return nil, fmt.Errorf("mesh: open %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.load(ctx); err != nil {
		_ = db.Close() // the open failed; its error is the one to report
		return nil, fmt.Errorf("mesh: open %s: %w", path, err)
	}
	return s, nil
}

// Close closes the store.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("mesh: close: %w", err)
	}
	return nil
}

// load lays out a new store, or reads what one holds.
func (s *Store) load(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read the version: %w", err)
	}
	switch {
	case version > schemaVersion:
		return fmt.Errorf("%w (layout %d, this kith reads %d)", ErrNewer, version, schemaVersion)
	case version == 0:
		return s.create(ctx)
	}
	state := crdt.State{Entries: map[string]crdt.Entry{}, Vector: crdt.Vector{}}
	rows, err := s.db.QueryContext(ctx, `SELECT e.key, e.value, r.id, e.seq, e.clock FROM entries e JOIN replicas r ON r.idx = e.origin`)
	if err != nil {
		return fmt.Errorf("read the entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		var e crdt.Entry
		var origin []byte
		if err := rows.Scan(&key, &e.Value, &origin, &e.Dot.Seq, &e.Clock); err != nil {
			return fmt.Errorf("read the entries: %w", err)
		}
		if len(origin) != len(e.Dot.Origin) {
			return errors.New("read the entries: a row does not read")
		}
		e.Dot.Origin = devicekey.ID(origin)
		state.Entries[key] = e
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the entries: %w", err)
	}
	if err := s.loadVector(ctx, state.Vector); err != nil {
		return err
	}
	if _, err := state.Roster(); err != nil {
		return fmt.Errorf("read the roster: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT coalesce(max(clock), 0) FROM ops").Scan(&s.clock); err != nil {
		return fmt.Errorf("read the clock: %w", err)
	}
	s.state = state
	return nil
}

// loadVector reads the version vector into v.
func (s *Store) loadVector(ctx context.Context, v crdt.Vector) error {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, v.seq, v.hash FROM vv v JOIN replicas r ON r.idx = v.origin`)
	if err != nil {
		return fmt.Errorf("read the version vector: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var origin, hash []byte
		var h crdt.Head
		if err := rows.Scan(&origin, &h.Seq, &hash); err != nil {
			return fmt.Errorf("read the version vector: %w", err)
		}
		if len(hash) != len(h.Hash) || len(origin) != len(devicekey.ID{}) {
			return errors.New("read the version vector: a row does not read")
		}
		h.Hash = [len(h.Hash)]byte(hash)
		v[devicekey.ID(origin)] = h
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the version vector: %w", err)
	}
	return nil
}

// create lays out an empty store.
func (s *Store) create(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lay out the store: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("lay out the store: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("lay out the store: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lay out the store: %w", err)
	}
	s.state = crdt.State{}
	return nil
}

// Roster is every member of the roster, this bridge among them, oldest first, and the
// IDs of those removed.
func (s *Store) Roster() (members []domain.Device, removed []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roster, err := s.state.Roster()
	if err != nil {
		return nil, nil, fmt.Errorf("mesh: %w", err)
	}
	for id, m := range roster {
		if m.Removed {
			removed = append(removed, id.String())
			continue
		}
		members = append(members, domain.Device{
			Name: m.Name, ID: id.String(), Role: domain.DeviceRole(m.Role), AddedAt: m.AddedAt, AddedBy: m.AddedBy,
		})
	}
	slices.SortFunc(members, func(a, b domain.Device) int {
		return cmp.Or(a.AddedAt.Compare(b.AddedAt), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	slices.Sort(removed)
	return members, removed, nil
}

// ChangeRoster writes each device of set, and the removal of each member remove names,
// as one op signed by signer: a bridge of the roster, or on an empty store the genesis,
// whose set names the signer itself. The signer's own entry carries its public key.
func (s *Store) ChangeRoster(ctx context.Context, signer crypto.Signer, set []domain.Device, remove []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	origin, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	writes, err := s.rosterWrites(signer, origin, set, remove)
	if err != nil {
		return err
	}
	op := crdt.Op{
		Origin: origin, Seq: s.state.Vector[origin].Seq + 1, Clock: s.clock.Next(time.Now()),
		Context: s.state.Vector.Context(), Writes: writes,
	}
	if err = op.Sign(signer); err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	next, err := s.state.Apply(op)
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	if err := s.commit(ctx, op, next); err != nil {
		return err
	}
	s.state, s.clock = next, op.Clock
	return nil
}

// errNotAMember is a removal of a device the roster does not list.
var errNotAMember = errors.New("mesh: no member has that ID")

// rosterWrites is set and the removals as roster writes.
func (s *Store) rosterWrites(signer crypto.Signer, origin devicekey.ID, set []domain.Device, remove []string) ([]crdt.Write, error) {
	roster, err := s.state.Roster()
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	members := make([]crdt.Member, 0, len(set)+len(remove))
	ids := make([]devicekey.ID, 0, len(set)+len(remove))
	for _, d := range set {
		id, err := devicekey.ParseID(d.ID)
		if err != nil {
			return nil, fmt.Errorf("mesh: %w", err)
		}
		m := crdt.Member{Name: d.Name, Role: crdt.Role(d.Role), AddedAt: d.AddedAt, AddedBy: d.AddedBy}
		if id == origin {
			if m.Key, err = x509.MarshalPKIXPublicKey(signer.Public()); err != nil {
				return nil, fmt.Errorf("mesh: %w", err)
			}
		}
		members, ids = append(members, m), append(ids, id)
	}
	for _, text := range remove {
		id, err := devicekey.ParseID(text)
		if err != nil {
			return nil, fmt.Errorf("mesh: %w", err)
		}
		m, ok := roster[id]
		if !ok || m.Removed {
			return nil, fmt.Errorf("%w: %s", errNotAMember, text)
		}
		m.Removed = true
		members, ids = append(members, m), append(ids, id)
	}
	writes := make([]crdt.Write, 0, len(members))
	for i, m := range members {
		value, err := m.Encode()
		if err != nil {
			return nil, fmt.Errorf("mesh: %w", err)
		}
		writes = append(writes, crdt.Write{Key: crdt.RosterKey(ids[i]), Value: value})
	}
	return writes, nil
}

// commit keeps op and what it changed of next: its entries, and its origin's head.
func (s *Store) commit(ctx context.Context, op crdt.Op, next crdt.State) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mesh: keep an op: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	origin, err := replica(ctx, tx, op.Origin)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ops (origin, seq, clock, body, sig) VALUES (?, ?, ?, ?, ?)`,
		origin, op.Seq, op.Clock, op.Body(), op.Sig); err != nil {
		return fmt.Errorf("mesh: keep an op: %w", err)
	}
	for _, w := range op.Writes {
		e := next.Entries[w.Key]
		by, err := replica(ctx, tx, e.Dot.Origin)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entries (key, value, origin, seq, clock) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, origin = excluded.origin, seq = excluded.seq, clock = excluded.clock`,
			w.Key, e.Value, by, e.Dot.Seq, e.Clock); err != nil {
			return fmt.Errorf("mesh: keep an op: %w", err)
		}
	}
	head := next.Vector[op.Origin]
	if _, err := tx.ExecContext(ctx, `INSERT INTO vv (origin, seq, hash) VALUES (?, ?, ?)
		ON CONFLICT (origin) DO UPDATE SET seq = excluded.seq, hash = excluded.hash`,
		origin, head.Seq, head.Hash[:]); err != nil {
		return fmt.Errorf("mesh: keep an op: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mesh: keep an op: %w", err)
	}
	return nil
}

// replica is id's index in replicas, given one on first use.
func replica(ctx context.Context, tx *sql.Tx, id devicekey.ID) (int64, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO replicas (id) VALUES (?) ON CONFLICT (id) DO NOTHING`, id[:]); err != nil {
		return 0, fmt.Errorf("mesh: name a replica: %w", err)
	}
	var idx int64
	if err := tx.QueryRowContext(ctx, `SELECT idx FROM replicas WHERE id = ?`, id[:]).Scan(&idx); err != nil {
		return 0, fmt.Errorf("mesh: name a replica: %w", err)
	}
	return idx, nil
}
