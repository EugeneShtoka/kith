package mesh

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// A mailbox (planning/design-mailbox.md) is a peer that keeps the envelopes bridges
// leave with it, and sealed snapshots, and passes them on to the other bridges. It
// opens nothing, writes nothing and is no replica: its ack does not hold the log back.
// A bridge dials every mailbox of its roster; a mailbox never dials.

// ErrNotAMailbox is a session with a peer the roster does not list as a mailbox (never,
// or no longer).
var ErrNotAMailbox = errors.New("mesh: the peer is not a mailbox of the mesh")

// SyncMailbox runs one session of this bridge (signer) with the mailbox, over frames,
// until either side ends it, ctx ends, or the mailbox leaves the roster. as is the key
// the session was authenticated as: signer's, or the one it rotated from while the
// mailbox admits only that one. A Hello each way; then each side sends the envelopes the other lacks (this bridge a
// sealed snapshot instead when the mailbox asks for one or the log no longer keeps
// them; the mailbox its snapshot when this bridge is behind what it keeps), then every
// one after, as it is written or left. This bridge says who is admitted after what it
// leaves, and again when that changes.
func (s *Store) SyncMailbox(ctx context.Context, signer crypto.Signer, as, mailbox devicekey.ID, frames Frames, log *slog.Logger) error {
	ctx, end := context.WithCancelCause(ctx)
	defer end(nil)
	if err := s.stillListed(mailbox); err != nil {
		return err
	}
	ss := syncing{signer: signer, as: as, peer: mailbox, frames: frames, log: log, told: newTold()}
	theirs, due, err := s.greetMailbox(ctx, ss, time.Now())
	if err != nil {
		return err
	}
	release, err := s.claim(mailbox, as, end)
	if err != nil {
		return err
	}
	defer release()
	sent, err := s.leave(ctx, ss, theirs, due)
	if err != nil {
		return err
	}
	admitted, err := s.admit(frames, nil)
	if err != nil {
		return err
	}
	in, failed := receiving(ctx, frames)
	return s.serveMailbox(ctx, ss, sent, admitted, in, failed)
}

// admit tells the mailbox who is admitted (admission) when it differs from what it was
// told last (told), and returns what it is told now. It follows what leave sent: a
// removal the mailbox acts on is one it holds, so the removed bridge's peers learn of
// it there, though the removed bridge can reach them no more.
func (s *Store) admit(frames Frames, told *meshv1.SyncFrame) (*meshv1.SyncFrame, error) {
	now := s.admission()
	if proto.Equal(now, told) {
		return told, nil
	}
	if err := frames.Send(now); err != nil {
		return nil, fmt.Errorf("mesh: tell the mailbox who is admitted: %w", err)
	}
	return now, nil
}

// AddMailbox writes the roster entry of the mailbox d names (its name, when and by whom
// it was added) and the addresses it is dialed at, signed by signer, a bridge of the
// roster.
func (s *Store) AddMailbox(ctx context.Context, signer crypto.Signer, d domain.Device, addresses []string) error {
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
	value, err := crdt.Member{Name: d.Name, Role: crdt.RoleMailbox, Addresses: addresses, AddedAt: d.AddedAt, AddedBy: d.AddedBy}.Encode()
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	return s.writeLocked(ctx, signer, origin, []crdt.Write{{Key: crdt.RosterKey(id), Value: value}})
}

// stillListed refuses a session once the peer is not a mailbox of the roster.
func (s *Store) stillListed(mailbox devicekey.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	roster, err := s.state.Roster()
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	if m, ok := roster[mailbox]; !ok || m.Removed || m.Role != crdt.RoleMailbox {
		return fmt.Errorf("%w: %s", ErrNotAMailbox, mailbox)
	}
	return nil
}

