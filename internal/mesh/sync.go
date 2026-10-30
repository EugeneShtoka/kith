package mesh

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"time"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/human"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// Frames is one sync stream as either side sees it: a frame sent, a frame received
// (io.EOF once the other side closed). Sync never sends from two goroutines at once.
type Frames = interface {
	Send(*meshv1.SyncFrame) error
	Receive() (*meshv1.SyncFrame, error)
}

// protocol is the sync protocol this kith speaks; a peer speaking another is refused.
const protocol = 1

// The pace of a session.
const (
	// framesItems bounds the ops or entries one frame carries.
	frameItems = 256
	// tick is how often a session tries the held ops again and sees whether the log
	// is due a compaction.
	tick = time.Second
	// compactEvery is how often the log is compacted.
	compactEvery = time.Hour
)

// Why a session ends without error from the stream.
var (
	// ErrStreamOpen is a second stream with a peer: of two, the one dialed by the lower
	// ID is kept, so both sides keep the same one.
	ErrStreamOpen = errors.New("mesh: a sync stream with that bridge is open already")
	// ErrNotABridge is a peer that is not a bridge of the roster (never, or no longer),
	// or one found forked.
	ErrNotABridge = errors.New("mesh: the peer is not a bridge of the mesh")
	// errProtocol is a peer that does not follow the protocol.
	errProtocol = errors.New("mesh: the peer broke the sync protocol")
)

// ClockGapError is a peer whose wall clock is further off this bridge's than
// MaxClockGap: the session is refused, the gap named.
type ClockGapError struct {
	Peer devicekey.ID
	Gap  time.Duration // its clock less this bridge's
}

func (e ClockGapError) Error() string {
	way, gap := "ahead of", e.Gap
	if gap < 0 {
		way, gap = "behind", -gap
	}
	return fmt.Sprintf("mesh: %s's clock is %s %s this bridge's (at most %s either way); set both clocks right",
		e.Peer, human.Duration(gap), way, human.Duration(MaxClockGap))
}

// session is an open sync stream with a peer: who dialed it, and how to end it.
type session struct {
	dialer devicekey.ID
	end    context.CancelCauseFunc
}

// claim holds the one stream with peer for a session dialed by dialer: a stream dialed
// by the lower ID wins over one dialed by the higher (both sides choose alike), and a
// newer stream dialed by the same side replaces an older one (a dead connection not
// yet noticed). release gives it up.
func (s *Store) claim(peer, dialer devicekey.ID, end context.CancelCauseFunc) (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.sessions[peer]; old != nil {
		if bytes.Compare(dialer[:], old.dialer[:]) > 0 {
			return nil, ErrStreamOpen
		}
		old.end(ErrStreamOpen)
	}
	mine := &session{dialer: dialer, end: end}
	s.sessions[peer] = mine
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.sessions[peer] == mine {
			delete(s.sessions, peer)
		}
	}, nil
}

// Connected reports whether a sync stream with peer is open.
func (s *Store) Connected(peer devicekey.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[peer] != nil
}

// Sync runs one sync session of this bridge (signer, its device key) with the bridge
// peer over frames, until either side ends it, ctx ends, or the peer is removed from
// the mesh (stillPaired); dialed reports whether this side dialed. A Hello each way (a
// clock more than MaxClockGap off refuses it), then each side sends the ops the other
// lacks, or a snapshot, then every op it commits after; each batch applied is
// acknowledged. An origin found to have signed two ops under one dot is removed from
// the roster, signed by this bridge.
func (s *Store) Sync(ctx context.Context, signer crypto.Signer, peer devicekey.ID, dialed bool, frames Frames, log *slog.Logger) error {
	self, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	ctx, end := context.WithCancelCause(ctx)
	defer end(nil)
	if perr := s.stillPaired(peer); perr != nil {
		return perr
	}
	theirs, err := s.greet(ctx, self, peer, frames, time.Now())
	if err != nil {
		return err
	}
	// A stream is held once both sides have said Hello, so a dial still failing holds
	// none; a bridge joining (it holds nothing yet) only takes a snapshot, and holds
	// none either.
	if len(theirs) > 0 {
		dialer := peer
		if dialed {
			dialer = self
		}
		release, cerr := s.claim(peer, dialer, end)
		if cerr != nil {
			return cerr
		}
		defer release()
	}
	if err := sendOps(frames, s.headProofs(ctx, theirs)); err != nil {
		return err
	}
	in, failed := receiving(ctx, frames)
	return s.serve(ctx, syncing{signer: signer, peer: peer, frames: frames, log: log}, theirs, in, failed)
}

