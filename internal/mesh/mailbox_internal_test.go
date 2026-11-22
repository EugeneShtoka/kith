package mesh

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mailbox"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// TestAMailboxCarriesTheMeshOverAnyInterleaving is step 9's failure review: the
// rotation review's generator, run with the bridges never meeting. Every sync between
// two bridges goes through one of one or two mailboxes (a bridge leaves and picks up
// there, then the other), and the mailboxes store and forward blindly and badly: they
// withhold envelopes, reorder, duplicate and replay them, move a box to another dot,
// change a byte of one, splice a snapshot's box from another, say a snapshot covers
// more than its maker signed, lose the epoch records they were to pass on, lose
// everything (and are paired again), and roll back to an old copy of themselves, which
// serves an old snapshot. A bridge's word of who is admitted is lost at times, so a
// removal never reaches a mailbox. A fork reaches one mailbox from two bridges when the forger and the replica handed its
// twin both leave it there. It holds the rotation review's invariants and: nothing a bridge sends a
// mailbox carries an op, a snapshot or a body's hash in the clear; a mailbox that took
// a bridge's word of a removal admits the removed key no more; and once settled, every
// bridge of the mesh holds the same entries though none ever met another but to join.
func TestAMailboxCarriesTheMeshOverAnyInterleaving(t *testing.T) {
	t.Parallel()
	runModel(t, true, true, 2)
}

// modelMailbox is one mailbox of the model: its store, its key, and an old copy of
// its file to roll back to.
type modelMailbox struct {
	store *mailbox.Store
	key   *ecdsa.PrivateKey
	id    devicekey.ID
	path  string
	dbKey []byte
	saved string
}

// addMailboxes makes up to n mailboxes, one at least, and has every bridge pair with
// each: each bridge writes the mailbox's entry itself, as a bridge the others never
// meet must (kith mailbox add on each).
func (m *model) addMailboxes(n int) {
	for k := range 1 + m.rng.IntN(n) {
		key, id := newDeviceID(m.t)
		dir := m.t.TempDir()
		mb := &modelMailbox{key: key, id: mustID(m.t, id), path: filepath.Join(dir, "mailbox.db"), dbKey: make([]byte, sealeddb.KeyLen), saved: filepath.Join(dir, "saved.db")}
		if _, err := rand.Read(mb.dbKey); err != nil {
			m.t.Fatal(err)
		}
		mb.store = m.openMailbox(mb)
		for _, r := range m.r {
			m.must(r.store.AddMailbox(m.ctx, r.key, domain.Device{ID: id, Name: fmt.Sprint("mailbox", k)}, []string{"192.0.2.9:7444"}))
			m.must(mb.store.Admit(m.ctx, r.id))
		}
		m.mb = append(m.mb, mb)
	}
}

func (m *model) openMailbox(mb *modelMailbox) *mailbox.Store {
	s, err := mailbox.Open(m.ctx, mb.path, mb.dbKey)
	m.must(err)
	m.t.Cleanup(func() { _ = s.Close() })
	return s
}

// through is i and j syncing through a mailbox, never meeting: i leaves and picks up
// what it holds, then j, then i again.
func (m *model) through(i, j int, faults bool) {
	k := m.rng.IntN(len(m.mb))
	m.post(i, k, faults)
	m.post(j, k, faults)
	m.post(i, k, faults)
}

// postAll has every bridge sync with every mailbox, twice over: once to leave what it
// holds, once to pick up what the others left.
func (m *model) postAll() {
	for range 2 {
		for k := range m.mb {
			for i := range m.r {
				m.post(i, k, false)
			}
		}
	}
}

