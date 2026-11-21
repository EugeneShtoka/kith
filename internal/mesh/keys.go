package mesh

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// maxSeals is how many ops a bridge seals under one epoch before it makes a new one:
// AES-GCM's random 96-bit nonces stay safe only well below 2³² boxes under one key.
const maxSeals = 1 << 28

// keyring is what a replica holds to seal and open ops: its sealing key, the content
// key of each epoch a wrap for it opened (and how many ops it sealed under each), and
// every epoch record taken, to pass on. added and touched are what changed since it
// was last kept.
type keyring struct {
	sealing *ecdh.PrivateKey
	keys    map[crdt.Epoch]epochKey
	records map[[sha256.Size]byte]crdt.EpochRecord
	added   [][sha256.Size]byte
	touched map[crdt.Epoch]bool
}

// epochKey is an epoch's content key, and how many ops this replica sealed under it.
type epochKey struct {
	key   []byte
	seals uint64
}

// clone is k to change, kept only once what changes with it is committed.
func (k *keyring) clone() keyring {
	return keyring{sealing: k.sealing, keys: maps.Clone(k.keys), records: maps.Clone(k.records), touched: map[crdt.Epoch]bool{}}
}

// public is this replica's sealing key as its roster entry carries it.
func (k *keyring) public() []byte { return k.sealing.PublicKey().Bytes() }

// errNoKey is an envelope sealed under an epoch no wrap for this replica has opened:
// its op waits, and the epoch is asked for.
var errNoKey = errors.New("mesh: no content key for the op's epoch")

// unseal checks op's envelope: it must open, under its epoch's key, to op itself.
func (k *keyring) unseal(op crdt.Op) error {
	e, err := crdt.SealedEpoch(op.Sealed)
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	held, ok := k.keys[e]
	if !ok {
		return fmt.Errorf("%w (%s)", errNoKey, e)
	}
	if err := crdt.Unseal(op.Sealed, held.key, op); err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	return nil
}

// open is the op envelope holds, opened under its epoch's key (crdt.Open).
func (k *keyring) open(envelope []byte) (crdt.Op, error) {
	e, err := crdt.SealedEpoch(envelope)
	if err != nil {
		return crdt.Op{}, fmt.Errorf("mesh: %w", err)
	}
	held, ok := k.keys[e]
	if !ok {
		return crdt.Op{}, fmt.Errorf("%w (%s)", errNoKey, e)
	}
	op, err := crdt.Open(envelope, held.key)
	if err != nil {
		return crdt.Op{}, fmt.Errorf("mesh: %w", err)
	}
	return op, nil
}

// seal seals op, written by self and applied to state, under the epoch it writes
// under (current), made first when none serves; a roster op state keeps gets its
// envelope too, so a snapshot carries it.
func (k *keyring) seal(state *crdt.State, signer crypto.Signer, self devicekey.ID, op *crdt.Op) error {
	roster, err := state.Roster()
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	e, ok := k.current(*state, roster)
	if !ok || k.keys[e].seals >= maxSeals {
		if e, err = k.make(*state, roster, signer, self); err != nil {
			return err
		}
	}
	held := k.keys[e]
	if op.Sealed, err = crdt.Seal(*op, e, held.key); err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	if n := len(state.RosterOps) - 1; n >= 0 && state.RosterOps[n].Origin == op.Origin && state.RosterOps[n].Seq == op.Seq {
		state.RosterOps[n].Sealed = op.Sealed // Apply appended it to a slice of its own
	}
	held.seals++
	k.keys[e], k.touched[e] = held, true
	return nil
}

// current is the epoch this replica writes under as of state: the latest by (number,
// maker) whose key it holds, made by a bridge still in the mesh (under its key then, or
// one it rotated to) after every bridge removal state holds, at a context state holds (so it can wrap it for a bridge that
// joins, wrapFor). Two bridges that made one at once both stand; writers converge on
// the later. One it has sealed maxSeals ops under is used up: seal makes the next,
// never falling back to an older one the others have moved on from.
func (k *keyring) current(state crdt.State, roster map[devicekey.ID]crdt.Member) (crdt.Epoch, bool) {
	var best crdt.Epoch
	found := false
	for e := range k.keys {
		ctx, ok := k.contextOf(e)
		now, live := crdt.Successor(roster, e.Maker)
		if !ok || !live || roster[now].Role != crdt.RoleBridge || !holds(state, ctx) || !state.Since(ctx) {
			continue
		}
		if !found || e.After(best) {
			best, found = e, true
		}
	}
	return best, found
}