// receiving reads frames on a goroutine of its own: each frame on in, then the error
// it ended with on failed. It ends when the stream does, which Sync's caller closes
// once Sync returns.
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

// syncing is what serve needs of one sync stream.
type syncing struct {
	signer crypto.Signer
	peer   devicekey.ID
	frames Frames
	log    *slog.Logger
}

// serve is a session past its Hellos: it sends the peer what it lacks past sent, and
// again after every commit, and takes each frame the peer sends, until either ends.
func (s *Store) serve(ctx context.Context, ss syncing, sent crdt.Vector, in <-chan *meshv1.SyncFrame, failed <-chan error) error {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	var snapshot *crdt.Snapshot
	for {
		changed := s.Changed()
		err := s.stillPaired(ss.peer)
		if err != nil {
			return err
		}
		if sent, err = s.sendMissing(ctx, ss.frames, sent); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ended(ctx)
		case <-changed:
		case now := <-ticker.C:
			if _, err = s.receive(ctx, nil, now); err != nil {
				return err
			}
			s.compactIfDue(ctx, now, ss.log)
		case err = <-failed:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("mesh: the sync stream: %w", err)
		case f := <-in:
			if snapshot, err = s.take(ctx, ss.signer, ss.peer, f, snapshot, ss.frames, ss.log, time.Now()); err != nil {
				return err
			}
		}
	}
}

// ended is why a session's context ended: none when its caller ended it, the reason
// when the session was ended for one (another stream kept).
func ended(ctx context.Context) error {
	if cause := context.Cause(ctx); !errors.Is(cause, context.Canceled) {
		return fmt.Errorf("mesh: the session ended: %w", cause)
	}
	return nil
}

// stillPaired refuses a session once the peer is not a bridge of the roster: removed,
// or found forked. This bridge removed is no reason: it syncs on with every peer that
// still admits it, so the news of its removal (held perhaps by it alone, written by a
// bridge now refused everywhere) reaches each of them, who then refuse it. Its own
// writes since it learned of the removal are refused everywhere anyway.
func (s *Store) stillPaired(peer devicekey.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	roster, err := s.state.Roster()
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	if m, ok := roster[peer]; !ok || m.Removed || m.Role != crdt.RoleBridge || s.forked[peer] {
		return fmt.Errorf("%w: %s", ErrNotABridge, peer)
	}
	return nil
}

// greet says Hello and reads the peer's: it must be the peer the stream authenticated,
// speak this protocol and keep a clock within MaxClockGap of this bridge's. Its vector
// is returned, and kept as what it said last. now is this bridge's clock.
func (s *Store) greet(ctx context.Context, self, peer devicekey.ID, frames Frames, now time.Time) (crdt.Vector, error) {
	if err := frames.Send(helloFrame(self, now, s.vector())); err != nil {
		return nil, fmt.Errorf("mesh: say hello: %w", err)
	}
	theirs, gap, err := readHello(frames, peer, now)
	if err != nil {
		return nil, err
	}
	if gap > MaxClockGap || gap < -MaxClockGap {
		_ = s.heard(ctx, peer, nil, gap, now) // the gap is what kith mesh shows; the refusal is the error
		return nil, ClockGapError{Peer: peer, Gap: gap}
	}
	if err := s.heard(ctx, peer, theirs, gap, now); err != nil {
		return nil, err
	}
	return theirs, nil
}

