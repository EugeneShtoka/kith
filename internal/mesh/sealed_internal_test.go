package mesh

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// sealStart is a named start of TestSealingHoldsInEveryOrder: a mesh of bridges, all
// synced, then what they do before any of them syncs again, and the sync sessions that
// follow, run in every order.
type sealStart struct {
	bridges  int
	setup    func(m *model)
	sessions [][2]int
	check    func(m *model, logs *logged)
}

var sealStarts = map[string]sealStart{
	// A removes D, making a new epoch, while B and D write under the old one: no op
	// written once its origin held the removal is sealed under a key D holds.
	"a removal concurrent with writes under the old epoch": {
		bridges: 4,
		setup: func(m *model) {
			m.must(m.r[0].store.ChangeRoster(m.ctx, m.r[0].key, nil, []string{m.r[3].id.String()}))
			m.publish(1, "b")
			m.publish(3, "d")
		},
		sessions: [][2]int{{0, 1}, {0, 2}, {1, 2}, {2, 3}, {1, 3}},
	},
	// A and B use up the epoch at once, each making the next: both stand, and every
	// bridge moves to the later.
	"two epochs at once": {
		bridges: 3,
		setup: func(m *model) {
			for _, i := range []int{0, 1} {
				m.useUp(i)
				m.publish(i, "next")
			}
		},
		sessions: [][2]int{{0, 1}, {0, 2}, {1, 2}},
		check: func(m *model, _ *logged) {
			later := m.r[0].id
			if bytes.Compare(m.r[1].id[:], later[:]) > 0 {
				later = m.r[1].id
			}
			if e, _ := currentEpoch(m.r[2].store); e != (crdt.Epoch{Number: 2, Maker: later}) {
				m.t.Errorf("the mesh writes under %v, want the later of the two made at once: 2 by %s", e, later)
			}
		},
	},
	// A removes D; B holds A's new epoch (a session cut after the Hellos) but not the
	// removal. D, which syncs with B still, learns of the epoch and asks B for it: B,
	// not holding the context the epoch was made at, wraps nothing for it.
	"a removed bridge asking one that has not heard of its removal": {
		bridges: 3,
		setup: func(m *model) {
			m.must(m.r[0].store.ChangeRoster(m.ctx, m.r[0].key, nil, []string{m.r[2].id.String()}))
			refused, err := m.r[1].store.takeEpochs(m.ctx, m.r[1].key, m.r[0].id, m.r[0].store.tell(syncing{peer: m.r[1].id, told: newTold(), log: m.log}))
			m.must(err)
			if len(refused) > 0 {
				m.t.Fatal(refused)
			}
		},
		sessions: [][2]int{{1, 2}, {1, 2}, {0, 1}, {0, 2}},
	},
	// A makes a new epoch; B holds its record (a session cut after the Hellos) but none
	// of A's ops it was made over, and pairs J: B writes under, and wraps for J, the
	// epoch whose context it holds, so J joins on it and makes none of its own.
	"a bridge pairing another while it holds a newer epoch's key but not its context": {
		bridges: 2,
		setup: func(m *model) {
			m.useUp(0)
			m.publish(0, "second")
			refused, err := m.r[1].store.takeEpochs(m.ctx, m.r[1].key, m.r[0].id, m.r[0].store.tell(syncing{peer: m.r[1].id, told: newTold(), log: m.log}))
			m.must(err)
			if len(refused) > 0 {
				m.t.Fatal(refused)
			}
			j := m.newReplica()
			m.r = append(m.r, j)
			m.join(m.r[1], j, "joined")
			m.publish(2, "j")
			sponsor, _ := currentEpoch(m.r[1].store)
			if got, ok := currentEpoch(j.store); !ok || got != sponsor || madeAny(j.store, j.id) {
				m.t.Fatalf("the bridge joining writes under %v (%v; made one of its own: %v), not its sponsor's %v", got, ok, madeAny(j.store, j.id), sponsor)
			}
		},
		sessions: [][2]int{{0, 1}, {0, 2}, {1, 2}},
	},
	// A uses up the first epoch and makes the second; B, having moved to it, uses it up
	// in turn: it makes a third, never falling back to the first, which it still holds.
	"a used-up epoch moves on, never back": {
		bridges: 2,
		setup: func(m *model) {
			m.useUp(0)
			m.publish(0, "second")
			m.exchange(0, 1, false)
			m.useUp(1)
			m.publish(1, "third")
		},
		sessions: [][2]int{{0, 1}},
		check: func(m *model, _ *logged) {
			if e, _ := currentEpoch(m.r[0].store); e != (crdt.Epoch{Number: 3, Maker: m.r[1].id}) {
				m.t.Errorf("the mesh writes under %v, want the third, by the bridge that used up the second", e)
			}
		},
	},
	// A removes D, wrapping the new epoch for B and C; meanwhile B pairs J, which joins
	// under the old epoch and writes. J holds no wrap of the new one: it says so, asks,
	// and is given one, by whichever bridge holds it and J's entry first.
	"a join between a removal and its new epoch": {
		bridges: 4,
		setup: func(m *model) {
			m.must(m.r[0].store.ChangeRoster(m.ctx, m.r[0].key, nil, []string{m.r[3].id.String()}))
			j := m.newReplica()
			m.r = append(m.r, j)
			m.join(m.r[1], j, "joined")
			m.publish(4, "j")
		},
		sessions: [][2]int{{0, 1}, {0, 4}, {1, 4}, {2, 4}, {0, 2}},
		check: func(m *model, logs *logged) {
			if !logs.has("no wrap for this bridge has opened the content key of an epoch; asking the peer for one") {
				m.t.Error("the bridge that joined between a removal and its new epoch never said it lacked a wrap")
			}
		},
	},
}

