package mesh

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// The bounds a replica keeps (planning/design-mesh.md, decisions 10 and 12).
const (
	// Horizon is how long the log keeps an op every live peer has acknowledged, and how
	// long a peer may go unheard before it is dormant: it no longer holds the log back,
	// and comes back through a snapshot.
	Horizon = 30 * 24 * time.Hour
	// MaxClockGap is how far off a peer's wall clock may be: further, it is refused, the
	// gap named. An op whose clock is further ahead of this replica's is held until it
	// is not, so a fast clock never takes every register early.
	MaxClockGap = 60 * time.Second
	// maxHeld bounds the ops held back for a peer; past it the latest are dropped, and
	// come again with the next session.
	maxHeld = 4096
	// sizeWarning is the store's size past which a sync says so in the log.
	sizeWarning = 64 << 20
)

// received is what a batch of a peer's ops did: how many were applied, why any were
// refused, the origins newly found forked, and this replica's own ops at the dots
// where a peer's showed a fork, sent back so the peer holds both too.
type received struct {
	applied int
	refused []error
	forked  []devicekey.ID
	proofs  []crdt.Op
}

// receive applies a peer's ops at now: each that is ready, in one transaction; those
// that follow ops not held yet, or come from the future, are held and tried again
// after every change.
func (s *Store) receive(ctx context.Context, ops []crdt.Op, now time.Time) (received, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(ctx, ops, now)
}

// applyLocked applies the held ops and ops, as receive. An origin found forked is
// recorded, not refused here: an honest op written over one of its later ops would
// otherwise never be ready where they were refused, and the mesh would split. It is
// removed from the roster instead (removeForked), which every replica judges alike.
func (s *Store) applyLocked(ctx context.Context, ops []crdt.Op, now time.Time) (received, error) {
	queue := slices.Concat(s.held, ops)
	b := batch{state: s.state}
	for progress := true; progress; {
		progress = false
		var rest []crdt.Op
		for _, op := range queue {
			switch s.offer(ctx, &b, op, now) {
			case kept:
				rest = append(rest, op)
			case applied:
				progress = true
			case dropped:
			}
		}
		queue = rest
	}
	if len(b.taken)+len(b.forks) > 0 {
		if err := s.commit(ctx, b.taken, b.state, b.forks); err != nil {
			return received{}, err
		}
		s.state = b.state
		for _, op := range b.taken {
			s.clock = max(s.clock, op.Clock)
		}
		for i := range b.forks {
			s.forked[b.forks[i].other.Origin] = true
		}
		s.notifyLocked()
	}
	s.held = s.holdBack(queue)
	b.out.applied = len(b.taken)
	return b.out, nil
}

// batch is a batch of a peer's ops as applied so far: the state over them, the ops
// taken, the forks found, and what to report.
type batch struct {
	state crdt.State
	taken []crdt.Op
	forks []fork
	out   received
}

// What became of an op offered.
type offered int

const (
	dropped offered = iota // a duplicate (or a fork, recorded), or refused
	kept                   // held back: it follows ops not held yet, or is from the future
	applied
)

// offer applies op to b, at now, if it can be.
func (s *Store) offer(ctx context.Context, b *batch, op crdt.Op, now time.Time) offered {
	switch {
	case op.Seq <= b.state.Vector[op.Origin].Seq:
		f, ok := s.forkOf(ctx, b.state, b.taken, op)
		if ok && !s.forked[op.Origin] && !slices.ContainsFunc(b.forks, func(g fork) bool { return g.other.Origin == op.Origin }) {
			b.forks, b.out.forked = append(b.forks, f), append(b.out.forked, op.Origin)
			if f.held != nil {
				b.out.proofs = append(b.out.proofs, *f.held)
			}
		}
		return dropped
	case op.Clock.Time().After(now.Add(MaxClockGap)):
		return kept
	}
	next, err := b.state.Apply(op)
	switch {
	case errors.Is(err, crdt.ErrNotReady):
		return kept
	case err != nil:
		b.out.refused = append(b.out.refused, fmt.Errorf("%s's op %d: %w", op.Origin, op.Seq, err))
		return dropped
	}
	b.state, b.taken = next, append(b.taken, op)
	return applied
}