// readHello reads the peer's Hello: its vector, and its clock less this bridge's (now).
func readHello(frames Frames, peer devicekey.ID, now time.Time) (crdt.Vector, time.Duration, error) {
	f, err := frames.Receive()
	if err != nil {
		return nil, 0, fmt.Errorf("mesh: read the peer's hello: %w", err)
	}
	h := f.GetHello()
	switch {
	case h == nil:
		return nil, 0, fmt.Errorf("%w: it did not say hello first", errProtocol)
	case !bytes.Equal(h.GetReplica(), peer[:]):
		return nil, 0, fmt.Errorf("%w: it says it is another bridge", errProtocol)
	case h.GetProtocol() != protocol:
		return nil, 0, fmt.Errorf("%w: it speaks protocol %d, this kith %d; run one kith on both", errProtocol, h.GetProtocol(), protocol)
	}
	vector, err := vectorOf(h.GetVector())
	if err != nil {
		return nil, 0, err
	}
	return vector, time.UnixMilli(h.GetWallMs()).Sub(now), nil
}

// sendMissing sends the peer what it lacks past sent (missing), and returns what it
// holds then.
func (s *Store) sendMissing(ctx context.Context, frames Frames, sent crdt.Vector) (crdt.Vector, error) {
	ops, snap, upTo, err := s.missing(ctx, sent)
	if err != nil {
		return nil, err
	}
	switch {
	case snap != nil:
		err = sendSnapshot(frames, *snap)
	case len(ops) > 0:
		err = sendOps(frames, ops)
	default:
		return sent, nil
	}
	if err != nil {
		return nil, err
	}
	out := maps.Clone(sent)
	if out == nil {
		out = crdt.Vector{}
	}
	for origin, h := range upTo {
		if h.Seq > out[origin].Seq {
			out[origin] = h
		}
	}
	return out, nil
}

// take handles one frame of the peer's at now (this bridge's clock): ops applied and
// acknowledged, snapshot chunks gathered until the last, then merged and acknowledged,
// an Ack kept. A fork found is answered with this replica's op at its dot, and its
// origin removed (signed by signer). It returns the snapshot gathered so far.
func (s *Store) take(ctx context.Context, signer crypto.Signer, peer devicekey.ID, f *meshv1.SyncFrame, snapshot *crdt.Snapshot,
	frames Frames, log *slog.Logger, now time.Time,
) (*crdt.Snapshot, error) {
	var res received
	var err error
	switch {
	case f.GetOps() != nil:
		ops, perr := opsOf(f.GetOps().GetOps())
		if perr != nil {
			return nil, perr
		}
		res, err = s.receive(ctx, ops, now)
	case f.GetSnapshot() != nil:
		if snapshot, err = gather(snapshot, f.GetSnapshot()); err != nil || !f.GetSnapshot().GetLast() {
			return snapshot, err
		}
		res, err = s.merge(ctx, *snapshot, now)
		snapshot = nil
	case f.GetAck() != nil:
		vector, verr := vectorOf(f.GetAck().GetVector())
		if verr != nil {
			return nil, verr
		}
		return snapshot, s.heard(ctx, peer, vector, s.gapOf(peer), now)
	default:
		return nil, fmt.Errorf("%w: a frame out of place", errProtocol)
	}
	if err != nil {
		return nil, err
	}
	for _, why := range res.refused {
		log.Warn("refused an op a peer sent", "peer", peer.String(), "err", why)
	}
	if err := frames.Send(ackFrame(s.vector())); err != nil {
		return nil, fmt.Errorf("mesh: acknowledge: %w", err)
	}
	s.removeForked(ctx, signer, res.forked, log)
	return snapshot, sendOps(frames, res.proofs)
}