// post is one session of bridge i with mailbox k, as SyncMailbox and Serve run it: the
// mailbox refusing a key it does not admit (the bridge falling back to the key it
// rotated from, as kithd does), a Hello each way, the bridge's word of who is admitted,
// then what each lacks, every frame answered as it arrives. With faults, frames are
// lost, duplicated, reordered and tampered with, and the session cut.
func (m *model) post(i, k int, faults bool) {
	r, mb := m.r[i], m.mb[k]
	if i == m.dormant || r.store.stillListed(mb.id) != nil {
		return
	}
	as := r.id
	if !mb.store.Admits(as) && r.was != nil && mb.store.Admits(r.wasID) {
		as = r.wasID
	}
	if !mb.store.Admits(as) {
		return // refused at the handshake
	}
	toM, toB := &queue{}, &queue{}
	var x *mailbox.Session
	var errM error
	ss := syncing{signer: r.key, as: as, peer: mb.id, log: m.log, told: newTold()}
	ss.frames = &turns{out: toM, in: toB, check: m.checkBlind, fill: func() {
		x, errM = mb.store.Greet(m.ctx, mb.id, as, &turns{out: toB, in: toM}, m.now)
	}}
	theirs, due, errB := r.store.greetMailbox(m.ctx, ss, m.at(i))
	if errB != nil || errM != nil {
		m.t.Fatalf("bridge %d and mailbox %d say hello: %v, %v", i, k, errB, errM)
	}
	sent, err := r.store.leave(m.ctx, ss, theirs, due)
	m.must(err)
	admitted, err := r.store.admit(ss.frames, nil)
	m.must(err)
	m.must(x.Pass(m.ctx))
	revoked := true // every word the bridge sent of who is admitted reached the mailbox
	var gathering *meshv1.SealedSnapshot
	for len(toM.frames)+len(toB.frames) > 0 {
		if len(toM.frames) > 0 {
			f := toM.frames[0]
			toM.frames = toM.frames[1:]
			deliver := true
			if faults {
				switch n := m.rng.IntN(100); {
				case n < 2:
					return
				case n < 40 && f.GetAdmission() != nil:
					deliver, revoked = false, false // a removal that never reaches the mailbox
				case n < 10:
					toM.frames = slices.Insert(toM.frames, m.rng.IntN(len(toM.frames)+1), cloneFrame(f))
				}
			}
			if deliver {
				m.must(x.Take(m.ctx, f, m.now))
				m.must(x.Pass(m.ctx))
			}
		}
		if len(toB.frames) > 0 {
			f := toB.frames[0]
			toB.frames = toB.frames[1:]
			if faults {
				switch m.mailboxFault(f, toB) {
				case cutHere:
					return
				case passOn:
					continue
				case deliver:
				}
			}
			before := m.dots(i)
			gathering, err = r.store.takeFromMailbox(m.ctx, ss, f, gathering, sent, m.at(i))
			m.must(err)
			m.checkNothingFromTheFuture(i, before)
			if e := r.store.tell(ss); e != nil {
				m.must(ss.frames.Send(&meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Epochs{Epochs: e}}))
			}
			sent, err = r.store.leave(m.ctx, ss, sent, false)
			m.must(err)
			admitted, err = r.store.admit(ss.frames, admitted)
			m.must(err)
		}
	}
	if revoked {
		m.checkRevoked(i, k)
	}
}

// mailboxFault does what a mailbox, or the way from it, may do to frame f on its way to
// a bridge, the rest of the session's frames in in.
func (m *model) mailboxFault(f *meshv1.SyncFrame, in *queue) fate {
	envelopes := f.GetEnvelopes().GetEnvelopes()
	switch n := m.rng.IntN(100); {
	case n < 2:
		return cutHere
	case n < 8 && len(envelopes) > 0:
		return passOn // withheld
	case n < 14:
		in.frames = slices.Insert(in.frames, m.rng.IntN(len(in.frames)+1), cloneFrame(f)) // again, later
	case n < 20 && len(envelopes) > 1:
		var split []*meshv1.SyncFrame
		for _, env := range envelopes {
			split = append(split, &meshv1.SyncFrame{Frame: &meshv1.SyncFrame_Envelopes{Envelopes: &meshv1.Envelopes{Envelopes: [][]byte{env}}}})
		}
		m.rng.Shuffle(len(split), func(x, y int) { split[x], split[y] = split[y], split[x] })
		in.frames = append(split, in.frames...)
		return passOn
	case n < 23 && len(envelopes) > 0:
		// Moved to another dot: a byte of its clear head changed.
		moved := cloneFrame(f)
		env := moved.GetEnvelopes().GetEnvelopes()[0]
		env[len("kith mesh sealed 1\n")+m.rng.IntN(len(devicekey.ID{})+2)] ^= 1
		in.frames = append([]*meshv1.SyncFrame{moved}, in.frames...)
		return passOn
	case n < 26 && len(envelopes) > 0:
		tampered := cloneFrame(f)
		env := tampered.GetEnvelopes().GetEnvelopes()[0]
		env[m.rng.IntN(len(env))] ^= 1
		in.frames = append([]*meshv1.SyncFrame{tampered}, in.frames...)
		return passOn
	case n < 32 && f.GetSealedSnapshot().GetLast():
		box := f.GetSealedSnapshot().GetBoxes()[0]
		if m.spliced != nil && !bytes.Equal(m.spliced, box) {
			// A box of another snapshot in its place.
			spliced := cloneFrame(f)
			spliced.GetSealedSnapshot().GetBoxes()[0] = bytes.Clone(m.spliced)
			in.frames = append([]*meshv1.SyncFrame{spliced}, in.frames...)
			m.spliced = box
			return passOn
		}
		m.spliced = box
	case n < 38 && f.GetEpochs() != nil:
		return passOn // the records it was to pass on, and the wants, lost
	case n < 42 && len(f.GetSealedSnapshot().GetVector()) > 0:
		// It says the snapshot covers more than its maker signed.
		lied := cloneFrame(f)
		lied.GetSealedSnapshot().GetVector()[0].Seq++
		in.frames = append([]*meshv1.SyncFrame{lied}, in.frames...)
		return passOn
	}
	return deliver
}