// greetMailbox says Hello and reads the mailbox's: it must be the mailbox the stream
// authenticated, speak this protocol and say it is a mailbox. Its clock is only logged.
// Its vector (of envelopes) is returned and kept as what it said last, and whether it
// asks for a snapshot; a mailbox holding fewer of this bridge's own ops than it said
// before has lost or withheld them, which is logged. Its epoch records are taken.
func (s *Store) greetMailbox(ctx context.Context, ss syncing, now time.Time) (crdt.Vector, bool, error) {
	if err := ss.frames.Send(helloFrame(ss.as, now, blind(s.vector()), s.tell(ss))); err != nil {
		return nil, false, fmt.Errorf("mesh: say hello: %w", err)
	}
	f, err := ss.frames.Receive()
	if err != nil {
		return nil, false, fmt.Errorf("mesh: read the mailbox's hello: %w", err)
	}
	h := f.GetHello()
	switch {
	case h == nil:
		return nil, false, fmt.Errorf("%w: it did not say hello first", errProtocol)
	case !bytes.Equal(h.GetReplica(), ss.peer[:]):
		return nil, false, fmt.Errorf("%w: it says it is another mailbox", errProtocol)
	case h.GetProtocol() != protocol:
		return nil, false, fmt.Errorf("%w: it speaks protocol %d, this kith %d; run one kith on both", errProtocol, h.GetProtocol(), protocol)
	case h.GetMailbox() == nil:
		return nil, false, fmt.Errorf("%w: it says it is no mailbox", errProtocol)
	}
	theirs, err := vectorOf(h.GetVector())
	if err != nil {
		return nil, false, err
	}
	if gap := time.UnixMilli(h.GetWallMs()).Sub(now); gap > MaxClockGap || gap < -MaxClockGap {
		ss.log.Info("the mailbox's clock is off; it decides nothing", "mailbox", ss.peer.String(), "gap", gap.String())
	}
	self, err := devicekey.IDOf(ss.signer.Public())
	if err != nil {
		return nil, false, fmt.Errorf("mesh: %w", err)
	}
	s.mu.Lock()
	was := s.peers[ss.peer].vector[self].Seq
	s.mailboxBytes[ss.peer] = h.GetMailbox().GetStoreBytes()
	s.mu.Unlock()
	if now := theirs[self].Seq; now < was {
		ss.log.Warn("the mailbox has lost or withheld this bridge's ops", "mailbox", ss.peer.String(), "after", now, "it held", was)
	}
	if herr := s.heard(ctx, ss.peer, theirs, 0, now); herr != nil {
		return nil, false, herr
	}
	refused, err := s.takeEpochs(ctx, ss.signer, ss.peer, h.GetEpochs())
	if err != nil {
		return nil, false, err
	}
	for _, why := range refused {
		ss.log.Warn("refused what the mailbox passed on", "mailbox", ss.peer.String(), "err", why)
	}
	return theirs, h.GetMailbox().GetSnapshotDue(), nil
}

// admission is what this bridge holds of who may sync with a mailbox: each bridge of
// the roster, and each removed (a key rotated from among them).
func (s *Store) admission() *meshv1.SyncFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	roster, _ := s.state.Roster() // it read when the session began, and every op taken since read too
	out := &meshv1.Admission{}
	for _, id := range slices.SortedFunc(maps.Keys(roster), func(a, b devicekey.ID) int { return bytes.Compare(a[:], b[:]) }) {
		switch m := roster[id]; {
		case m.Role != crdt.RoleBridge:
		case m.Removed:
			out.Revoke = append(out.Revoke, bytes.Clone(id[:]))
		default:
			out.Admit = append(out.Admit, bytes.Clone(id[:]))
		}
	}
	return &meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Admission{Admission: out}}
}

// leave sends the mailbox what it lacks past theirs (its vector, of envelopes): a
// sealed snapshot when it asks for one (due) or the log keeps some of those ops no
// longer or without their envelopes (written before ops were sealed); else the
// envelopes, each origin's in seq order. An op this bridge holds at the head the mailbox
// holds, under another envelope, goes too: the mailbox then holds both, a fork, and
// tells every bridge. It returns the vector the mailbox holds then, as far as this
// bridge knows.
func (s *Store) leave(ctx context.Context, ss syncing, theirs crdt.Vector, due bool) (crdt.Vector, error) {
	envelopes, snap, upTo, err := s.toLeave(ctx, ss.signer, theirs, due)
	switch {
	case err != nil:
		return nil, err
	case snap != nil:
		err = sendSealedSnapshot(ss.frames, snap)
	default:
		err = sendEnvelopes(ss.frames, envelopes, false)
	}
	if err != nil {
		return nil, err
	}
	// Seqs only: the mailbox's heads are checked for a fork once a session.
	out := crdt.Vector{}
	for origin, h := range theirs {
		out[origin] = crdt.Head{Seq: h.Seq}
	}
	for origin, h := range upTo {
		if h.Seq > out[origin].Seq {
			out[origin] = crdt.Head{Seq: h.Seq}
		}
	}
	return out, nil
}

