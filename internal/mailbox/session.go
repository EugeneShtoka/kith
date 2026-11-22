package mailbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"time"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// Protocol is the mesh's sync protocol (internal/mesh), which a mailbox speaks the
// reduced form of: a bridge speaking another is refused.
const Protocol = 3

// Frames is one sync stream as the mailbox sees it (mesh.Frames): a frame sent, a
// frame received (io.EOF once the bridge closed).
type Frames = interface {
	Send(*meshv1.SyncFrame) error
	Receive() (*meshv1.SyncFrame, error)
}

// The pace of a session.
const (
	frameItems   = 256
	compactEvery = time.Hour
)

// errProtocol is a bridge that does not follow the protocol.
var errProtocol = errors.New("mailbox: the bridge broke the sync protocol")

// told is what a session has sent the bridge of what is not envelopes past its vector:
// records and forks by hash, snapshots by number, and each other bridge's wants.
type told struct {
	records   map[[sha256.Size]byte]bool
	forks     map[[sha256.Size]byte]bool
	snapshots map[int64]bool
	wants     map[devicekey.ID]string
}

// Serve runs one session of the mailbox (self) with the bridge peer, admitted, over
// frames, until either side ends it, ctx ends or the key is revoked: Greet, then Pass
// whenever the store changes, and Take each frame the bridge sends. now is the
// mailbox's clock.
func (s *Store) Serve(ctx context.Context, self, peer devicekey.ID, frames Frames, now func() time.Time) error {
	ctx, end := context.WithCancelCause(ctx)
	defer end(nil)
	release := s.hold(peer, &end)
	defer release()
	if !s.Admits(peer) {
		return errRevoked
	}
	x, gerr := s.Greet(ctx, self, peer, frames, now())
	if gerr != nil {
		return gerr
	}
	in, failed := receiving(ctx, frames)
	for {
		changed := s.Changed()
		if err := x.Pass(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			if cause := context.Cause(ctx); !errors.Is(cause, context.Canceled) {
				return fmt.Errorf("mailbox: the session ended: %w", cause)
			}
			return nil
		case <-changed:
		case err := <-failed:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("mailbox: the sync stream: %w", err)
		case f := <-in:
			if err := x.Take(ctx, f, now()); err != nil {
				return err
			}
		}
	}
}

// Session is a sync session of the mailbox with a bridge past the Hellos, as Serve runs
// it: how far the bridge holds each origin (sent), what it has been told otherwise, a
// snapshot it is leaving, gathered so far, and whether it was passed all the mailbox
// held when the session began.
type Session struct {
	s         *Store
	peer      devicekey.ID
	frames    Frames
	sent      map[devicekey.ID]uint64
	told      told
	gathering *meshv1.SealedSnapshot
	caughtUp  bool
}

// hold tracks a session of peer's, so a revocation ends it.
func (s *Store) hold(peer devicekey.ID, end *context.CancelCauseFunc) (release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[peer] == nil {
		s.sessions[peer] = map[*context.CancelCauseFunc]bool{}
	}
	s.sessions[peer][end] = true
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.sessions[peer], end)
	}
}

// Greet reads the bridge peer's Hello on frames and answers it as self at now: the
// mailbox's vector, every epoch record it holds, its store's size and whether it wants
// a snapshot. The bridge's wants and records are kept.
func (s *Store) Greet(ctx context.Context, self, peer devicekey.ID, frames Frames, now time.Time) (*Session, error) {
	f, err := frames.Receive()
	if err != nil {
		return nil, fmt.Errorf("mailbox: read the bridge's hello: %w", err)
	}
	h := f.GetHello()
	switch {
	case h == nil:
		return nil, fmt.Errorf("%w: it did not say hello first", errProtocol)
	case !bytes.Equal(h.GetReplica(), peer[:]):
		return nil, fmt.Errorf("%w: it says it is another bridge", errProtocol)
	case h.GetProtocol() != Protocol:
		return nil, fmt.Errorf("%w: it speaks protocol %d, this mailbox %d; run one kith on both", errProtocol, h.GetProtocol(), Protocol)
	}
	theirs, err := seqsOf(h.GetVector())
	if err != nil {
		return nil, err
	}
	if err := s.putRecords(ctx, h.GetEpochs().GetRecords()); err != nil {
		return nil, err
	}
	s.want(peer, h.GetEpochs().GetWant())
	order, records := s.sortedRecords()
	epochs := &meshv1.Epochs{}
	for _, hash := range order {
		epochs.Records = append(epochs.Records, records[hash])
	}
	s.mu.Lock()
	vector, due := s.vectorLocked(), s.dueLocked(now)
	s.mu.Unlock()
	hello := &meshv1.Hello{
		Replica: bytes.Clone(self[:]), WallMs: now.UnixMilli(), Protocol: Protocol, Epochs: epochs,
		Mailbox: &meshv1.MailboxState{SnapshotDue: due, StoreBytes: uint64(max(0, s.Size()))}, // #nosec G115 -- clamped
	}
	for _, origin := range slices.SortedFunc(maps.Keys(vector), byOrigin) {
		hd := vector[origin]
		hello.Vector = append(hello.Vector, &meshv1.Head{Origin: bytes.Clone(origin[:]), Seq: hd.Seq, Hash: bytes.Clone(hd.Hash[:])})
	}
	if err := frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Hello{Hello: hello}}); err != nil {
		return nil, fmt.Errorf("mailbox: say hello: %w", err)
	}
	return &Session{s: s, peer: peer, frames: frames, sent: theirs, told: told{
		records: map[[sha256.Size]byte]bool{}, forks: map[[sha256.Size]byte]bool{}, snapshots: map[int64]bool{}, wants: map[devicekey.ID]string{},
	}}, nil
}