// holdBack is the ops still waiting: once each, at most maxHeld, the earliest kept.
func (s *Store) holdBack(ops []crdt.Op) []crdt.Op {
	slices.SortFunc(ops, byClock)
	type key struct {
		dot crdt.Dot
		sig string
	}
	seen := map[key]bool{}
	var out []crdt.Op
	for _, op := range ops {
		k := key{crdt.Dot{Origin: op.Origin, Seq: op.Seq}, string(op.Sig)}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, op)
	}
	return out[:min(len(out), maxHeld)]
}

// byClock orders ops by (clock, origin): an order that follows causality, as a write's
// clock is past that of every op it had seen.
func byClock(a, b crdt.Op) int {
	return cmp.Or(cmp.Compare(a.Clock, b.Clock), bytes.Compare(a.Origin[:], b.Origin[:]))
}

// forkOf reports whether op, under a dot state holds, is another op than the one held
// there: a fork, proven by op's signature. One whose dot's op is no longer kept, and
// is not the head, cannot be compared, and is taken for the duplicate it most likely
// is.
func (s *Store) forkOf(ctx context.Context, state crdt.State, taken []crdt.Op, op crdt.Op) (fork, bool) {
	held := s.opAt(ctx, taken, op.Origin, op.Seq)
	var digest [32]byte
	switch head := state.Vector[op.Origin]; {
	case held != nil:
		digest = held.Digest()
	case head.Seq == op.Seq:
		digest = head.Hash
	default:
		return fork{}, false
	}
	if digest == op.Digest() {
		return fork{}, false
	}
	roster, err := state.Roster()
	if err != nil || crdt.Verify(roster[op.Origin].Key, op) != nil {
		return fork{}, false
	}
	return fork{held: held, other: op}, true
}

// opAt is the op held at (origin, seq): among taken, else in the log; nil when it is
// not kept.
func (s *Store) opAt(ctx context.Context, taken []crdt.Op, origin devicekey.ID, seq uint64) *crdt.Op {
	if i := slices.IndexFunc(taken, func(o crdt.Op) bool { return o.Origin == origin && o.Seq == seq }); i >= 0 {
		return &taken[i]
	}
	ops, err := s.loadOps(ctx, originOps, origin[:], seq-1, seq)
	if err != nil || len(ops) == 0 {
		return nil
	}
	return &ops[0]
}

// headProofs are this replica's ops at each head of vector (a peer's) whose hash is not
// the op held there: sent to the peer, which then holds both and names the fork too.
func (s *Store) headProofs(ctx context.Context, vector crdt.Vector) []crdt.Op {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []crdt.Op
	for origin, theirs := range vector {
		mine := s.state.Vector[origin]
		if theirs.Seq == 0 || mine.Seq < theirs.Seq || (mine.Seq == theirs.Seq && mine.Hash == theirs.Hash) {
			continue
		}
		if op := s.opAt(ctx, nil, origin, theirs.Seq); op != nil && op.Digest() != theirs.Hash {
			out = append(out, *op)
		}
	}
	return out
}

// missing is what a peer holding vector lacks: this replica's ops past it, in an order
// that follows causality, or a snapshot when the log no longer keeps some of them (the
// peer is behind the horizon) or the peer holds nothing (a bridge joining). upTo is
// this replica's vector they bring the peer to.
func (s *Store) missing(ctx context.Context, vector crdt.Vector) (ops []crdt.Op, snap *crdt.Snapshot, upTo crdt.Vector, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	upTo = maps.Clone(s.state.Vector)
	if len(vector) == 0 && len(upTo) > 0 {
		snapshot := s.state.Snapshot(s.clock)
		return nil, &snapshot, upTo, nil
	}
	for origin, h := range upTo {
		from := vector[origin].Seq
		if h.Seq <= from {
			continue
		}
		got, err := s.loadOps(ctx, originOps, origin[:], from, h.Seq)
		if err != nil {
			return nil, nil, nil, err
		}
		if uint64(len(got)) != h.Seq-from {
			snapshot := s.state.Snapshot(s.clock)
			return nil, &snapshot, upTo, nil
		}
		ops = append(ops, got...)
	}
	slices.SortFunc(ops, byClock)
	return ops, nil, upTo, nil
}

