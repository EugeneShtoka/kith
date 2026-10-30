// Package mesh is a replica's store: its ops, the entries they wrote and its version
// vector, in a SQLite file of its own sealed under the cache's key (internal/sealeddb).
// Not the cache: a rebuild must not lose a replica's writes or unpair anyone. The data
// model, the merge and the roster's rules are internal/mesh/crdt's; the sync session
// over a pair of frame streams is here (sync.go).
package mesh

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"os"
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
// replicas and named by its index elsewhere. A key holds one entry per concurrent
// write (crdt.State). The roster's ops are kept for good (roster = 1): membership as
// of any op is read from them, and a snapshot carries them; every other op leaves the
// log once past the horizon and acknowledged by every live peer. peers is each other
// replica's last reported vector, when it was heard and its clock's gap then; forks
// each origin that signed two ops under one dot, with both.
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
	roster INTEGER NOT NULL,
	PRIMARY KEY (origin, seq)
) WITHOUT ROWID;
CREATE TABLE entries (
	key    TEXT NOT NULL,
	origin INTEGER NOT NULL REFERENCES replicas(idx),
	seq    INTEGER NOT NULL,
	value  BLOB NOT NULL,
	clock  INTEGER NOT NULL,
	PRIMARY KEY (key, origin, seq)
) WITHOUT ROWID;
CREATE TABLE vv (
	origin INTEGER PRIMARY KEY REFERENCES replicas(idx),
	seq    INTEGER NOT NULL,
	hash   BLOB NOT NULL
);
CREATE TABLE peers (
	origin INTEGER PRIMARY KEY REFERENCES replicas(idx),
	vector BLOB NOT NULL,
	heard  INTEGER NOT NULL,
	gap    INTEGER NOT NULL
);
CREATE TABLE forks (
	origin     INTEGER PRIMARY KEY REFERENCES replicas(idx),
	seq        INTEGER NOT NULL,
	found      INTEGER NOT NULL,
	held_body  BLOB,
	held_sig   BLOB,
	other_body BLOB NOT NULL,
	other_sig  BLOB NOT NULL
);
`

// Store is one replica's store, its state held in memory as last committed.
type Store struct {
	db   *sql.DB
	path string

	mu    sync.Mutex
	state crdt.State
	clock crdt.Clock // the latest op's
	// peers is what each other replica last said: its vector, when, and its clock's gap.
	peers map[devicekey.ID]peer
	// forked are the origins that signed two ops under one dot: none of their ops are
	// taken any more, from them or by relay.
	forked map[devicekey.ID]bool
	// held are ops a peer sent that cannot be applied yet: past ops not held, or from
	// the future. Retried after every change; lost with the process, as a peer sends
	// them again.
	held []crdt.Op
	// changed is closed after every commit, and replaced.
	changed chan struct{}
	// sessions are the open sync streams, one per peer.
	sessions  map[devicekey.ID]*session
	compacted time.Time
}

// peer is what a replica last said of itself.
type peer struct {
	vector crdt.Vector
	heard  time.Time
	gap    time.Duration
}

// Open opens (creating) the store at path, sealed under key. A key that does not open
// it is sealeddb.ErrWrongKey, and a store of a newer kith ErrNewer.
func Open(ctx context.Context, path string, key []byte) (*Store, error) {
	db, err := sealeddb.OpenWith(ctx, path, key, sealeddb.Setup{TxLock: "immediate"})
	if err != nil {
		return nil, fmt.Errorf("mesh: open %s: %w", path, err)
	}
	s := &Store{db: db, path: path}
	if err := s.load(ctx); err != nil {
		_ = db.Close() // the open failed; its error is the one to report
		return nil, fmt.Errorf("mesh: open %s: %w", path, err)
	}
	return s, nil
}

// Copy writes a consistent copy of the store at src (under srcKey) to dst under dstKey,
// a file that must not exist or be empty, as a backup takes the cache: one read
// transaction (VACUUM INTO), so kithd may go on writing src. src is checked first and
// the copy after: whole, and a store this kith reads; a newer kith's is ErrNewer. Used
// to take the store into a backup and to put it back from one.
func Copy(ctx context.Context, src string, srcKey []byte, dst string, dstKey []byte) error {
	if err := check(ctx, src, srcKey); err != nil {
		return err
	}
	db, err := sealeddb.OpenWith(ctx, src, srcKey, sealeddb.Setup{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("mesh: copy %s: %w", src, err)
	}
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", sealeddb.Target(dst, dstKey))
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("mesh: copy %s: %w", src, err)
	}
	f, err := os.OpenFile(dst, os.O_RDWR, 0) // #nosec G304 -- the copy's path, the caller's
	if err == nil {
		err = f.Sync()
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return fmt.Errorf("mesh: copy %s: %w", src, err)
	}
	return check(ctx, dst, dstKey)
}

// check reports why the store at path, under key, is not one this kith reads: damaged,
// a newer kith's, or holding what does not read. It only reads.
func check(ctx context.Context, path string, key []byte) error {
	db, err := sealeddb.OpenWith(ctx, path, key, sealeddb.Setup{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("mesh: %s: %w", path, err)
	}
	defer func() { _ = db.Close() }()
	var verdict string
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&verdict); err != nil {
		return fmt.Errorf("mesh: %s: %w", path, err)
	}
	if verdict != "ok" {
		return fmt.Errorf("mesh: %s is damaged: %s", path, verdict)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("mesh: %s: %w", path, err)
	}
	if version == 0 {
		return nil // laid out by nobody yet: nothing to read
	}
	if err := (&Store{db: db}).load(ctx); err != nil {
		return fmt.Errorf("mesh: %s: %w", path, err)
	}
	return nil
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
	s.changed, s.sessions = make(chan struct{}), map[devicekey.ID]*session{}
	s.peers, s.forked = map[devicekey.ID]peer{}, map[devicekey.ID]bool{}
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
	state := crdt.State{Entries: map[string][]crdt.Entry{}, Vector: crdt.Vector{}}
	if err := s.loadEntries(ctx, state.Entries); err != nil {
		return err
	}
	if err := s.loadVector(ctx, state.Vector); err != nil {
		return err
	}
	var err error
	if state.RosterOps, err = s.loadOps(ctx, rosterOps); err != nil {
		return err
	}
	if _, err := state.Roster(); err != nil {
		return fmt.Errorf("read the roster: %w", err)
	}
	if err := s.loadPeers(ctx); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT max(coalesce((SELECT max(clock) FROM ops), 0), coalesce((SELECT max(clock) FROM entries), 0))`).
		Scan(&s.clock); err != nil {
		return fmt.Errorf("read the clock: %w", err)
	}
	s.state = state
	return nil
}