// TestSealingHoldsInEveryOrder runs each named start's sessions in every order, then
// settles the mesh, and holds that every bridge of the mesh holds the same entries and
// the same envelope per dot, that no op written once its origin held a bridge's
// removal is sealed under an epoch that bridge holds the key of, and that once each
// has written again every bridge writes under the same epoch.
func TestSealingHoldsInEveryOrder(t *testing.T) {
	t.Parallel()
	for name, start := range sealStarts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, order := range permutations(len(start.sessions)) {
				t.Run(fmt.Sprint(order), func(t *testing.T) {
					t.Parallel()
					logs := &logged{}
					m := newMesh(t, start.bridges, slog.New(logs))
					start.setup(m)
					for _, k := range order {
						s := start.sessions[k]
						m.exchange(s[0], s[1], false)
						m.checkEnvelopes()
						m.checkRemovedReadNothingNew()
					}
					m.settleSealed()
					if start.check != nil {
						start.check(m, logs)
					}
				})
			}
		})
	}
}

// newMesh is a mesh of n bridges, the first its genesis, each other joined through it,
// all synced.
func newMesh(t *testing.T, n int, log *slog.Logger) *model {
	t.Helper()
	m := &model{
		t: t, ctx: t.Context(), rng: mrand.New(mrand.NewPCG(0, 2)), log: log, now: time.Now(),
		cut: map[[2]int]bool{}, replay: map[int][]*meshv1.SyncFrame{}, forger: -1, dormant: -1, deviceIDs: map[string]bool{},
	}
	for range n {
		m.r = append(m.r, m.newReplica())
	}
	for i := 1; i < n; i++ {
		m.join(m.r[0], m.r[i], fmt.Sprint("bridge", i))
	}
	if n > 1 {
		m.syncAll()
	}
	return m
}

// syncAll syncs every pair until a round changes nothing.
func (m *model) syncAll() {
	for round := range 3 * len(m.r) {
		before := m.texts()
		for i := range m.r {
			for j := i + 1; j < len(m.r); j++ {
				m.exchange(i, j, false)
			}
		}
		if round > 0 && slices.Equal(before, m.texts()) {
			return
		}
	}
	m.t.Fatal("the mesh never settled")
}