// toLeave is what leave sends: envelopes, or a sealed snapshot, and the vector they
// bring the mailbox to.
func (s *Store) toLeave(ctx context.Context, signer crypto.Signer, theirs crdt.Vector, due bool) ([][]byte, *meshv1.SealedSnapshot, crdt.Vector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	upTo := maps.Clone(s.state.Vector)
	var out [][]byte
	for _, origin := range slices.SortedFunc(maps.Keys(upTo), func(a, b devicekey.ID) int { return bytes.Compare(a[:], b[:]) }) {
		h, from := upTo[origin], theirs[origin]
		if from.Seq > 0 && from.Seq <= h.Seq && from.Hash != ([sha256.Size]byte{}) {
			// The mailbox's head: another envelope there is a fork, and this one its proof.
			if held := s.opAt(ctx, nil, origin, from.Seq); held != nil && len(held.Sealed) > 0 && sha256.Sum256(held.Sealed) != from.Hash {
				out = append(out, held.Sealed)
			}
		}
		if due || h.Seq <= from.Seq {
			continue
		}
		got, err := s.loadOps(ctx, originOps, origin[:], from.Seq, h.Seq)
		if err != nil {
			return nil, nil, nil, err
		}
		if uint64(len(got)) != h.Seq-from.Seq || slices.ContainsFunc(got, func(o crdt.Op) bool { return len(o.Sealed) == 0 }) {
			due = true
			continue
		}
		for i := range got {
			out = append(out, got[i].Sealed)
		}
	}
	if !due {
		return out, nil, upTo, nil
	}
	snap, err := s.sealSnapshotLocked(ctx, signer)
	if err != nil {
		return nil, nil, nil, err
	}
	return nil, snap, upTo, nil
}

// sealSnapshotLocked is this replica's snapshot sealed under the epoch it writes under
// (made first when none serves, as for an op), signed by signer (this bridge): its
// vector in the clear, without hashes. Each box counts as a seal under the epoch.
func (s *Store) sealSnapshotLocked(ctx context.Context, signer crypto.Signer) (*meshv1.SealedSnapshot, error) {
	maker, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	roster, err := s.state.Roster()
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	keys := s.keys.clone()
	e, ok := keys.current(s.state, roster)
	if !ok || keys.keys[e].seals >= maxSeals {
		if e, err = keys.make(s.state, roster, signer, maker); err != nil {
			return nil, err
		}
	}
	if werr := keys.wrapRecovery(s.state, signer, maker); werr != nil {
		return nil, werr
	}
	plain, err := proto.Marshal(snapshotFrame(s.state.Snapshot(s.clock)))
	if err != nil {
		return nil, fmt.Errorf("mesh: seal a snapshot: %w", err)
	}
	held := keys.keys[e]
	boxes, err := crdt.SealSnapshot(plain, maker, e, held.key)
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	held.seals += uint64(len(boxes))
	keys.keys[e], keys.touched[e] = held, true
	if kerr := s.keepKeysLocked(ctx, keys); kerr != nil {
		return nil, kerr
	}
	clear := s.state.Vector.Context()
	sig, err := crdt.SignSnapshot(signer, crdt.SnapshotDigest(maker, clear, boxes))
	if err != nil {
		return nil, fmt.Errorf("mesh: %w", err)
	}
	out := &meshv1.SealedSnapshot{Boxes: boxes, Maker: bytes.Clone(maker[:]), Sig: sig, Last: true}
	for _, d := range clear {
		out.Vector = append(out.Vector, &meshv1.Head{Origin: bytes.Clone(d.Origin[:]), Seq: d.Seq, Hash: make([]byte, sha256.Size)})
	}
	return out, nil
}