// merge takes a peer's snapshot (crdt.State.Merge) at now, then tries the held ops
// again. Its proofs are this replica's ops at dots the snapshot holds otherwise.
func (s *Store) merge(ctx context.Context, snap crdt.Snapshot, now time.Time) (received, error) {
	if ahead(snap, now) {
		return received{}, errAhead
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next, dots, err := s.state.Merge(snap)
	if err != nil {
		return received{}, fmt.Errorf("mesh: %w", err)
	}
	var forks []fork
	var proofs []crdt.Op
	for _, op := range snap.RosterOps {
		if !s.state.Vector.Covers(crdt.Dot{Origin: op.Origin, Seq: op.Seq}) {
			continue
		}
		if f, ok := s.forkOf(ctx, s.state, nil, op); ok && !s.forked[op.Origin] &&
			!slices.ContainsFunc(forks, func(g fork) bool { return g.other.Origin == op.Origin }) {
			forks = append(forks, f)
		}
	}
	for _, d := range dots {
		if op := s.opAt(ctx, nil, d.Origin, d.Seq); op != nil {
			proofs = append(proofs, *op)
		}
	}
	if kerr := s.replace(ctx, next, forks, false); kerr != nil {
		return received{}, kerr
	}
	s.state, s.clock = next, max(s.clock, snap.Clock)
	var forked []devicekey.ID
	for i := range forks {
		f := &forks[i]
		s.forked[f.other.Origin] = true
		forked = append(forked, f.other.Origin)
		if f.held != nil {
			proofs = append(proofs, *f.held)
		}
	}
	s.notifyLocked()
	out, err := s.applyLocked(ctx, nil, now)
	out.proofs = append(proofs, out.proofs...)
	out.forked = append(forked, out.forked...)
	return out, err
}

// removeForked removes each origin of forked from the roster, signed by signer (this
// bridge), as an unpairing would: the fork is named in the log, and every replica
// judges the origin's later ops by that removal alike. One removed already, or a
// bridge that is no member itself, writes nothing.
func (s *Store) removeForked(ctx context.Context, signer crypto.Signer, forked []devicekey.ID, log *slog.Logger) {
	for _, id := range forked {
		log.Warn("a bridge signed two different ops under one number; removing it from the mesh", "bridge", id.String())
		members, _, err := s.Roster()
		if err != nil || !slices.ContainsFunc(members, func(d domain.Device) bool { return d.ID == id.String() }) {
			continue
		}
		if err := s.ChangeRoster(ctx, signer, nil, []string{id.String()}); err != nil {
			log.Warn("remove a forked bridge from the mesh", "bridge", id.String(), "err", err)
		}
	}
}

// errAhead is a snapshot holding a write from the future: like such an op, it is not
// taken early; the session ends, and the next one sends it again.
var errAhead = errors.New("mesh: the snapshot holds a write further ahead of this bridge's clock than the bound; taken once the clock gets there")

// ahead reports whether snap holds a write whose clock is more than MaxClockGap past
// now.
func ahead(snap crdt.Snapshot, now time.Time) bool {
	limit := now.Add(MaxClockGap)
	for _, op := range snap.RosterOps {
		if op.Clock.Time().After(limit) {
			return true
		}
	}
	for _, entries := range snap.Entries {
		for _, e := range entries {
			if e.Clock.Time().After(limit) {
				return true
			}
		}
	}
	return false
}

// replace keeps next whole: every entry and head, and every roster op; afresh drops
// all the store held first (the ops, the peers and the forks too).
func (s *Store) replace(ctx context.Context, next crdt.State, forks []fork, afresh bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mesh: keep a snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	wipe := "DELETE FROM entries"
	if afresh {
		wipe = deleteEvery
	}
	if _, err := tx.ExecContext(ctx, wipe); err != nil {
		return fmt.Errorf("mesh: keep a snapshot: %w", err)
	}
	for key, entries := range next.Entries {
		if err := writeEntries(ctx, tx, key, entries); err != nil {
			return err
		}
	}
	for _, op := range next.RosterOps {
		if err := insertOp(ctx, tx, op); err != nil {
			return err
		}
	}
	for origin, h := range next.Vector {
		if err := writeHead(ctx, tx, origin, h); err != nil {
			return err
		}
	}
	if err := insertForks(ctx, tx, forks); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mesh: keep a snapshot: %w", err)
	}
	return nil
}

// errNotJoined is a mesh whose roster does not list this bridge as a bridge: the
// pairing was not confirmed, or the bridge was removed since.
var errNotJoined = errors.New("mesh: the mesh does not list this bridge as one of its bridges: the pairing was not confirmed")

