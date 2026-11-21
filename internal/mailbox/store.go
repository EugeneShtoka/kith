// Package mailbox is kith-mailbox's store, and its half of a sync session with a
// bridge (planning/design-mailbox.md): the envelopes and sealed snapshots bridges leave
// with it, kept and passed on to the others. It reads nothing it holds beyond what is
// in the clear (an envelope's origin and seq, a snapshot's maker and vector, an epoch
// record's wraps), never writes an op, and is no replica.
package mailbox

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// The bounds the mailbox keeps.
const (
	// Horizon is the mesh's (mesh.Horizon): an envelope a snapshot covers is kept this
	// long after it arrived, by the mailbox's own clock, as a bridge keeps an op.
	Horizon = 30 * 24 * time.Hour
	// snapshotBytes is how much of what the newest snapshot does not cover makes one
	// due, besides its age (Horizon).
	snapshotBytes = 64 << 20
	// maxEnvelope bounds an envelope: an op's body at its bound, padded and sealed.
	maxEnvelope = crdt.MaxOpSize + 16<<10
	// maxBoxes bounds a sealed snapshot's boxes, each at most crdt.MaxOpSize.
	maxBoxes = 1024
)

// schemaVersion is the store's layout (PRAGMA user_version); a later one is refused.
const schemaVersion = 1

// schema keeps what bridges leave: envelopes, several under one dot when an origin
// signed two ops under it (a fork, kept and passed on); sealed snapshots whole, as the
// frame that carries one; epoch records; and each key admitted, or revoked (admitted =
// 0). pairings are open pairings, which kith-mailbox pair and the listener answering
// them share.
const schema = `
CREATE TABLE envelopes (
	origin   BLOB NOT NULL,
	seq      INTEGER NOT NULL,
	hash     BLOB NOT NULL,
	envelope BLOB NOT NULL,
	received INTEGER NOT NULL,
	PRIMARY KEY (origin, seq, hash)
) WITHOUT ROWID;
CREATE TABLE snapshots (
	n        INTEGER PRIMARY KEY,
	frame    BLOB NOT NULL,
	received INTEGER NOT NULL
);
CREATE TABLE records (
	hash BLOB PRIMARY KEY,
	body BLOB NOT NULL,
	sig  BLOB NOT NULL
) WITHOUT ROWID;
CREATE TABLE admitted (
	id       BLOB PRIMARY KEY,
	admitted INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE pairings (
	token   TEXT PRIMARY KEY,
	expires INTEGER NOT NULL,
	device  BLOB,
	verdict INTEGER NOT NULL DEFAULT 0
);
`

// ErrNewer is a store a newer kith-mailbox wrote.
var ErrNewer = errors.New("mailbox: the store was written by a newer kith-mailbox")

// Store is a mailbox's store, its index held in memory as last committed: what it
// keeps of each envelope and snapshot, not the bytes.
type Store struct {
	db   *sql.DB
	path string

	mu sync.Mutex
	// held is each origin's envelopes, by seq; more than one at a seq is a fork.
	held map[devicekey.ID]map[uint64][]kept
	// snapshots are those kept, none dominated by another.
	snapshots []snapshot
	records   map[[sha256.Size]byte]*meshv1.EpochRecord
	// admitted is each key admitted (true) or revoked (false).
	admitted map[devicekey.ID]bool
	// wants are each bridge's epochs whose key it lacks, as it said last.
	wants map[devicekey.ID][]*meshv1.Epoch
	// sessions are the open sessions of each bridge, ended when it is revoked.
	sessions map[devicekey.ID]map[*context.CancelCauseFunc]bool
	// changed is closed after every commit, and replaced.
	changed chan struct{}
}

// kept is an envelope as the index keeps it.
type kept struct {
	hash     [sha256.Size]byte
	size     int
	received time.Time
}

// snapshot is a sealed snapshot as the index keeps it.
type snapshot struct {
	n        int64
	maker    devicekey.ID
	vector   map[devicekey.ID]uint64
	size     int
	received time.Time
}

// Open opens (creating) the store at path, sealed under key.
func Open(ctx context.Context, path string, key []byte) (*Store, error) {
	db, err := sealeddb.OpenWith(ctx, path, key, sealeddb.Setup{TxLock: "immediate"})
	if err != nil {
		return nil, fmt.Errorf("mailbox: open %s: %w", path, err)
	}
	s := &Store{db: db, path: path}
	if err := s.load(ctx); err != nil {
		_ = db.Close() // the open failed; its error is the one to report
		return nil, fmt.Errorf("mailbox: open %s: %w", path, err)
	}
	return s, nil
}