// gapOf is the clock gap peer showed last.
func (s *Store) gapOf(peer devicekey.ID) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peers[peer].gap
}

// vector is this replica's vector now.
func (s *Store) vector() crdt.Vector {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.state.Vector)
}

// compactIfDue compacts the log once per compactEvery, and says so when the store has
// grown past sizeWarning.
func (s *Store) compactIfDue(ctx context.Context, now time.Time, log *slog.Logger) {
	s.mu.Lock()
	due := now.Sub(s.compacted) >= compactEvery
	if due {
		s.compacted = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	if err := s.compact(ctx, now); err != nil {
		log.Warn("compacting the mesh store failed", "err", err)
	}
	if size := s.size(); size > sizeWarning {
		log.Warn("the mesh store is large", "path", s.path, "bytes", size)
	}
}

// Join makes this bridge (self, signing with signer) one of the mesh of sponsor, a
// member bridge that has just confirmed its pairing, over frames: it says Hello holding
// nothing, so the sponsor sends a snapshot, and adopts it (adopt). It returns the
// mesh's members as this bridge admits them now.
func (s *Store) Join(ctx context.Context, self, sponsor devicekey.ID, signer crypto.Signer, frames Frames) ([]domain.Device, error) {
	if err := frames.Send(helloFrame(self, time.Now(), nil)); err != nil {
		return nil, fmt.Errorf("mesh: say hello: %w", err)
	}
	_, gap, err := readHello(frames, sponsor, time.Now())
	if err != nil {
		return nil, err
	}
	if gap > MaxClockGap || gap < -MaxClockGap {
		return nil, ClockGapError{Peer: sponsor, Gap: gap}
	}
	snapshot, err := awaitSnapshot(ctx, frames)
	if err != nil {
		return nil, err
	}
	members, err := s.adopt(ctx, *snapshot, signer, self)
	if err != nil {
		return nil, err
	}
	_ = frames.Send(ackFrame(s.vector())) // the sponsor learns it at the next sync otherwise
	return members, nil
}

// awaitSnapshot gathers the snapshot the sponsor of a join sends, to its last chunk.
// Ops it sends meanwhile are passed over: a bridge joining holds nothing to apply them
// over yet.
func awaitSnapshot(ctx context.Context, frames Frames) (*crdt.Snapshot, error) {
	var snapshot *crdt.Snapshot
	for ctx.Err() == nil {
		f, err := frames.Receive()
		if err != nil {
			return nil, fmt.Errorf("mesh: join: the mesh's snapshot: %w", err)
		}
		if f.GetSnapshot() == nil {
			continue
		}
		if snapshot, err = gather(snapshot, f.GetSnapshot()); err != nil || f.GetSnapshot().GetLast() {
			return snapshot, err
		}
	}
	return nil, fmt.Errorf("mesh: join: %w", ctx.Err())
}

// ── The wire ──

func helloFrame(self devicekey.ID, now time.Time, vector crdt.Vector) *meshv1.SyncFrame {
	return &meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Hello{Hello: &meshv1.Hello{
		Replica: self[:], WallMs: now.UnixMilli(), Vector: headsOf(vector), Protocol: protocol,
	}}}
}

func ackFrame(vector crdt.Vector) *meshv1.SyncFrame {
	return &meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Ack{Ack: &meshv1.Ack{Vector: headsOf(vector)}}}
}

// sendOps sends ops in frames of frameItems, in order.
func sendOps(frames Frames, ops []crdt.Op) error {
	for len(ops) > 0 {
		n := min(len(ops), frameItems)
		f := &meshv1.Ops{Ops: wireOps(ops[:n])}
		if err := frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Ops{Ops: f}}); err != nil {
			return fmt.Errorf("mesh: send ops: %w", err)
		}
		ops = ops[n:]
	}
	return nil
}