// settleSealed syncs every pair until nothing changes, has every bridge of the mesh
// write again and settles again, then holds the bridges of the first's mesh to the
// same entries, envelopes and epoch.
func (m *model) settleSealed() {
	m.syncAll()
	var members []int
	for i := range m.r {
		if m.lists(0, i) {
			members = append(members, i)
		}
	}
	m.checkEnvelopes()
	m.checkRemovedReadNothingNew()
	m.checkOneEpoch(members, m.syncAll)
	for _, i := range members[1:] {
		if got, want := m.text(i), m.text(members[0]); got != want {
			m.t.Fatalf("replica %d and replica %d differ:\n%s", i, members[0], diffLines(got, want))
		}
	}
}

// publish has replica i write: the accounts it runs, named by what.
func (m *model) publish(i int, what string) {
	m.must(m.r[i].store.PublishAccounts(m.ctx, m.r[i].key, []domain.MeshAccount{{Network: "slack", ID: what}}))
}

// useUp has replica i reach maxSeals under the epoch it writes under, as after 2²⁸ ops.
func (m *model) useUp(i int) {
	s := m.r[i].store
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := currentEpochLocked(s)
	if !ok {
		return
	}
	k := s.keys.keys[e]
	k.seals = maxSeals
	s.keys.keys[e] = k
	_, err := s.db.ExecContext(m.ctx, `UPDATE epochs SET seals = ? WHERE number = ? AND maker = ?`, k.seals, e.Number, e.Maker[:])
	m.must(err)
}

// permutations is every order of 0…n-1.
func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{nil}
	}
	var out [][]int
	for _, rest := range permutations(n - 1) {
		for at := range n {
			out = append(out, slices.Insert(slices.Clone(rest), at, n-1))
		}
	}
	return out
}

// logged keeps every message logged.
type logged struct {
	mu   sync.Mutex
	msgs []string
}

func (l *logged) Enabled(context.Context, slog.Level) bool { return true }

func (l *logged) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, r.Message)
	return nil
}

func (l *logged) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logged) WithGroup(string) slog.Handler      { return l }

func (l *logged) has(msg string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.ContainsFunc(l.msgs, func(m string) bool { return strings.Contains(m, msg) })
}

// A store laid out before ops were sealed opens: its ops keep no envelope, and its
// next write puts this bridge's sealing key in its own entry, makes the first epoch and
// is sealed under it.
func TestAStoreFromBeforeSealingSealsFromItsNextWrite(t *testing.T) {
	t.Parallel()
	m := newMesh(t, 0, slog.New(slog.DiscardHandler))
	key, id := newDeviceID(t)
	r := &modelReplica{key: key, id: mustID(t, id), path: filepath.Join(t.TempDir(), "mesh.db"), dbKey: make([]byte, sealeddb.KeyLen)}
	if _, err := rand.Read(r.dbKey); err != nil {
		t.Fatal(err)
	}
	m.r = append(m.r, r)
	r.store = m.open(r)
	// The genesis as a kith before sealing wrote it: an entry with no sealing key, an op
	// with no envelope; then layout 1, with no keyring.
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	m.must(err)
	value, err := crdt.Member{Key: der, Role: crdt.RoleBridge}.Encode()
	m.must(err)
	genesis := crdt.Op{Origin: r.id, Seq: 1, Clock: crdt.At(time.Now()), Writes: []crdt.Write{{Key: crdt.RosterKey(r.id), Value: value}}}
	m.must(genesis.Sign(key))
	if got, rerr := r.store.receive(m.ctx, []crdt.Op{genesis}, time.Now()); rerr != nil || got.applied != 1 {
		t.Fatalf("the genesis: %+v, %v", got, rerr)
	}
	m.must(r.store.Close())
	db, err := sealeddb.Open(m.ctx, r.path, r.dbKey)
	m.must(err)
	_, err = db.ExecContext(m.ctx, `ALTER TABLE ops DROP COLUMN sealed; DROP TABLE sealing; DROP TABLE epochs; DROP TABLE records;
		PRAGMA user_version = 1;`)
	m.must(err)
	m.must(db.Close())
	r.store = m.open(r)
	if ops := m.heldOps(0); len(ops) != 1 || len(ops[0].Sealed) > 0 {
		t.Fatalf("the genesis, kept from before: %+v", ops)
	}
	m.publish(0, "after")
	roster, err := r.store.state.Roster()
	m.must(err)
	if got := roster[r.id].Sealing; !bytes.Equal(got, r.store.Sealing()) {
		t.Errorf("its entry carries %x after its next write, want its sealing key %x", got, r.store.Sealing())
	}
	ops := m.heldOps(0)
	if len(ops) != 2 || len(ops[1].Sealed) == 0 {
		t.Fatalf("the write after: %+v, want sealed", ops)
	}
	e, ok := currentEpoch(r.store)
	if got, err := crdt.SealedEpoch(ops[1].Sealed); !ok || err != nil || got != e || e.Number != 1 {
		t.Errorf("the write is sealed under %v (%v), the store writes under %v (%v); want the first epoch", got, err, e, ok)
	}
}