// Close closes the store.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("mailbox: close: %w", err)
	}
	return nil
}

// load lays out a new store, or reads the index of what one holds.
func (s *Store) load(ctx context.Context) error {
	s.held, s.records = map[devicekey.ID]map[uint64][]kept{}, map[[sha256.Size]byte]*meshv1.EpochRecord{}
	s.admitted, s.wants = map[devicekey.ID]bool{}, map[devicekey.ID][]*meshv1.Epoch{}
	s.sessions, s.changed = map[devicekey.ID]map[*context.CancelCauseFunc]bool{}, make(chan struct{})
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read the version: %w", err)
	}
	switch {
	case version > schemaVersion:
		return fmt.Errorf("%w (layout %d, this one reads %d)", ErrNewer, version, schemaVersion)
	case version == 0:
		if _, err := s.db.ExecContext(ctx, schema+fmt.Sprintf("PRAGMA user_version = %d;", schemaVersion)); err != nil {
			return fmt.Errorf("lay out the store: %w", err)
		}
		return nil
	}
	return errors.Join(s.loadEnvelopes(ctx), s.loadSnapshots(ctx), s.loadRecords(ctx), s.loadAdmitted(ctx))
}

func (s *Store) loadEnvelopes(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT origin, seq, hash, length(envelope), received FROM envelopes`)
	if err != nil {
		return fmt.Errorf("read the envelopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var origin, hash []byte
		var seq uint64
		var k kept
		var received int64
		if err := rows.Scan(&origin, &seq, &hash, &k.size, &received); err != nil {
			return fmt.Errorf("read the envelopes: %w", err)
		}
		if len(origin) != len(devicekey.ID{}) || len(hash) != len(k.hash) {
			return errors.New("read the envelopes: a row does not read")
		}
		k.hash, k.received = [sha256.Size]byte(hash), time.UnixMilli(received)
		s.index(devicekey.ID(origin), seq, k)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the store: %w", err)
	}
	return nil
}

func (s *Store) loadSnapshots(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT n, frame, received FROM snapshots`)
	if err != nil {
		return fmt.Errorf("read the snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var frame []byte
		var sn snapshot
		var received int64
		if err = rows.Scan(&sn.n, &frame, &received); err != nil {
			return fmt.Errorf("read the snapshots: %w", err)
		}
		var msg meshv1.SealedSnapshot
		if err = proto.Unmarshal(frame, &msg); err != nil {
			return fmt.Errorf("read the snapshots: %w", err)
		}
		if sn.maker, sn.vector, err = clearOf(&msg); err != nil {
			return fmt.Errorf("read the snapshots: %w", err)
		}
		sn.size, sn.received = len(frame), time.UnixMilli(received)
		s.snapshots = append(s.snapshots, sn)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the store: %w", err)
	}
	return nil
}

func (s *Store) loadRecords(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT hash, body, sig FROM records`)
	if err != nil {
		return fmt.Errorf("read the epoch records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var hash []byte
		r := &meshv1.EpochRecord{}
		if err := rows.Scan(&hash, &r.Body, &r.Sig); err != nil || len(hash) != sha256.Size {
			return errors.New("read the epoch records: a row does not read")
		}
		s.records[[sha256.Size]byte(hash)] = r
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the store: %w", err)
	}
	return nil
}

func (s *Store) loadAdmitted(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, admitted FROM admitted`)
	if err != nil {
		return fmt.Errorf("read who is admitted: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id []byte
		var admitted bool
		if err := rows.Scan(&id, &admitted); err != nil || len(id) != len(devicekey.ID{}) {
			return errors.New("read who is admitted: a row does not read")
		}
		s.admitted[devicekey.ID(id)] = admitted
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the store: %w", err)
	}
	return nil
}

// index adds an envelope to the index.
func (s *Store) index(origin devicekey.ID, seq uint64, k kept) {
	if s.held[origin] == nil {
		s.held[origin] = map[uint64][]kept{}
	}
	s.held[origin][seq] = append(s.held[origin][seq], k)
	slices.SortFunc(s.held[origin][seq], func(a, b kept) int { return bytes.Compare(a.hash[:], b.hash[:]) })
}