// adopt makes the store the mesh's, from a member's snapshot, for a bridge that joins
// it (self, signing with signer): its own genesis and everything it held are dropped,
// and the devices paired with it written again as its own ops, so they chain from its
// add and every bridge admits them. A device whose name the mesh has taken keeps it
// with its ID's first group added. One transaction: a join cut off leaves the store
// as it was. It returns the mesh's members as this bridge admits them now.
func (s *Store) adopt(ctx context.Context, snap crdt.Snapshot, signer crypto.Signer, self devicekey.ID) ([]domain.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mine, err := s.state.Roster()
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	next, _, err := crdt.State{}.Merge(snap)
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	theirs, err := next.Roster()
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	if m, ok := theirs[self]; !ok || m.Removed || m.Role != crdt.RoleBridge {
		return nil, errNotJoined
	}
	writes, err := readds(mine, theirs)
	if err != nil {
		return nil, err
	}
	clock := max(s.clock, snap.Clock)
	if len(writes) > 0 {
		op := crdt.Op{
			Origin: self, Seq: next.Vector[self].Seq + 1, Clock: clock.Next(time.Now()),
			Context: next.Vector.Context(), Writes: writes,
		}
		if err = op.Sign(signer); err != nil {
			return nil, fmt.Errorf("mesh: %w", err)
		}
		if next, err = next.Apply(op); err != nil {
			return nil, fmt.Errorf("mesh: %w", err)
		}
		clock = op.Clock
	}
	if kerr := s.replace(ctx, next, nil, true); kerr != nil {
		return nil, kerr
	}
	s.state, s.clock = next, clock
	s.peers, s.forked, s.held = map[devicekey.ID]peer{}, map[devicekey.ID]bool{}, nil
	s.notifyLocked()
	roster, err := next.Roster()
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	var out []domain.Device
	for id := range roster {
		if m := roster[id]; !m.Removed && id != self {
			out = append(out, deviceOf(id, &m))
		}
	}
	slices.SortFunc(out, func(a, b domain.Device) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	return out, nil
}

// readds are the writes that pair again in the mesh (theirs) each device paired with a
// joining bridge (mine) that the mesh does not list: one whose name the mesh has taken
// keeps it with its ID's first group added.
func readds(mine, theirs map[devicekey.ID]crdt.Member) ([]crdt.Write, error) {
	taken := map[string]bool{}
	for id := range theirs {
		if !theirs[id].Removed {
			taken[theirs[id].Name] = true
		}
	}
	var writes []crdt.Write
	for _, id := range slices.SortedFunc(maps.Keys(mine), func(a, b devicekey.ID) int { return bytes.Compare(a[:], b[:]) }) {
		m := mine[id]
		if _, known := theirs[id]; known || m.Removed || m.Role != crdt.RoleDevice {
			continue
		}
		if m.Name != "" && taken[m.Name] {
			m.Name += " " + strings.SplitN(id.String(), "-", 2)[0]
		}
		taken[m.Name] = true
		value, err := m.Encode()
		if err != nil {
			return nil, fmt.Errorf("mesh: %w", err)
		}
		writes = append(writes, crdt.Write{Key: crdt.RosterKey(id), Value: value})
	}
	return writes, nil
}

// heard keeps what peer said at now: its vector (nil keeps the last), and its clock's
// gap.
func (s *Store) heard(ctx context.Context, id devicekey.ID, vector crdt.Vector, gap time.Duration, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.peers[id]
	if vector != nil {
		p.vector = vector
	}
	p.heard, p.gap = now, gap
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mesh: keep a peer: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	idx, err := replica(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peers (origin, vector, heard, gap) VALUES (?, ?, ?, ?)
		ON CONFLICT (origin) DO UPDATE SET vector = excluded.vector, heard = excluded.heard, gap = excluded.gap`,
		idx, encodeVector(p.vector), now.UnixMilli(), gap.Milliseconds()); err != nil {
		return fmt.Errorf("mesh: keep a peer: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mesh: keep a peer: %w", err)
	}
	s.peers[id] = p
	return nil
}

// compact drops from the log every op but the roster's that is past the horizon at now
// and that every live peer (one heard within the horizon) holds. A dormant peer holds
// nothing back; it gets a snapshot when it is back.
func (s *Store) compact(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	drop, err := s.pastTheHorizon(ctx, now)
	if err != nil {
		return err
	}
	for _, p := range s.peers {
		if now.Sub(p.heard) > Horizon {
			continue
		}
		drop = slices.DeleteFunc(drop, func(d crdt.Dot) bool { return !p.vector.Covers(d) })
	}
	for _, d := range drop {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM ops WHERE origin = (SELECT idx FROM replicas WHERE id = ?) AND seq = ?`,
			d.Origin[:], d.Seq); err != nil {
			return fmt.Errorf("mesh: compact: %w", err)
		}
	}
	return nil
}