// sendSnapshot sends snap in chunks of frameItems: the entries, then the roster's ops,
// then the vector and clock with the last.
func sendSnapshot(frames Frames, snap crdt.Snapshot) error {
	var entries []*meshv1.Entry
	for key, es := range snap.Entries {
		for _, e := range es {
			entries = append(entries, &meshv1.Entry{
				Key: key, Value: e.Value, Origin: bytes.Clone(e.Dot.Origin[:]), Seq: e.Dot.Seq, Clock: uint64(e.Clock),
			})
		}
	}
	roster := wireOps(snap.RosterOps)
	for {
		chunk := &meshv1.Snapshot{}
		n := min(len(entries), frameItems)
		chunk.Entries, entries = entries[:n], entries[n:]
		n = min(len(roster), frameItems-len(chunk.GetEntries()))
		chunk.Roster, roster = roster[:n], roster[n:]
		if len(entries)+len(roster) == 0 {
			chunk.Vector, chunk.Clock, chunk.Last = headsOf(snap.Vector), uint64(snap.Clock), true
		}
		if err := frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Snapshot{Snapshot: chunk}}); err != nil {
			return fmt.Errorf("mesh: send a snapshot: %w", err)
		}
		if chunk.GetLast() {
			return nil
		}
	}
}

// gather adds a snapshot chunk to what was gathered so far.
func gather(so *crdt.Snapshot, chunk *meshv1.Snapshot) (*crdt.Snapshot, error) {
	if so == nil {
		so = &crdt.Snapshot{Entries: map[string][]crdt.Entry{}}
	}
	for _, e := range chunk.GetEntries() {
		if len(e.GetOrigin()) != len(devicekey.ID{}) {
			return nil, fmt.Errorf("%w: a snapshot entry names no origin", errProtocol)
		}
		so.Entries[e.GetKey()] = append(so.Entries[e.GetKey()], crdt.Entry{
			Value: e.GetValue(), Dot: crdt.Dot{Origin: devicekey.ID(e.GetOrigin()), Seq: e.GetSeq()}, Clock: crdt.Clock(e.GetClock()),
		})
	}
	ops, err := opsOf(chunk.GetRoster())
	if err != nil {
		return nil, err
	}
	so.RosterOps = append(so.RosterOps, ops...)
	if chunk.GetLast() {
		if so.Vector, err = vectorOf(chunk.GetVector()); err != nil {
			return nil, err
		}
		so.Clock = crdt.Clock(chunk.GetClock())
	}
	return so, nil
}

func headsOf(v crdt.Vector) []*meshv1.Head {
	out := make([]*meshv1.Head, 0, len(v))
	for origin, h := range v {
		out = append(out, &meshv1.Head{Origin: bytes.Clone(origin[:]), Seq: h.Seq, Hash: bytes.Clone(h.Hash[:])})
	}
	return out
}

func vectorOf(heads []*meshv1.Head) (crdt.Vector, error) {
	v := make(crdt.Vector, len(heads))
	for _, h := range heads {
		var head crdt.Head
		if len(h.GetOrigin()) != len(devicekey.ID{}) || len(h.GetHash()) != len(head.Hash) {
			return nil, fmt.Errorf("%w: a vector's head does not read", errProtocol)
		}
		head.Seq, head.Hash = h.GetSeq(), [len(head.Hash)]byte(h.GetHash())
		v[devicekey.ID(h.GetOrigin())] = head
	}
	return v, nil
}

func wireOps(ops []crdt.Op) []*meshv1.Op {
	out := make([]*meshv1.Op, 0, len(ops))
	for _, op := range ops {
		out = append(out, &meshv1.Op{Body: op.Body(), Sig: op.Sig})
	}
	return out
}

func opsOf(wire []*meshv1.Op) ([]crdt.Op, error) {
	out := make([]crdt.Op, 0, len(wire))
	for _, w := range wire {
		op, err := crdt.ParseOp(w.GetBody(), w.GetSig())
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errProtocol, err)
		}
		out = append(out, op)
	}
	return out, nil
}