// Changed is closed after the store's next commit.
func (s *Store) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func (s *Store) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// Admits reports whether the key id may sync with the mailbox.
func (s *Store) Admits(id devicekey.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.admitted[id]
}

// Admit admits the key id, a bridge paired with the mailbox by hand: the one way a key
// revoked is admitted again.
func (s *Store) Admit(ctx context.Context, id devicekey.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.admitLocked(ctx, map[devicekey.ID]bool{id: true})
}

// admit takes what a bridge said of who is admitted: each key it admits that was never
// revoked, and each it revokes. Every session of a key revoked ends, and the snapshots
// it made go: the bridges refuse a removed
// bridge's word for the mesh's state, and the mailbox would go on counting what they
// cover as held.
func (s *Store) admit(ctx context.Context, said *meshv1.Admission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := map[devicekey.ID]bool{}
	for _, list := range [][][]byte{said.GetAdmit(), said.GetRevoke()} {
		for _, raw := range list {
			if len(raw) != len(devicekey.ID{}) {
				return fmt.Errorf("%w: an admission names no key", errProtocol)
			}
		}
	}
	for _, raw := range said.GetAdmit() {
		if _, known := s.admitted[devicekey.ID(raw)]; !known {
			changed[devicekey.ID(raw)] = true
		}
	}
	for _, raw := range said.GetRevoke() {
		if admitted, known := s.admitted[devicekey.ID(raw)]; admitted || !known {
			changed[devicekey.ID(raw)] = false
		}
	}
	if len(changed) == 0 {
		return nil
	}
	return s.admitLocked(ctx, changed)
}

func (s *Store) admitLocked(ctx context.Context, changed map[devicekey.ID]bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mailbox: keep who is admitted: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	var keep []snapshot
	for id, admitted := range changed {
		if _, err := tx.ExecContext(ctx, `INSERT INTO admitted (id, admitted) VALUES (?, ?)
			ON CONFLICT (id) DO UPDATE SET admitted = excluded.admitted`, id[:], admitted); err != nil {
			return fmt.Errorf("mailbox: keep who is admitted: %w", err)
		}
	}
	for _, sn := range s.snapshots {
		if admitted, ok := changed[sn.maker]; ok && !admitted {
			if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE n = ?`, sn.n); err != nil {
				return fmt.Errorf("mailbox: keep who is admitted: %w", err)
			}
			continue
		}
		keep = append(keep, sn)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mailbox: keep who is admitted: %w", err)
	}
	if len(keep) != len(s.snapshots) {
		s.snapshots = keep
		s.notifyLocked()
	}
	for id, admitted := range changed {
		s.admitted[id] = admitted
		if !admitted {
			for end := range s.sessions[id] {
				(*end)(errRevoked)
			}
		}
	}
	return nil
}

// errRevoked ends the session of a key no longer admitted.
var errRevoked = errors.New("mailbox: the key is no longer admitted")

// put keeps envelopes a bridge left at now: each that reads, once per hash. A second
// envelope under a dot held is a fork, kept beside the first.
func (s *Store) put(ctx context.Context, envelopes [][]byte, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	type item struct {
		dot crdt.Dot
		k   kept
		env []byte
	}
	var fresh []item
	for _, env := range envelopes {
		dot, err := crdt.SealedDot(env)
		if err != nil || len(env) > maxEnvelope || dot.Seq == 0 {
			return fmt.Errorf("%w: an envelope that does not read", errProtocol)
		}
		k := kept{hash: sha256.Sum256(env), size: len(env), received: now}
		if slices.ContainsFunc(s.held[dot.Origin][dot.Seq], func(o kept) bool { return o.hash == k.hash }) ||
			slices.ContainsFunc(fresh, func(o item) bool { return o.k.hash == k.hash }) {
			continue
		}
		fresh = append(fresh, item{dot, k, env})
	}
	if len(fresh) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mailbox: keep an envelope: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	for i := range fresh {
		it := &fresh[i]
		if _, err := tx.ExecContext(ctx, `INSERT INTO envelopes (origin, seq, hash, envelope, received) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`, it.dot.Origin[:], it.dot.Seq, it.k.hash[:], it.env, now.UnixMilli()); err != nil {
			return fmt.Errorf("mailbox: keep an envelope: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mailbox: keep an envelope: %w", err)
	}
	for i := range fresh {
		s.index(fresh[i].dot.Origin, fresh[i].dot.Seq, fresh[i].k)
	}
	s.notifyLocked()
	return nil
}

// putSnapshot keeps a sealed snapshot a bridge left at now, and drops every snapshot it
// dominates (one that dominates it, it is dropped instead). Its maker's signature is
// the bridges' to check: the mailbox holds no key.
func (s *Store) putSnapshot(ctx context.Context, snap *meshv1.SealedSnapshot, now time.Time) error {
	maker, vector, err := clearOf(snap)
	switch {
	case err != nil:
		return err
	case len(snap.GetBoxes()) > maxBoxes || slices.ContainsFunc(snap.GetBoxes(), func(b []byte) bool { return len(b) > crdt.MaxOpSize }):
		return fmt.Errorf("%w: a snapshot past the mailbox's bounds", errProtocol)
	}
	frame, err := proto.Marshal(snap)
	if err != nil {
		return fmt.Errorf("mailbox: keep a snapshot: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.snapshots, func(o snapshot) bool { return dominates(o.vector, vector) }) {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mailbox: keep a snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	res, err := tx.ExecContext(ctx, `INSERT INTO snapshots (frame, received) VALUES (?, ?)`, frame, now.UnixMilli())
	if err != nil {
		return fmt.Errorf("mailbox: keep a snapshot: %w", err)
	}
	n, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("mailbox: keep a snapshot: %w", err)
	}
	keep := []snapshot{{n: n, maker: maker, vector: vector, size: len(frame), received: now}}
	for _, o := range s.snapshots {
		if dominates(vector, o.vector) {
			if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE n = ?`, o.n); err != nil {
				return fmt.Errorf("mailbox: keep a snapshot: %w", err)
			}
			continue
		}
		keep = append(keep, o)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mailbox: keep a snapshot: %w", err)
	}
	s.snapshots = keep
	s.notifyLocked()
	return nil
}