// pastTheHorizon is every op the log keeps, but the roster's, whose clock is past the
// horizon at now.
func (s *Store) pastTheHorizon(ctx context.Context, now time.Time) ([]crdt.Dot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, o.seq FROM ops o JOIN replicas r ON r.idx = o.origin
		WHERE o.roster = 0 AND o.clock < ?`, crdt.At(now.Add(-Horizon)))
	if err != nil {
		return nil, fmt.Errorf("mesh: compact: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []crdt.Dot
	for rows.Next() {
		var id []byte
		var d crdt.Dot
		if err := rows.Scan(&id, &d.Seq); err != nil || len(id) != len(d.Origin) {
			return nil, errors.New("mesh: compact: a row does not read")
		}
		d.Origin = devicekey.ID(id)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mesh: compact: %w", err)
	}
	return out, nil
}

// size is the store's size on disk, its write-ahead log included.
func (s *Store) size() int64 {
	var n int64
	for _, p := range []string{s.path, s.path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// Status is the mesh as this bridge (self) sees it: every other bridge of the roster,
// and every one found forked, with what each said last.
func (s *Store) Status(self devicekey.ID) (domain.MeshStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roster, err := s.state.Roster()
	if err != nil {
		return domain.MeshStatus{}, fmt.Errorf("mesh: %w", err)
	}
	me, ok := roster[self]
	out := domain.MeshStatus{
		Self: self.String(), Addresses: me.Addresses, StoreBytes: s.size(), Removed: !ok || me.Removed, Accounts: s.accountsLocked(self),
	}
	for id := range roster {
		m := roster[id]
		if id == self || m.Role != crdt.RoleBridge || (m.Removed && !s.forked[id]) {
			continue
		}
		p := s.peers[id]
		var lag uint64
		for origin, h := range s.state.Vector {
			lag += h.Seq - min(h.Seq, p.vector[origin].Seq)
		}
		out.Peers = append(out.Peers, domain.MeshPeer{
			Bridge: deviceOf(id, &m), Addresses: m.Addresses, Accounts: s.accountsLocked(id), Heard: p.heard, ClockGap: p.gap,
			Lag: lag, Connected: s.sessions[id] != nil, Forked: s.forked[id],
		})
	}
	slices.SortFunc(out.Peers, func(a, b domain.MeshPeer) int {
		return cmp.Or(cmp.Compare(a.Bridge.Name, b.Bridge.Name), cmp.Compare(a.Bridge.ID, b.Bridge.ID))
	})
	return out, nil
}

// accountsLocked is what id last published of the accounts it runs.
func (s *Store) accountsLocked(id devicekey.ID) []domain.MeshAccount {
	entries := s.state.Entries[crdt.AccountsKey(id)]
	if len(entries) == 0 {
		return nil
	}
	list, err := crdt.ReadAccounts(entries[len(entries)-1].Value)
	if err != nil {
		return nil
	}
	out := make([]domain.MeshAccount, 0, len(list))
	for _, a := range list {
		out = append(out, domain.MeshAccount{Network: a.Network, ID: a.ID, Label: a.Label})
	}
	return out
}

// encodeVector is a vector as peers keeps it: per origin, its ID, seq and hash.
func encodeVector(v crdt.Vector) []byte {
	var b []byte
	for origin, h := range v {
		b = append(b, origin[:]...)
		b = binary.AppendUvarint(b, h.Seq)
		b = append(b, h.Hash[:]...)
	}
	return b
}

// errNotAVector is a kept vector that does not read.
var errNotAVector = errors.New("mesh: a kept version vector does not read")

// decodeVector reads a vector as encodeVector writes it.
func decodeVector(b []byte) (crdt.Vector, error) {
	v := crdt.Vector{}
	for len(b) > 0 {
		var origin devicekey.ID
		var h crdt.Head
		if len(b) < len(origin) {
			return nil, errNotAVector
		}
		origin, b = devicekey.ID(b[:len(origin)]), b[len(origin):]
		seq, n := binary.Uvarint(b)
		if n <= 0 || len(b) < n+len(h.Hash) {
			return nil, errNotAVector
		}
		h.Seq, h.Hash, b = seq, [len(h.Hash)]byte(b[n:n+len(h.Hash)]), b[n+len(h.Hash):]
		v[origin] = h
	}
	return v, nil
}