// make makes a new epoch of self's, past every one known, wrapped for every bridge of
// roster that has a sealing key.
func (k *keyring) make(state crdt.State, roster map[devicekey.ID]crdt.Member, signer crypto.Signer, self devicekey.ID) (crdt.Epoch, error) {
	var newest uint64
	for h := range k.records {
		newest = max(newest, k.records[h].Epoch.Number)
	}
	e := crdt.Epoch{Number: newest + 1, Maker: self}
	key := make([]byte, crdt.KeyLen)
	if _, err := rand.Read(key); err != nil {
		return e, fmt.Errorf("mesh: make a content key: %w", err)
	}
	rec := crdt.EpochRecord{Epoch: e, Context: state.Vector.Context(), Signer: self}
	for _, id := range slices.SortedFunc(maps.Keys(roster), func(a, b devicekey.ID) int { return bytes.Compare(a[:], b[:]) }) {
		if m := roster[id]; !m.Removed && m.Role == crdt.RoleBridge && len(m.Sealing) > 0 {
			w, err := crdt.WrapKey(e, key, id, m.Sealing)
			if err != nil {
				return e, fmt.Errorf("mesh: %w", err)
			}
			rec.Wraps = append(rec.Wraps, w)
		}
	}
	if err := rec.Sign(signer); err != nil {
		return e, fmt.Errorf("mesh: %w", err)
	}
	k.add(rec)
	k.keys[e], k.touched[e] = epochKey{key: key}, true
	return e, nil
}

// contextOf is the context epoch e was made at, as its maker's record says (another's,
// the first by hash, when the maker's is not held).
func (k *keyring) contextOf(e crdt.Epoch) ([]crdt.Dot, bool) {
	var out []crdt.Dot
	var first [sha256.Size]byte
	found := false
	for h := range k.records {
		r := k.records[h]
		switch {
		case r.Epoch != e:
		case r.Signer == e.Maker:
			return r.Context, true
		case !found || bytes.Compare(h[:], first[:]) < 0:
			out, first, found = r.Context, h, true
		}
	}
	return out, found
}

func (k *keyring) add(rec crdt.EpochRecord) {
	h := rec.Hash()
	k.records[h], k.added = rec, append(k.added, h)
}

// Why a peer's epoch record is not taken.
var errRecordSigner = errors.New("mesh: an epoch record not signed by a bridge of the mesh")

// joined is k's sealing key with nothing else, and records taken as roster admits
// them: the keyring of a bridge joining a mesh. One not taken is asked for again, as
// any record missed.
func (k *keyring) joined(records []crdt.EpochRecord, roster map[devicekey.ID]crdt.Member, self devicekey.ID) keyring {
	out := keyring{sealing: k.sealing, keys: map[crdt.Epoch]epochKey{}, records: map[[sha256.Size]byte]crdt.EpochRecord{}, touched: map[crdt.Epoch]bool{}}
	for i := range records {
		_ = out.take(records[i], roster, self)
	}
	return out
}

// take keeps a record a peer passed on, signed by a bridge of roster, and the content
// key of its wrap for self, if it has one and the key is not held yet.
func (k *keyring) take(rec crdt.EpochRecord, roster map[devicekey.ID]crdt.Member, self devicekey.ID) error {
	if _, ok := k.records[rec.Hash()]; ok {
		return nil
	}
	m, ok := roster[rec.Signer]
	if !ok || m.Removed || m.Role != crdt.RoleBridge {
		return fmt.Errorf("%w (%s)", errRecordSigner, rec.Signer)
	}
	if err := crdt.VerifyRecord(m.Key, rec); err != nil {
		return fmt.Errorf("mesh: an epoch record: %w", err)
	}
	k.add(rec)
	if _, held := k.keys[rec.Epoch]; held {
		return nil
	}
	for _, w := range rec.Wraps {
		if w.To != self {
			continue
		}
		key, err := crdt.Unwrap(rec.Epoch, w, k.sealing)
		if err != nil {
			return fmt.Errorf("mesh: %w", err)
		}
		k.keys[rec.Epoch], k.touched[rec.Epoch] = epochKey{key: key}, true
	}
	return nil
}

// wrapFor adds a wrap of epoch e for the bridge to, signed by self: for a bridge
// joining, or one that asked. Nothing unless this replica holds e's key and the context
// it was made at (so it has seen every removal e was made after), and to is a bridge
// of the mesh with a sealing key.
func (k *keyring) wrapFor(state crdt.State, roster map[devicekey.ID]crdt.Member, signer crypto.Signer, self, to devicekey.ID, e crdt.Epoch) error {
	held, ok := k.keys[e]
	ctx, known := k.contextOf(e)
	m := roster[to]
	if !ok || !known || !holds(state, ctx) || m.Removed || m.Role != crdt.RoleBridge || len(m.Sealing) == 0 {
		return nil
	}
	w, err := crdt.WrapKey(e, held.key, to, m.Sealing)
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	rec := crdt.EpochRecord{Epoch: e, Context: ctx, Wraps: []crdt.Wrap{w}, Signer: self}
	if err := rec.Sign(signer); err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	k.add(rec)
	return nil
}