// dominates reports whether a covers everything b does.
func dominates(a, b map[devicekey.ID]uint64) bool {
	for origin, seq := range b {
		if a[origin] < seq {
			return false
		}
	}
	return true
}

// clearOf is a sealed snapshot's clear fields: its maker and the vector it covers.
func clearOf(snap *meshv1.SealedSnapshot) (devicekey.ID, map[devicekey.ID]uint64, error) {
	if len(snap.GetMaker()) != len(devicekey.ID{}) || len(snap.GetBoxes()) == 0 {
		return devicekey.ID{}, nil, fmt.Errorf("%w: a snapshot names no maker", errProtocol)
	}
	vector := map[devicekey.ID]uint64{}
	for _, h := range snap.GetVector() {
		if len(h.GetOrigin()) != len(devicekey.ID{}) {
			return devicekey.ID{}, nil, fmt.Errorf("%w: a snapshot's vector does not read", errProtocol)
		}
		vector[devicekey.ID(h.GetOrigin())] = h.GetSeq()
	}
	return devicekey.ID(snap.GetMaker()), vector, nil
}

// putRecords keeps the epoch records that read and are not held yet.
func (s *Store) putRecords(ctx context.Context, records []*meshv1.EpochRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fresh := map[[sha256.Size]byte]*meshv1.EpochRecord{}
	for _, r := range records {
		parsed, err := crdt.ParseEpochRecord(r.GetBody(), r.GetSig())
		if err != nil || len(r.GetBody())+len(r.GetSig()) > maxEnvelope {
			return fmt.Errorf("%w: an epoch record that does not read", errProtocol)
		}
		if h := parsed.Hash(); s.records[h] == nil {
			fresh[h] = &meshv1.EpochRecord{Body: r.GetBody(), Sig: r.GetSig()}
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mailbox: keep an epoch record: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	for h, r := range fresh {
		if _, err := tx.ExecContext(ctx, `INSERT INTO records (hash, body, sig) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			h[:], r.GetBody(), r.GetSig()); err != nil {
			return fmt.Errorf("mailbox: keep an epoch record: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mailbox: keep an epoch record: %w", err)
	}
	maps.Copy(s.records, fresh)
	s.notifyLocked()
	return nil
}

// want keeps the epochs bridge lacks, as it said last.
func (s *Store) want(bridge devicekey.ID, epochs []*meshv1.Epoch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(epochs) == 0 && len(s.wants[bridge]) == 0 {
		return
	}
	s.wants[bridge] = epochs
	s.notifyLocked()
}

// vector is, per origin, how far the mailbox holds its ops: the furthest any snapshot
// covers, then each envelope held after it in turn; and the hash of the envelope at
// that seq (the lowest, under a fork), none when it holds only a snapshot there.
func (s *Store) vectorLocked() map[devicekey.ID]crdt.Head {
	out := map[devicekey.ID]crdt.Head{}
	for _, sn := range s.snapshots {
		for origin, seq := range sn.vector {
			if seq > out[origin].Seq {
				out[origin] = crdt.Head{Seq: seq}
			}
		}
	}
	for origin, at := range s.held {
		h := out[origin]
		for len(at[h.Seq+1]) > 0 {
			h = crdt.Head{Seq: h.Seq + 1, Hash: at[h.Seq+1][0].hash}
		}
		if h.Seq > 0 && h.Hash == ([sha256.Size]byte{}) && len(at[h.Seq]) > 0 {
			h.Hash = at[h.Seq][0].hash
		}
		out[origin] = h
	}
	return out
}

// missing is what a bridge holding up to sent lacks: per origin, the envelopes after it
// in seq order, when the mailbox holds each of them; where it does not (the bridge is
// behind what the mailbox keeps), the snapshots that bring it furthest there, each
// sent once a session (told), and the envelopes after them. upTo is how far it brings
// the bridge.
func (s *Store) missing(ctx context.Context, sent map[devicekey.ID]uint64, told map[int64]bool) (envelopes [][]byte, snaps []*meshv1.SealedSnapshot, upTo map[devicekey.ID]uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vector := s.vectorLocked()
	upTo = maps.Clone(sent)
	for {
		behind := map[devicekey.ID]bool{}
		for origin, h := range vector {
			for seq := upTo[origin] + 1; seq <= h.Seq; seq++ {
				if len(s.held[origin][seq]) == 0 {
					behind[origin] = true
					break
				}
			}
		}
		best, ok := s.furthestLocked(behind, upTo, told)
		if !ok {
			break
		}
		told[best.n] = true
		snap, err := s.snapshotLocked(ctx, best.n)
		if err != nil {
			return nil, nil, nil, err
		}
		snaps = append(snaps, snap)
		for origin, seq := range best.vector {
			upTo[origin] = max(upTo[origin], seq)
		}
	}
	type at struct {
		origin devicekey.ID
		seq    uint64
	}
	var want []at
	for _, origin := range slices.SortedFunc(maps.Keys(vector), byOrigin) {
		seq := upTo[origin] + 1
		for ; seq <= vector[origin].Seq && len(s.held[origin][seq]) > 0; seq++ {
			want = append(want, at{origin, seq})
		}
		upTo[origin] = max(upTo[origin], seq-1)
	}
	for _, w := range want {
		env, err := s.envelopeLocked(ctx, w.origin, w.seq, s.held[w.origin][w.seq][0].hash)
		if err != nil {
			return nil, nil, nil, err
		}
		envelopes = append(envelopes, env)
	}
	return envelopes, snaps, upTo, nil
}

// furthestLocked is the snapshot not told yet that brings a bridge holding up to upTo
// furthest in the origins it is behind in: the most ops past upTo there.
func (s *Store) furthestLocked(behind map[devicekey.ID]bool, upTo map[devicekey.ID]uint64, told map[int64]bool) (snapshot, bool) {
	var best snapshot
	var most uint64
	for _, sn := range s.snapshots {
		var n uint64
		for origin := range behind {
			n += sn.vector[origin] - min(sn.vector[origin], upTo[origin])
		}
		if !told[sn.n] && n > most {
			best, most = sn, n
		}
	}
	return best, most > 0
}

// bestLocked is the snapshot covering the most ops.
func (s *Store) bestLocked() (snapshot, bool) {
	var best snapshot
	var most uint64
	found := false
	for _, sn := range s.snapshots {
		var n uint64
		for _, seq := range sn.vector {
			n += seq
		}
		if !found || n > most || (n == most && sn.n > best.n) {
			best, most, found = sn, n, true
		}
	}
	return best, found
}

func (s *Store) snapshotLocked(ctx context.Context, n int64) (*meshv1.SealedSnapshot, error) {
	var frame []byte
	if err := s.db.QueryRowContext(ctx, `SELECT frame FROM snapshots WHERE n = ?`, n).Scan(&frame); err != nil {
		return nil, fmt.Errorf("mailbox: read a snapshot: %w", err)
	}
	var out meshv1.SealedSnapshot
	if err := proto.Unmarshal(frame, &out); err != nil {
		return nil, fmt.Errorf("mailbox: read a snapshot: %w", err)
	}
	return &out, nil
}

func (s *Store) envelopeLocked(ctx context.Context, origin devicekey.ID, seq uint64, hash [sha256.Size]byte) ([]byte, error) {
	var env []byte
	if err := s.db.QueryRowContext(ctx, `SELECT envelope FROM envelopes WHERE origin = ? AND seq = ? AND hash = ?`,
		origin[:], seq, hash[:]).Scan(&env); err != nil {
		return nil, fmt.Errorf("mailbox: read an envelope: %w", err)
	}
	return env, nil
}

// forks is every envelope held under a dot that holds more than one, by hash.
func (s *Store) forks(ctx context.Context) (map[[sha256.Size]byte][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[[sha256.Size]byte][]byte{}
	for origin, at := range s.held {
		for seq, ks := range at {
			if len(ks) < 2 {
				continue
			}
			for _, k := range ks {
				env, err := s.envelopeLocked(ctx, origin, seq, k.hash)
				if err != nil {
					return nil, err
				}
				out[k.hash] = env
			}
		}
	}
	return out, nil
}

// due reports whether the mailbox wants a snapshot at now: it holds none, or more than
// Horizon or snapshotBytes of envelopes past the one covering most.
func (s *Store) dueLocked(now time.Time) bool {
	best, ok := s.bestLocked()
	if !ok {
		return true
	}
	size := 0
	for origin, at := range s.held {
		for seq, ks := range at {
			if seq <= best.vector[origin] {
				continue
			}
			for _, k := range ks {
				if size += k.size; size > snapshotBytes || now.Sub(k.received) > Horizon {
					return true
				}
			}
		}
	}
	return false
}

// Compact drops every envelope a snapshot covers that arrived over Horizon before now.
func (s *Store) Compact(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cover := map[devicekey.ID]uint64{}
	for _, sn := range s.snapshots {
		for origin, seq := range sn.vector {
			cover[origin] = max(cover[origin], seq)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mailbox: compact: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	type gone struct {
		origin devicekey.ID
		seq    uint64
		hash   [sha256.Size]byte
	}
	var drop []gone
	for origin, at := range s.held {
		for seq, ks := range at {
			for _, k := range ks {
				if seq <= cover[origin] && now.Sub(k.received) > Horizon {
					drop = append(drop, gone{origin, seq, k.hash})
				}
			}
		}
	}
	for _, g := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM envelopes WHERE origin = ? AND seq = ? AND hash = ?`, g.origin[:], g.seq, g.hash[:]); err != nil {
			return fmt.Errorf("mailbox: compact: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mailbox: compact: %w", err)
	}
	for _, g := range drop {
		at := s.held[g.origin]
		at[g.seq] = slices.DeleteFunc(at[g.seq], func(k kept) bool { return k.hash == g.hash })
		if len(at[g.seq]) == 0 {
			delete(at, g.seq)
		}
	}
	return nil
}

// Size is the store's size on disk, its write-ahead log included.
func (s *Store) Size() int64 {
	var n int64
	for _, p := range []string{s.path, s.path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// sortedRecords is every record held, by hash.
func (s *Store) sortedRecords() ([][sha256.Size]byte, map[[sha256.Size]byte]*meshv1.EpochRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.SortedFunc(maps.Keys(s.records), func(a, b [sha256.Size]byte) int { return bytes.Compare(a[:], b[:]) }), maps.Clone(s.records)
}

// othersWants is what every bridge but peer lacks, by bridge.
func (s *Store) othersWants(peer devicekey.ID) map[devicekey.ID][]*meshv1.Epoch {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[devicekey.ID][]*meshv1.Epoch{}
	for id, w := range s.wants {
		if id != peer && len(w) > 0 && s.admitted[id] {
			out[id] = w
		}
	}
	return out
}

// byOrigin orders device IDs.
func byOrigin(a, b devicekey.ID) int { return cmp.Compare(string(a[:]), string(b[:])) }