// serveMailbox is a mailbox session past its Hellos: it leaves the mailbox what it
// lacks past sent, again after every commit, tells it again who is admitted when that
// changed since admitted, and takes each frame the mailbox sends, until either ends.
func (s *Store) serveMailbox(ctx context.Context, ss syncing, sent crdt.Vector, admitted *meshv1.SyncFrame,
	in <-chan *meshv1.SyncFrame, failed <-chan error,
) error {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	var gathering *meshv1.SealedSnapshot
	for {
		changed := s.Changed()
		if err := s.stillListed(ss.peer); err != nil {
			return err
		}
		if e := s.tell(ss); e != nil {
			if err := ss.frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Epochs{Epochs: e}}); err != nil {
				return fmt.Errorf("mesh: send epoch records: %w", err)
			}
		}
		var err error
		if sent, err = s.leave(ctx, ss, sent, false); err != nil {
			return err
		}
		if admitted, err = s.admit(ss.frames, admitted); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ended(ctx)
		case <-changed:
		case now := <-ticker.C:
			if terr := s.tickMailbox(ctx, ss, now); terr != nil {
				return terr
			}
		case ferr := <-failed:
			if errors.Is(ferr, io.EOF) {
				return nil
			}
			return fmt.Errorf("mesh: the sync stream: %w", ferr)
		case f := <-in:
			if gathering, err = s.takeFromMailbox(ctx, ss, f, gathering, sent, time.Now()); err != nil {
				return err
			}
		}
	}
}

// tickMailbox is a mailbox session's tick at now: the snapshot and the ops held back
// tried again, and the log compacted when due.
func (s *Store) tickMailbox(ctx context.Context, ss syncing, now time.Time) error {
	if err := s.retrySnapshot(ctx, ss, now); err != nil {
		return err
	}
	if _, err := s.receive(ctx, nil, now); err != nil {
		return err
	}
	s.compactIfDue(ctx, now, ss.log)
	return nil
}

// takeFromMailbox handles one frame of the mailbox's at now: envelopes opened and
// applied (those under an epoch whose key is not held yet wait), a sealed snapshot
// gathered to its last chunk, then opened and merged, epoch records taken, an Ack
// kept. sent is bumped past what arrived, so it is not left again. A fork found is
// removed from the roster, and this bridge's op at its dot left with the mailbox. It
// returns the sealed snapshot gathered so far.
func (s *Store) takeFromMailbox(ctx context.Context, ss syncing, f *meshv1.SyncFrame, gathering *meshv1.SealedSnapshot,
	sent crdt.Vector, now time.Time,
) (*meshv1.SealedSnapshot, error) {
	var res received
	var err error
	switch {
	case f.GetEnvelopes() != nil:
		ops := s.open(f.GetEnvelopes().GetEnvelopes(), ss)
		for i := range ops {
			if h := sent[ops[i].Origin]; ops[i].Seq > h.Seq {
				sent[ops[i].Origin] = crdt.Head{Seq: ops[i].Seq}
			}
		}
		res, err = s.receive(ctx, ops, now)
	case f.GetSealedSnapshot() != nil:
		chunk := f.GetSealedSnapshot()
		if gathering == nil {
			gathering = &meshv1.SealedSnapshot{}
		}
		gathering.Boxes = append(gathering.Boxes, chunk.GetBoxes()...)
		if !chunk.GetLast() {
			return gathering, nil
		}
		gathering.Maker, gathering.Vector, gathering.Sig, gathering.Last = chunk.GetMaker(), chunk.GetVector(), chunk.GetSig(), true
		s.mu.Lock()
		s.heldSnapshot = gathering
		s.mu.Unlock()
		return nil, s.retrySnapshot(ctx, ss, now)
	case f.GetEpochs() != nil:
		var refused []error
		if refused, err = s.takeEpochs(ctx, ss.signer, ss.peer, f.GetEpochs()); err == nil {
			if err = s.retrySnapshot(ctx, ss, now); err == nil {
				res, err = s.receive(ctx, nil, now)
				res.refused = append(refused, res.refused...)
			}
		}
	case f.GetAck() != nil:
		vector, verr := vectorOf(f.GetAck().GetVector())
		if verr != nil {
			return nil, verr
		}
		return gathering, s.heard(ctx, ss.peer, vector, 0, now)
	default:
		return nil, fmt.Errorf("%w: a frame out of place from a mailbox", errProtocol)
	}
	if err != nil {
		return nil, err
	}
	return gathering, s.answerMailbox(ctx, ss, res)
}