// Pass sends the bridge what it has not been told: records, forks and other bridges'
// wants, then what it lacks. The first ends with an Ack (the mailbox's vector), which
// says the bridge has now been passed all the mailbox held: a bridge recovering a mesh
// reads to there.
func (x *Session) Pass(ctx context.Context) error {
	s, peer, frames, t := x.s, x.peer, x.frames, &x.told
	order, records := s.sortedRecords()
	epochs := &meshv1.Epochs{}
	for _, hash := range order {
		if !t.records[hash] {
			t.records[hash] = true
			epochs.Records = append(epochs.Records, records[hash])
		}
	}
	if len(epochs.GetRecords()) > 0 {
		if err := frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Epochs{Epochs: epochs}}); err != nil {
			return fmt.Errorf("mailbox: send epoch records: %w", err)
		}
	}
	wants := s.othersWants(peer)
	for _, id := range slices.SortedFunc(maps.Keys(wants), byOrigin) {
		if text := fmt.Sprint(wants[id]); t.wants[id] != text {
			t.wants[id] = text
			e := &meshv1.Epochs{Want: wants[id], For: bytes.Clone(id[:])}
			if err := frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Epochs{Epochs: e}}); err != nil {
				return fmt.Errorf("mailbox: pass on what a bridge lacks: %w", err)
			}
		}
	}
	forks, ferr := s.forks(ctx)
	if ferr != nil {
		return ferr
	}
	var fresh [][]byte
	for _, hash := range slices.SortedFunc(maps.Keys(forks), func(a, b [sha256.Size]byte) int { return bytes.Compare(a[:], b[:]) }) {
		if !t.forks[hash] {
			t.forks[hash] = true
			fresh = append(fresh, forks[hash])
		}
	}
	if err := sendEnvelopes(frames, fresh, true); err != nil {
		return err
	}
	envelopes, snaps, upTo, err := s.missing(ctx, x.sent, t.snapshots)
	if err != nil {
		return err
	}
	for _, snap := range snaps {
		if err := sendSnapshot(frames, snap); err != nil {
			return err
		}
	}
	if err := sendEnvelopes(frames, envelopes, false); err != nil {
		return err
	}
	x.sent = upTo
	if !x.caughtUp {
		x.caughtUp = true
		return x.ack()
	}
	return nil
}

// Take handles one frame of the bridge's at now: envelopes and a snapshot's last chunk
// kept, then acknowledged; epoch records kept, and its wants; what it says of who is
// admitted; its Ack (how far it holds each origin).
func (x *Session) Take(ctx context.Context, f *meshv1.SyncFrame, now time.Time) error {
	s, peer, sent := x.s, x.peer, x.sent
	switch {
	case f.GetEnvelopes() != nil:
		if err := s.put(ctx, f.GetEnvelopes().GetEnvelopes(), now); err != nil {
			return err
		}
		for _, env := range f.GetEnvelopes().GetEnvelopes() {
			if dot, err := crdt.SealedDot(env); err == nil {
				sent[dot.Origin] = max(sent[dot.Origin], dot.Seq) // a bridge leaves only what it holds, and all before it
			}
		}
	case f.GetSealedSnapshot() != nil:
		if done, err := x.gather(ctx, f.GetSealedSnapshot(), now); err != nil || !done {
			return err
		}
	case f.GetEpochs() != nil:
		if err := s.putRecords(ctx, f.GetEpochs().GetRecords()); err != nil {
			return err
		}
		if len(f.GetEpochs().GetWant()) > 0 && len(f.GetEpochs().GetFor()) == 0 {
			s.want(peer, f.GetEpochs().GetWant())
		}
		return nil
	case f.GetAdmission() != nil:
		return s.admit(ctx, f.GetAdmission())
	case f.GetAck() != nil:
		held, err := seqsOf(f.GetAck().GetVector())
		if err != nil {
			return err
		}
		for origin, seq := range held {
			sent[origin] = max(sent[origin], seq)
		}
		return nil
	default:
		return fmt.Errorf("%w: a frame out of place", errProtocol)
	}
	return x.ack()
}