// loadEntries reads every key's entries into entries.
func (s *Store) loadEntries(ctx context.Context, entries map[string][]crdt.Entry) error {
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
		entries[key] = append(entries[key], e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the entries: %w", err)
	}
	for _, e := range entries {
		slices.SortFunc(e, func(a, b crdt.Entry) int {
			return cmp.Or(slices.Compare(a.Dot.Origin[:], b.Dot.Origin[:]), cmp.Compare(a.Dot.Seq, b.Dot.Seq))
		})
	}
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

// The ops loadOps reads: the roster's, and those of one origin in a range of seqs (the
// origin's ID, after which seq, up to which), in an order that follows causality: by
// (clock, origin), as a write's clock is past every op it had seen.
const (
	selectOps   = `SELECT o.body, o.sig FROM ops o JOIN replicas r ON r.idx = o.origin `
	rosterOps   = selectOps + `WHERE o.roster = 1 ORDER BY o.clock, r.id`
	originOps   = selectOps + `WHERE r.id = ? AND o.seq > ? AND o.seq <= ? ORDER BY o.clock, r.id`
	deleteEvery = `DELETE FROM entries; DELETE FROM ops; DELETE FROM vv; DELETE FROM peers; DELETE FROM forks;`
)

// loadOps reads the kept ops query (rosterOps, originOps) names.
func (s *Store) loadOps(ctx context.Context, query string, args ...any) ([]crdt.Op, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read the ops: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []crdt.Op
	for rows.Next() {
		var body, sig []byte
		if err := rows.Scan(&body, &sig); err != nil {
			return nil, fmt.Errorf("read the ops: %w", err)
		}
		op, err := crdt.ParseOp(body, sig)
		if err != nil {
			return nil, fmt.Errorf("read the ops: %w", err)
		}
		out = append(out, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the ops: %w", err)
	}
	return out, nil
}

// loadPeers reads what each peer last said, and the origins found forked.
func (s *Store) loadPeers(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, p.vector, p.heard, p.gap FROM peers p JOIN replicas r ON r.idx = p.origin`)
	if err != nil {
		return fmt.Errorf("read the peers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, vector []byte
		var heard, gap int64
		if err = rows.Scan(&id, &vector, &heard, &gap); err != nil {
			return fmt.Errorf("read the peers: %w", err)
		}
		v, verr := decodeVector(vector)
		if verr != nil || len(id) != len(devicekey.ID{}) {
			return errors.New("read the peers: a row does not read")
		}
		s.peers[devicekey.ID(id)] = peer{vector: v, heard: time.UnixMilli(heard), gap: time.Duration(gap) * time.Millisecond}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("read the peers: %w", err)
	}
	forks, err := s.db.QueryContext(ctx, `SELECT r.id FROM forks f JOIN replicas r ON r.idx = f.origin`)
	if err != nil {
		return fmt.Errorf("read the forks: %w", err)
	}
	defer func() { _ = forks.Close() }()
	for forks.Next() {
		var id []byte
		if err := forks.Scan(&id); err != nil || len(id) != len(devicekey.ID{}) {
			return errors.New("read the forks: a row does not read")
		}
		s.forked[devicekey.ID(id)] = true
	}
	if err := forks.Err(); err != nil {
		return fmt.Errorf("read the forks: %w", err)
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

// Changed is closed after the store's next commit: its own write or a peer's.
func (s *Store) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// notifyLocked wakes everything waiting on Changed.
func (s *Store) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
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
	for id := range roster {
		m := roster[id]
		if m.Removed {
			removed = append(removed, id.String())
			continue
		}
		members = append(members, deviceOf(id, &m))
	}
	slices.SortFunc(members, func(a, b domain.Device) int {
		return cmp.Or(a.AddedAt.Compare(b.AddedAt), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	slices.Sort(removed)
	return members, removed, nil
}

// deviceOf is a roster member as a device.
func deviceOf(id devicekey.ID, m *crdt.Member) domain.Device {
	return domain.Device{Name: m.Name, ID: id.String(), Role: domain.DeviceRole(m.Role), AddedAt: m.AddedAt, AddedBy: m.AddedBy}
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
	return s.writeLocked(ctx, signer, origin, writes)
}

// writeLocked writes writes as this replica's next op, signed by signer.
func (s *Store) writeLocked(ctx context.Context, signer crypto.Signer, origin devicekey.ID, writes []crdt.Write) error {
	op := crdt.Op{
		Origin: origin, Seq: s.state.Vector[origin].Seq + 1, Clock: s.clock.Next(time.Now()),
		Context: s.state.Vector.Context(), Writes: writes,
	}
	if err := op.Sign(signer); err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	next, err := s.state.Apply(op)
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	if err := s.commit(ctx, []crdt.Op{op}, next, nil); err != nil {
		return err
	}
	s.state, s.clock = next, op.Clock
	s.notifyLocked()
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
		if was, ok := roster[id]; ok && !was.Removed {
			m.Key, m.Addresses = was.Key, was.Addresses // a rename keeps a bridge's key and addresses
		}
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
	for i := range members {
		value, err := members[i].Encode()
		if err != nil {
			return nil, fmt.Errorf("mesh: %w", err)
		}
		writes = append(writes, crdt.Write{Key: crdt.RosterKey(ids[i]), Value: value})
	}
	return writes, nil
}

// Bridge writes the roster entry of the bridge d names (its name, when and by whom it
// was added) with its public key (PKIX) and the addresses it is dialed at, signed by
// signer: a bridge of the roster. Its own entry carries the signer's key.
func (s *Store) Bridge(ctx context.Context, signer crypto.Signer, d domain.Device, key []byte, addresses []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	origin, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	id, err := devicekey.ParseID(d.ID)
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	if id == origin {
		if key, err = x509.MarshalPKIXPublicKey(signer.Public()); err != nil {
			return fmt.Errorf("mesh: %w", err)
		}
	}
	value, err := crdt.Member{
		Key: key, Name: d.Name, Role: crdt.RoleBridge, Addresses: addresses, AddedAt: d.AddedAt, AddedBy: d.AddedBy,
	}.Encode()
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	return s.writeLocked(ctx, signer, origin, []crdt.Write{{Key: crdt.RosterKey(id), Value: value}})
}

// PublishAccounts writes the accounts this bridge runs to its own key, signed by
// signer, when they differ from what it holds there.
func (s *Store) PublishAccounts(ctx context.Context, signer crypto.Signer, accounts []domain.MeshAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	origin, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	list := make([]crdt.Account, 0, len(accounts))
	for _, a := range accounts {
		list = append(list, crdt.Account{Network: a.Network, ID: a.ID, Label: a.Label})
	}
	slices.SortFunc(list, func(a, b crdt.Account) int {
		return cmp.Or(cmp.Compare(a.Network, b.Network), cmp.Compare(a.ID, b.ID), cmp.Compare(a.Label, b.Label))
	})
	value, err := crdt.EncodeAccounts(list)
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	key := crdt.AccountsKey(origin)
	if held := s.state.Entries[key]; len(held) == 1 && bytes.Equal(held[0].Value, value) {
		return nil
	}
	return s.writeLocked(ctx, signer, origin, []crdt.Write{{Key: key, Value: value}})
}

// fork is an origin found to have signed two ops under one dot: the one this replica
// holds (none when it no longer keeps it) and the other.
type fork struct {
	held  *crdt.Op
	other crdt.Op
}

// commit keeps ops and what they changed of next: the entries of every key they wrote,
// and each origin's head; and the forks found.
func (s *Store) commit(ctx context.Context, ops []crdt.Op, next crdt.State, forks []fork) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mesh: keep an op: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	keys, origins := map[string]bool{}, map[devicekey.ID]bool{}
	for _, op := range ops {
		if err := insertOp(ctx, tx, op); err != nil {
			return err
		}
		for _, w := range op.Writes {
			keys[w.Key] = true
		}
		origins[op.Origin] = true
	}
	for key := range keys {
		if err := writeEntries(ctx, tx, key, next.Entries[key]); err != nil {
			return err
		}
	}
	for origin := range origins {
		if err := writeHead(ctx, tx, origin, next.Vector[origin]); err != nil {
			return err
		}
	}
	if err := insertForks(ctx, tx, forks); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mesh: keep an op: %w", err)
	}
	return nil
}

// insertOp keeps op in the log, unless it is there already.
func insertOp(ctx context.Context, tx *sql.Tx, op crdt.Op) error {
	origin, err := replica(ctx, tx, op.Origin)
	if err != nil {
		return err
	}
	roster := slices.ContainsFunc(op.Writes, func(w crdt.Write) bool { return crdt.IsRosterKey(w.Key) })
	if _, err := tx.ExecContext(ctx, `INSERT INTO ops (origin, seq, clock, body, sig, roster) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`, origin, op.Seq, op.Clock, op.Body(), op.Sig, roster); err != nil {
		return fmt.Errorf("mesh: keep an op: %w", err)
	}
	return nil
}

// writeEntries makes key's rows entries.
func writeEntries(ctx context.Context, tx *sql.Tx, key string, entries []crdt.Entry) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE key = ?`, key); err != nil {
		return fmt.Errorf("mesh: keep an entry: %w", err)
	}
	for _, e := range entries {
		by, err := replica(ctx, tx, e.Dot.Origin)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entries (key, origin, seq, value, clock) VALUES (?, ?, ?, ?, ?)`,
			key, by, e.Dot.Seq, e.Value, e.Clock); err != nil {
			return fmt.Errorf("mesh: keep an entry: %w", err)
		}
	}
	return nil
}

// writeHead keeps origin's head.
func writeHead(ctx context.Context, tx *sql.Tx, origin devicekey.ID, head crdt.Head) error {
	idx, err := replica(ctx, tx, origin)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vv (origin, seq, hash) VALUES (?, ?, ?)
		ON CONFLICT (origin) DO UPDATE SET seq = excluded.seq, hash = excluded.hash`,
		idx, head.Seq, head.Hash[:]); err != nil {
		return fmt.Errorf("mesh: keep the version vector: %w", err)
	}
	return nil
}

// insertForks keeps each fork found, the first of each origin.
func insertForks(ctx context.Context, tx *sql.Tx, forks []fork) error {
	for i := range forks {
		f := &forks[i]
		idx, err := replica(ctx, tx, f.other.Origin)
		if err != nil {
			return err
		}
		var heldBody, heldSig []byte
		if f.held != nil {
			heldBody, heldSig = f.held.Body(), f.held.Sig
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO forks (origin, seq, found, held_body, held_sig, other_body, other_sig)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			idx, f.other.Seq, time.Now().UnixMilli(), heldBody, heldSig, f.other.Body(), f.other.Sig); err != nil {
			return fmt.Errorf("mesh: keep a fork: %w", err)
		}
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