// answerMailbox says what a batch the mailbox passed on did: refusals logged, forks
// removed and their proofs left with the mailbox, this bridge's vector acknowledged.
func (s *Store) answerMailbox(ctx context.Context, ss syncing, res received) error {
	for _, why := range res.refused {
		ss.log.Warn("refused what the mailbox passed on", "mailbox", ss.peer.String(), "err", why)
	}
	if err := ss.frames.Send(ackFrame(blind(s.vector()))); err != nil {
		return fmt.Errorf("mesh: acknowledge: %w", err)
	}
	s.removeForked(ctx, ss.signer, res.forked, ss.log)
	var proofs [][]byte
	for i := range res.proofs {
		if len(res.proofs[i].Sealed) > 0 {
			proofs = append(proofs, res.proofs[i].Sealed)
		}
	}
	return sendEnvelopes(ss.frames, proofs, false)
}

// open is each of envelopes opened to its op. One sealed under an epoch whose key is
// not held yet is held back (applyLocked opens it once the key arrives); one that does
// not open is refused, said in the log.
func (s *Store) open(envelopes [][]byte, ss syncing) []crdt.Op {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []crdt.Op
	for _, env := range envelopes {
		op, err := s.keys.open(env)
		switch {
		case errors.Is(err, errNoKey):
			if len(s.sealedHeld) < maxHeld {
				s.sealedHeld = append(s.sealedHeld, env)
			}
		case err != nil:
			ss.log.Warn("refused what the mailbox passed on", "mailbox", ss.peer.String(), "err", err)
		default:
			out = append(out, op)
		}
	}
	return out
}

// retrySnapshot opens the sealed snapshot a mailbox sent, once the key of its epoch is
// held, and merges it (mergeSealed); until then it waits, its epoch asked for. One that
// does not open, or is not its maker's, is refused, said in the log.
func (s *Store) retrySnapshot(ctx context.Context, ss syncing, now time.Time) error {
	s.mu.Lock()
	sealed := s.heldSnapshot
	s.mu.Unlock()
	if sealed == nil {
		return nil
	}
	res, err := s.mergeSealed(ctx, sealed, now)
	switch {
	case errors.Is(err, errNoKey):
		return nil
	case errors.Is(err, errAhead):
		return nil // kept; taken once this bridge's clock gets there
	}
	s.mu.Lock()
	if s.heldSnapshot == sealed {
		s.heldSnapshot = nil
	}
	s.mu.Unlock()
	if err != nil {
		if errors.Is(err, errBadSealedSnapshot) {
			ss.log.Warn("refused a snapshot the mailbox passed on", "mailbox", ss.peer.String(), "err", err)
			return nil
		}
		return err
	}
	return s.answerMailbox(ctx, ss, res)
}

// errBadSealedSnapshot is a sealed snapshot that does not open, does not read, or is
// not signed by its maker, a bridge of the mesh.
var errBadSealedSnapshot = errors.New("mesh: a sealed snapshot is refused")

// mergeSealed opens sealed, a snapshot a mailbox kept, and merges it as a peer's
// (merge): sealed under an epoch whose key is held (errNoKey: not yet), and signed, with
// its clear vector, by its maker, a bridge of the roster this replica holds, or of the
// snapshot's own when this replica does not know it yet.
func (s *Store) mergeSealed(ctx context.Context, sealed *meshv1.SealedSnapshot, now time.Time) (received, error) {
	if len(sealed.GetMaker()) != len(devicekey.ID{}) || len(sealed.GetBoxes()) == 0 {
		return received{}, fmt.Errorf("%w: it names no maker", errBadSealedSnapshot)
	}
	maker := devicekey.ID(sealed.GetMaker())
	e, err := crdt.SnapshotEpoch(sealed.GetBoxes()[0])
	if err != nil {
		return received{}, fmt.Errorf("%w: %w", errBadSealedSnapshot, err)
	}
	s.mu.Lock()
	held, ok := s.keys.keys[e]
	s.mu.Unlock()
	if !ok {
		return received{}, fmt.Errorf("%w (%s)", errNoKey, e)
	}
	plain, err := crdt.OpenSnapshot(sealed.GetBoxes(), maker, held.key)
	if err != nil {
		return received{}, fmt.Errorf("%w: %w", errBadSealedSnapshot, err)
	}
	var whole meshv1.Snapshot
	if uerr := proto.Unmarshal(plain, &whole); uerr != nil {
		return received{}, fmt.Errorf("%w: %w", errBadSealedSnapshot, uerr)
	}
	whole.Last = true
	snap, err := gather(nil, &whole)
	if err != nil {
		return received{}, fmt.Errorf("%w: %w", errBadSealedSnapshot, err)
	}
	clear, err := vectorOf(sealed.GetVector())
	if err != nil {
		return received{}, fmt.Errorf("%w: %w", errBadSealedSnapshot, err)
	}
	if err := s.checkMaker(maker, *snap, crdt.SnapshotDigest(maker, clear.Context(), sealed.GetBoxes()), sealed.GetSig()); err != nil {
		return received{}, err
	}
	return s.merge(ctx, *snap, now)
}