// mailboxStep is one of what only the mailbox review generates between sessions: a
// mailbox loses everything and is paired again with every bridge of the mesh, rolls
// back to the copy of itself kept last, keeps a copy, or compacts.
func (m *model) mailboxStep() {
	mb := m.mb[m.rng.IntN(len(m.mb))]
	switch n := m.rng.IntN(6); {
	case n < 1:
		m.must(mb.store.Close())
		m.must(os.Remove(mb.path))
		for _, ext := range []string{"-wal", "-shm"} {
			_ = os.Remove(mb.path + ext) // none, once closed, most likely
		}
		mb.store = m.openMailbox(mb)
		for i, r := range m.r {
			if m.member(i) && m.lists(0, i) {
				m.must(mb.store.Admit(m.ctx, r.id))
			}
		}
	case n < 2:
		if _, err := os.Stat(mb.saved); err != nil {
			return
		}
		m.must(mb.store.Close())
		m.copyFile(mb.saved, mb.path)
		mb.store = m.openMailbox(mb)
	case n < 4:
		m.must(mb.store.Close())
		m.copyFile(mb.path, mb.saved)
		mb.store = m.openMailbox(mb)
	default:
		m.must(mb.store.Compact(m.ctx, m.now))
	}
}

func (m *model) copyFile(from, to string) {
	body, err := os.ReadFile(from) // #nosec G304 -- the test's own files
	m.must(err)
	m.must(os.WriteFile(to, body, 0o600))
	for _, ext := range []string{"-wal", "-shm"} {
		_ = os.Remove(to + ext) // a closed store left none, most likely
	}
}

// checkBlind holds that a frame a bridge sends a mailbox carries nothing in the clear
// but what the mailbox needs: no op, no snapshot, and no vector with the hash of an op's
// body, which would let it confirm a guess at what the op says.
func (m *model) checkBlind(f *meshv1.SyncFrame) {
	m.t.Helper()
	var heads []*meshv1.Head
	switch {
	case f.GetOps() != nil, f.GetSnapshot() != nil:
		m.t.Fatalf("a bridge sent a mailbox %T in the clear", f.GetFrame())
	case f.GetHello() != nil:
		heads = f.GetHello().GetVector()
	case f.GetAck() != nil:
		heads = f.GetAck().GetVector()
	case f.GetSealedSnapshot() != nil:
		heads = f.GetSealedSnapshot().GetVector()
	}
	for _, h := range heads {
		if !bytes.Equal(h.GetHash(), make([]byte, len(h.GetHash()))) {
			m.t.Fatal("a bridge told a mailbox the hash of an op's body")
		}
	}
}

// checkRevoked holds that mailbox k, told by bridge i who is admitted, admits no key
// that i holds removed as a bridge.
func (m *model) checkRevoked(i, k int) {
	r := m.r[i].store
	r.mu.Lock()
	roster, err := r.state.Roster()
	r.mu.Unlock()
	m.must(err)
	for id := range roster {
		if roster[id].Role == crdt.RoleBridge && roster[id].Removed && m.mb[k].store.Admits(id) {
			m.t.Fatalf("mailbox %d, told by replica %d of %s's removal, admits it still", k, i, id)
		}
	}
}

// turns is one side of a session in the model: what it sends goes to out, each frame
// checked first when check is set; what it receives comes from in, and when in is
// empty, fill runs: the other side's turn.
type turns struct {
	out, in *queue
	check   func(*meshv1.SyncFrame)
	fill    func()
}