// An epoch record is taken only from a bridge of the mesh, as it signed it; and an
// epoch made by a bridge since removed is never written under, whatever context its
// record claims (one covering the removal, made up by the bridge removed).
func TestARemovedBridgesEpochIsNeverWrittenUnder(t *testing.T) {
	t.Parallel()
	m := newMesh(t, 3, slog.New(slog.DiscardHandler))
	a, b, d := m.r[0], m.r[1], m.r[2]
	m.must(a.store.ChangeRoster(m.ctx, a.key, nil, []string{d.id.String()}))
	forged := func(signer *modelReplica, by *ecdsa.PrivateKey) *meshv1.Epochs {
		e := crdt.Epoch{Number: 9, Maker: d.id}
		w, err := crdt.WrapKey(e, make([]byte, crdt.KeyLen), b.id, b.store.Sealing())
		m.must(err)
		rec := crdt.EpochRecord{Epoch: e, Context: a.store.vector().Context(), Wraps: []crdt.Wrap{w}, Signer: signer.id}
		m.must(rec.Sign(by))
		return &meshv1.Epochs{Records: []*meshv1.EpochRecord{{Body: rec.Body(), Sig: rec.Sig}}}
	}
	if refused, err := b.store.takeEpochs(m.ctx, b.key, a.id, forged(a, d.key)); err != nil || len(refused) != 1 {
		t.Errorf("a record signed by another than its signer: refused %v, %v", refused, err)
	}
	// Before b holds the removal, d is a bridge of its mesh: the record is taken.
	if refused, err := b.store.takeEpochs(m.ctx, b.key, d.id, forged(d, d.key)); err != nil || len(refused) != 0 {
		t.Fatalf("d's record before its removal: refused %v, %v", refused, err)
	}
	m.exchange(0, 1, false)
	if e, ok := currentEpoch(b.store); !ok || e.Maker == d.id {
		t.Errorf("b writes under %v (%v), once it holds the removal of the bridge that made it", e, ok)
	}
	m.publish(1, "after")
	if ops := m.heldOps(1); slices.ContainsFunc(ops, func(op crdt.Op) bool {
		e, err := crdt.SealedEpoch(op.Sealed)
		return err == nil && e.Maker == d.id
	}) {
		t.Error("b sealed an op under the removed bridge's epoch")
	}
	// Once b holds the removal, d's records are not taken.
	other := forged(d, d.key)
	if refused, err := b.store.takeEpochs(m.ctx, b.key, d.id, other); err != nil || len(refused) != 1 {
		t.Errorf("d's record after its removal: refused %v, %v", refused, err)
	}
}