// holds reports whether state holds every op ctx names.
func holds(state crdt.State, ctx []crdt.Dot) bool {
	return !slices.ContainsFunc(ctx, func(d crdt.Dot) bool { return !state.Vector.Covers(d) })
}

// save keeps what changed of k in tx.
func (k *keyring) save(ctx context.Context, tx *sql.Tx) error {
	for _, h := range k.added {
		r := k.records[h]
		if _, err := tx.ExecContext(ctx, `INSERT INTO records (hash, body, sig) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			h[:], r.Body(), r.Sig); err != nil {
			return fmt.Errorf("mesh: keep an epoch record: %w", err)
		}
	}
	for e := range k.touched {
		held := k.keys[e]
		if _, err := tx.ExecContext(ctx, `INSERT INTO epochs (number, maker, key, seals) VALUES (?, ?, ?, ?)
			ON CONFLICT (number, maker) DO UPDATE SET key = excluded.key, seals = excluded.seals`,
			e.Number, e.Maker[:], held.key, held.seals); err != nil {
			return fmt.Errorf("mesh: keep a content key: %w", err)
		}
	}
	k.added, k.touched = nil, map[crdt.Epoch]bool{}
	return nil
}

// loadKeys reads the keyring.
func (s *Store) loadKeys(ctx context.Context) error {
	s.keys = keyring{keys: map[crdt.Epoch]epochKey{}, records: map[[sha256.Size]byte]crdt.EpochRecord{}, touched: map[crdt.Epoch]bool{}}
	var private []byte
	if err := s.db.QueryRowContext(ctx, `SELECT private FROM sealing`).Scan(&private); err != nil {
		return fmt.Errorf("read the sealing key: %w", err)
	}
	var err error
	if s.keys.sealing, err = ecdh.X25519().NewPrivateKey(private); err != nil {
		return fmt.Errorf("read the sealing key: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT number, maker, key, seals FROM epochs`)
	if err != nil {
		return fmt.Errorf("read the content keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var e crdt.Epoch
		var maker []byte
		var held epochKey
		if rows.Scan(&e.Number, &maker, &held.key, &held.seals) != nil || len(maker) != len(e.Maker) {
			return errors.New("read the content keys: a row does not read")
		}
		e.Maker = devicekey.ID(maker)
		s.keys.keys[e] = held
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("read the content keys: %w", err)
	}
	records, err := s.db.QueryContext(ctx, `SELECT body, sig FROM records`)
	if err != nil {
		return fmt.Errorf("read the epoch records: %w", err)
	}
	defer func() { _ = records.Close() }()
	for records.Next() {
		var body, sig []byte
		if err := records.Scan(&body, &sig); err != nil {
			return fmt.Errorf("read the epoch records: %w", err)
		}
		r, err := crdt.ParseEpochRecord(body, sig)
		if err != nil {
			return fmt.Errorf("read the epoch records: %w", err)
		}
		s.keys.records[r.Hash()] = r
	}
	if err := records.Err(); err != nil {
		return fmt.Errorf("read the epoch records: %w", err)
	}
	return nil
}

// newSealing makes this replica's sealing key and keeps it in tx.
func newSealing(ctx context.Context, tx *sql.Tx) error {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("make a sealing key: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sealing (id, private) VALUES (1, ?)`, key.Bytes()); err != nil {
		return fmt.Errorf("keep the sealing key: %w", err)
	}
	return nil
}

// Sealing is this replica's sealing key's public half (X25519): what its roster entry
// carries, and what a bridge joining a mesh sends with its pairing.
func (s *Store) Sealing() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys.public()
}

// checkSnapshot refuses a snapshot holding a roster op whose envelope, one this
// replica holds the key of, does not open to the op.
func (k *keyring) checkSnapshot(snap crdt.Snapshot) error {
	for i := range snap.RosterOps {
		op := &snap.RosterOps[i]
		if len(op.Sealed) == 0 {
			continue
		}
		if err := k.unseal(*op); err != nil && !errors.Is(err, errNoKey) {
			return fmt.Errorf("mesh: the snapshot's roster: %s's op %d: %w", op.Origin, op.Seq, err)
		}
	}
	return nil
}