func (t *turns) Send(f *meshv1.SyncFrame) error {
	if t.check != nil {
		t.check(f)
	}
	return t.out.Send(f)
}

func (t *turns) Receive() (*meshv1.SyncFrame, error) {
	if len(t.in.frames) == 0 && t.fill != nil {
		t.fill()
	}
	if len(t.in.frames) == 0 {
		return nil, io.EOF
	}
	return t.in.Receive()
}

// A bridge never syncs a mailbox its roster does not list, and a mailbox lets in only a
// key it admits.
func TestAMailboxIsListedAndAdmits(t *testing.T) {
	t.Parallel()
	m := newModel(t, 0)
	m.mailboxing = true
	m.addMailboxes(1)
	mb, a := m.mb[0], m.r[0]
	if err := a.store.stillListed(mb.id); err != nil {
		t.Fatalf("the mailbox each bridge added: %v", err)
	}
	_, id := newDeviceID(t)
	if mb.store.Admits(mustID(t, id)) {
		t.Fatal("a key no bridge admitted is admitted")
	}
	m.must(a.store.ChangeRoster(m.ctx, a.key, nil, []string{mb.id.String()}))
	if err := a.store.stillListed(mb.id); err == nil {
		t.Fatal("a mailbox removed from the roster is still synced with")
	}
}

// A fork whose halves two bridges hold, never meeting, is found through a mailbox: the
// second to leave its half there sees the mailbox's head is another op, leaves its own
// as proof, and every bridge is told of both.
func TestAForkIsFoundThroughAMailbox(t *testing.T) {
	t.Parallel()
	m := newModel(t, 1)
	m.mailboxing = true
	m.addMailboxes(1)
	m.postAll()
	m.equivocate()
	forger := m.r[m.forger]
	twin := -1
	for i := range m.r {
		if i != m.forger && m.dots(i)[crdt.Dot{Origin: forger.id, Seq: m.forkSeq}] {
			twin = i
		}
	}
	if twin < 0 {
		t.Fatal("no replica took the twin")
	}
	m.post(m.forger, 0, false) // the mailbox's head for the forger is its own op now
	m.post(twin, 0, false)     // which this replica holds otherwise: it leaves its half
	for i := range m.r {
		m.post(i, 0, false)
	}
	for i, r := range m.r {
		r.store.mu.Lock()
		found := r.store.forked[forger.id]
		r.store.mu.Unlock()
		if i != m.forger && !found {
			t.Errorf("replica %d never found the fork", i)
		}
	}
}

// A snapshot sealed under an epoch a removed bridge still holds the key of, naming a
// bridge of the mesh as its maker, is refused: its boxes open, but the signature is
// not its maker's.
func TestASnapshotNotSignedByItsMakerIsRefused(t *testing.T) {
	t.Parallel()
	m := newModel(t, 0)
	a, b, gone := m.r[0], m.r[1], m.r[2]
	gone.store.mu.Lock()
	roster, err := gone.store.state.Roster()
	m.must(err)
	e, ok := gone.store.keys.current(gone.store.state, roster)
	key := gone.store.keys.keys[e].key
	snap := gone.store.state.Snapshot(gone.store.clock)
	gone.store.mu.Unlock()
	if !ok {
		t.Fatal("the bridge to be removed writes under no epoch")
	}
	m.must(a.store.ChangeRoster(m.ctx, a.key, nil, []string{gone.id.String()}))
	m.exchange(0, 1, false)
	plain, err := proto.Marshal(snapshotFrame(snap))
	m.must(err)
	boxes, err := crdt.SealSnapshot(plain, a.id, e, key)
	m.must(err)
	clear := snap.Vector.Context()
	sig, err := crdt.SignSnapshot(gone.key, crdt.SnapshotDigest(a.id, clear, boxes))
	m.must(err)
	forged := &meshv1.SealedSnapshot{Boxes: boxes, Maker: bytes.Clone(a.id[:]), Sig: sig, Last: true}
	for _, d := range clear {
		forged.Vector = append(forged.Vector, &meshv1.Head{Origin: bytes.Clone(d.Origin[:]), Seq: d.Seq, Hash: make([]byte, 32)})
	}
	if _, err := b.store.mergeSealed(m.ctx, forged, m.now); !errors.Is(err, errBadSealedSnapshot) {
		t.Fatalf("a snapshot the removed bridge signed as another's: %v, want %v", err, errBadSealedSnapshot)
	}
}