// ack sends the bridge the mailbox's vector: what it holds, durably.
func (x *Session) ack() error {
	x.s.mu.Lock()
	vector := x.s.vectorLocked()
	x.s.mu.Unlock()
	ack := &meshv1.Ack{}
	for _, origin := range slices.SortedFunc(maps.Keys(vector), byOrigin) {
		h := vector[origin]
		ack.Vector = append(ack.Vector, &meshv1.Head{Origin: bytes.Clone(origin[:]), Seq: h.Seq, Hash: bytes.Clone(h.Hash[:])})
	}
	if err := x.frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Ack{Ack: ack}}); err != nil {
		return fmt.Errorf("mailbox: acknowledge: %w", err)
	}
	return nil
}

// gather adds a chunk of the snapshot the bridge is leaving at now; at its last, the
// snapshot is kept, and done.
func (x *Session) gather(ctx context.Context, chunk *meshv1.SealedSnapshot, now time.Time) (done bool, err error) {
	if x.gathering == nil {
		x.gathering = &meshv1.SealedSnapshot{}
	}
	if x.gathering.Boxes = append(x.gathering.Boxes, chunk.GetBoxes()...); len(x.gathering.GetBoxes()) > maxBoxes {
		return false, fmt.Errorf("%w: a snapshot past the mailbox's bounds", errProtocol)
	}
	if !chunk.GetLast() {
		return false, nil
	}
	snap := x.gathering
	x.gathering = nil
	snap.Maker, snap.Vector, snap.Sig, snap.Last = chunk.GetMaker(), chunk.GetVector(), chunk.GetSig(), true
	if err := x.s.putSnapshot(ctx, snap, now); err != nil {
		return false, err
	}
	_, vector, _ := clearOf(snap) // putSnapshot read it
	for origin, seq := range vector {
		x.sent[origin] = max(x.sent[origin], seq)
	}
	return true, nil
}

// receiving reads frames on a goroutine of its own: each frame on in, then the error
// it ended with on failed.
func receiving(ctx context.Context, frames Frames) (in <-chan *meshv1.SyncFrame, failed <-chan error) {
	frame, fail := make(chan *meshv1.SyncFrame), make(chan error, 1)
	go func() {
		for {
			f, err := frames.Receive()
			if err != nil {
				fail <- err
				return
			}
			select {
			case frame <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	return frame, fail
}

// seqsOf is a vector's seqs, by origin.
func seqsOf(heads []*meshv1.Head) (map[devicekey.ID]uint64, error) {
	out := map[devicekey.ID]uint64{}
	for _, h := range heads {
		if len(h.GetOrigin()) != len(devicekey.ID{}) {
			return nil, fmt.Errorf("%w: a vector's head does not read", errProtocol)
		}
		out[devicekey.ID(h.GetOrigin())] = h.GetSeq()
	}
	return out, nil
}

// sendEnvelopes sends envelopes in frames of frameItems; forks marks them as forks.
func sendEnvelopes(frames Frames, envelopes [][]byte, forks bool) error {
	for len(envelopes) > 0 {
		n := min(len(envelopes), frameItems)
		f := &meshv1.Envelopes{Envelopes: envelopes[:n], Forks: forks}
		if err := frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Envelopes{Envelopes: f}}); err != nil {
			return fmt.Errorf("mailbox: send envelopes: %w", err)
		}
		envelopes = envelopes[n:]
	}
	return nil
}

// sendSnapshot sends snap a box per frame, its maker, vector and signature with the
// last.
func sendSnapshot(frames Frames, snap *meshv1.SealedSnapshot) error {
	boxes := snap.GetBoxes()
	for i, box := range boxes {
		chunk := &meshv1.SealedSnapshot{Boxes: [][]byte{box}}
		if i == len(boxes)-1 {
			chunk.Maker, chunk.Vector, chunk.Sig, chunk.Last = snap.GetMaker(), snap.GetVector(), snap.GetSig(), true
		}
		if err := frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_SealedSnapshot{SealedSnapshot: chunk}}); err != nil {
			return fmt.Errorf("mailbox: send a snapshot: %w", err)
		}
	}
	return nil
}

// Compacting compacts the store every compactEvery until ctx ends.
func (s *Store) Compacting(ctx context.Context, log *slog.Logger) {
	ticker := time.NewTicker(compactEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.Compact(ctx, now); err != nil {
				log.Warn("compacting the mailbox failed", "err", err)
			}
		}
	}
}