// checkMaker checks a sealed snapshot's signature (sig, over digest) against its
// maker's key: a bridge of this replica's roster, or of the snapshot's own when this
// replica does not list the maker (a bridge that joined since it last synced).
func (s *Store) checkMaker(maker devicekey.ID, snap crdt.Snapshot, digest [sha256.Size]byte, sig []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	roster, err := s.state.Roster()
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	if _, known := roster[maker]; !known {
		merged, _, merr := s.state.Merge(snap)
		if merr != nil {
			return fmt.Errorf("%w: %w", errBadSealedSnapshot, merr)
		}
		if roster, err = merged.Roster(); err != nil {
			return fmt.Errorf("%w: %w", errBadSealedSnapshot, err)
		}
	}
	m := roster[maker]
	if m.Removed || m.Role != crdt.RoleBridge {
		return fmt.Errorf("%w: its maker %s is not a bridge of the mesh", errBadSealedSnapshot, maker)
	}
	if err := crdt.VerifySnapshot(m.Key, digest, sig); err != nil {
		return fmt.Errorf("%w: %w", errBadSealedSnapshot, err)
	}
	return nil
}

// blind is v as a mailbox is told it: seqs alone. The hash of an op's body would let
// whoever runs it confirm a guess at what the op says.
func blind(v crdt.Vector) crdt.Vector {
	out := make(crdt.Vector, len(v))
	for origin, h := range v {
		out[origin] = crdt.Head{Seq: h.Seq}
	}
	return out
}

// snapshotFrame is snap whole, as one Snapshot frame: what a sealed snapshot seals.
func snapshotFrame(snap crdt.Snapshot) *meshv1.Snapshot {
	out := &meshv1.Snapshot{Roster: wireOps(snap.RosterOps), Vector: headsOf(snap.Vector), Clock: uint64(snap.Clock), Last: true}
	for _, key := range slices.Sorted(maps.Keys(snap.Entries)) {
		for _, e := range snap.Entries[key] {
			out.Entries = append(out.Entries, &meshv1.Entry{
				Key: key, Value: e.Value, Origin: bytes.Clone(e.Dot.Origin[:]), Seq: e.Dot.Seq, Clock: uint64(e.Clock),
			})
		}
	}
	return out
}

// sendEnvelopes sends envelopes in frames of frameItems, in order; forks marks them as a
// mailbox's forks.
func sendEnvelopes(frames Frames, envelopes [][]byte, forks bool) error {
	for len(envelopes) > 0 {
		n := min(len(envelopes), frameItems)
		f := &meshv1.Envelopes{Envelopes: envelopes[:n], Forks: forks}
		if err := frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Envelopes{Envelopes: f}}); err != nil {
			return fmt.Errorf("mesh: send envelopes: %w", err)
		}
		envelopes = envelopes[n:]
	}
	return nil
}

// sendSealedSnapshot sends snap a box per frame, its maker, vector and signature with
// the last.
func sendSealedSnapshot(frames Frames, snap *meshv1.SealedSnapshot) error {
	boxes := snap.GetBoxes()
	for i, box := range boxes {
		chunk := &meshv1.SealedSnapshot{Boxes: [][]byte{box}}
		if i == len(boxes)-1 {
			chunk.Maker, chunk.Vector, chunk.Sig, chunk.Last = snap.GetMaker(), snap.GetVector(), snap.GetSig(), true
		}
		if err := frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_SealedSnapshot{SealedSnapshot: chunk}}); err != nil {
			return fmt.Errorf("mesh: send a sealed snapshot: %w", err)
		}
	}
	return nil
}
