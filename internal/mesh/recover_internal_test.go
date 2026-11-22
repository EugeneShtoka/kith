package mesh

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// TestARecoveryKeyBringsTheMeshBack is step 10's failure review: the mailbox review's
// generator, with recovery keys written at any step by any bridge, two at once at times
// (both stand), and replaced. Once the mesh settles, every bridge is taken as lost: a new
// kithd, paired with one mailbox alone, recovers with each key that stands, from each
// mailbox, and holds what the mesh held, key for key (each key's latest value), its
// bridges listed as removed; with another key it recovers nothing and writes nothing; a
// bridge in a mesh with others is refused; and from a mailbox rolled back to an old copy it
// recovers only what the mesh held at some time, or nothing.
func TestARecoveryKeyBringsTheMeshBack(t *testing.T) {
	t.Parallel()
	for seed := range uint64(modelSeeds) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			m := newModel(t, seed)
			m.sealing, m.rotating, m.mailboxing, m.recovering = true, true, true, true
			m.addMailboxes(2)
			m.secrets, m.ever = map[string]string{}, map[[2]string]bool{}
			for range modelSteps {
				m.step()
				m.recordHeld()
			}
			m.settle()
			m.checkRecovery()
		})
	}
}

// writeRecoveryKey has a member write a new recovery key, and at times another member
// one of its own before they sync.
func (m *model) writeRecoveryKey() {
	for range 1 + m.rng.IntN(2) {
		i := m.rng.IntN(len(m.r))
		if !m.writer(i) {
			continue
		}
		written, err := m.r[i].store.NewRecoveryKey(m.ctx, m.r[i].key)
		m.must(err)
		pub := m.recoveryPublic(written)
		m.secrets[string(pub)] = written
	}
}

// recoveryPublic is the public half of the recovery key written.
func (m *model) recoveryPublic(written string) []byte {
	secret, err := devicekey.ParseSecret(written, devicekey.MeshRecovery)
	m.must(err)
	priv, err := crdt.RecoveryPrivate(secret)
	m.must(err)
	return priv.PublicKey().Bytes()
}

// checkRecovery holds what TestARecoveryKeyBringsTheMeshBack says of a settled mesh.
func (m *model) checkRecovery() {
	r0 := m.r[0].store
	r0.mu.Lock()
	standing := r0.state.RecoveryKeys()
	want := latest(r0.state)
	r0.mu.Unlock()
	members, _, err := r0.Roster()
	m.must(err)
	m.recovered = func(removed []string) {
		for _, d := range members {
			if d.Role == domain.RoleBridge && !slices.Contains(removed, d.ID) {
				m.t.Fatalf("the recovered mesh does not list %s, a bridge of the old one, as removed", d.ID)
			}
		}
	}
	for _, pub := range standing {
		written, ok := m.secrets[string(pub)]
		if !ok {
			m.t.Fatal("a recovery key stands that no step wrote")
		}
		for k := range m.mb {
			got, err := m.recoverFrom(k, written)
			if err != nil {
				m.t.Fatalf("recovering from mailbox %d: %v", k, err)
			}
			if !maps.Equal(got, want) {
				m.t.Fatalf("recovered from mailbox %d:\n%s", k, diffLines(lines(got), lines(want)))
			}
		}
	}
	other := make([]byte, crdt.KeyLen)
	other[0] = 1
	if got, err := m.recoverFrom(0, devicekey.Secret(other, devicekey.MeshRecovery)); !errors.Is(err, ErrWrongRecoveryKey) || len(got) != 0 {
		m.t.Fatalf("recovering with another key: %d keys, %v; want none, %v", len(got), err, ErrWrongRecoveryKey)
	}
	bridges := slices.DeleteFunc(slices.Clone(members), func(d domain.Device) bool { return d.Role != domain.RoleBridge })
	if len(standing) > 0 && len(bridges) > 1 {
		secret, perr := devicekey.ParseSecret(m.secrets[string(standing[0])], devicekey.MeshRecovery)
		m.must(perr)
		if _, rerr := r0.Recover(m.ctx, m.r[0].key, secret, m.mb[0].id, &queue{}); !errors.Is(rerr, ErrNotFresh) {
			m.t.Fatalf("a bridge of the mesh recovering: %v, want %v", rerr, ErrNotFresh)
		}
	}
	m.recovered = nil
	m.checkOldCopy(standing)
}

// checkOldCopy recovers from mailbox 0 rolled back to the old copy of itself it keeps,
// if it keeps one: nothing, or keys the mesh held at some time.
func (m *model) checkOldCopy(standing [][]byte) {
	mb := m.mb[0]
	if _, err := os.Stat(mb.saved); err != nil || len(standing) == 0 {
		return
	}
	m.must(mb.store.Close())
	m.copyFile(mb.saved, mb.path)
	mb.store = m.openMailbox(mb)
	for _, pub := range standing {
		got, err := m.recoverFrom(0, m.secrets[string(pub)])
		if errors.Is(err, ErrWrongRecoveryKey) {
			continue // the copy is older than the key
		}
		m.must(err)
		for key, value := range got {
			if !m.everHeld(key, value) {
				m.t.Fatalf("recovered %s = %s from an old copy: no replica ever held it", key, value)
			}
		}
	}
}

// recordHeld keeps every value each replica holds under a key, in its entries or its
// log, as of this step (ever): the log keeps each op until compaction, well after a
// step.
func (m *model) recordHeld() {
	for i := range m.r {
		s := m.r[i].store
		s.mu.Lock()
		for key, entries := range s.state.Entries {
			for _, e := range entries {
				m.ever[[2]string{key, string(e.Value)}] = true
			}
		}
		s.mu.Unlock()
		ops := m.heldOps(i)
		for j := range ops {
			for _, w := range ops[j].Writes {
				m.ever[[2]string{w.Key, string(w.Value)}] = true
			}
		}
	}
}

// everHeld reports whether a replica held value under key at some step.
func (m *model) everHeld(key, value string) bool { return m.ever[[2]string{key, value}] }

// recoverFrom has a new kithd, paired with mailbox k alone, recover with written, and
// returns what it holds then (latest), or nothing when it is refused, its store as it
// was.
func (m *model) recoverFrom(k int, written string) (map[string]string, error) {
	mb, r := m.mb[k], m.newReplica()
	m.must(mb.store.Admit(m.ctx, r.id))
	m.must(r.store.AddMailbox(m.ctx, r.key, domain.Device{ID: mb.id.String(), Name: "mailbox"}, []string{"192.0.2.9:7444"}))
	secret, err := devicekey.ParseSecret(written, devicekey.MeshRecovery)
	m.must(err)
	before := r.store.vector()
	toM, toB := &queue{}, &queue{}
	frames := &turns{out: toM, in: toB, check: m.checkBlind, fill: func() {
		x, gerr := mb.store.Greet(m.ctx, mb.id, r.id, &turns{out: toB, in: toM}, m.now)
		m.must(gerr)
		m.must(x.Pass(m.ctx))
	}}
	if _, err := r.store.Recover(m.ctx, r.key, secret, mb.id, frames); err != nil {
		if !maps.Equal(r.store.vector(), before) {
			m.t.Fatalf("a recovery refused (%v) wrote", err)
		}
		return nil, err
	}
	if m.recovered != nil {
		_, removed, rerr := r.store.Roster()
		m.must(rerr)
		m.recovered(removed)
	}
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	return latest(r.store.state), nil
}

// latest is each key's latest value (by clock, then origin), but the roster's and the
// accounts', which are each mesh's own.
func latest(st crdt.State) map[string]string {
	out := map[string]string{}
	for key, entries := range st.Entries {
		if crdt.IsRosterKey(key) || strings.HasPrefix(key, "bridge/") || len(entries) == 0 {
			continue
		}
		e := slices.MaxFunc(entries, func(a, b crdt.Entry) int {
			return cmp.Or(cmp.Compare(a.Clock, b.Clock), bytes.Compare(a.Dot.Origin[:], b.Dot.Origin[:]))
		})
		out[key] = string(e.Value)
	}
	return out
}

// lines is kv as sorted lines.
func lines(kv map[string]string) string {
	var b strings.Builder
	for _, key := range slices.Sorted(maps.Keys(kv)) {
		fmt.Fprintf(&b, "%s %s\n", key, kv[key])
	}
	return b.String()
}

// Recovering refuses a snapshot sealed under an epoch the recovery key opens but signed
// by another than the bridge it names as its maker: a removed bridge still holds old
// content keys, and could leave one with a mailbox that never heard of its removal.
func TestRecoveringRefusesASnapshotNotSignedByItsMaker(t *testing.T) {
	t.Parallel()
	m := newModel(t, 0)
	a, gone := m.r[0], m.r[2]
	written, err := a.store.NewRecoveryKey(m.ctx, a.key)
	m.must(err)
	secret, err := devicekey.ParseSecret(written, devicekey.MeshRecovery)
	m.must(err)
	priv, err := crdt.RecoveryPrivate(secret)
	m.must(err)
	a.store.mu.Lock()
	var h held
	keys := map[crdt.Epoch][]byte{}
	for _, r := range a.store.keys.records {
		h.records = append(h.records, r)
		for _, w := range r.Wraps {
			if w.To == crdt.RecoveryID(priv.PublicKey().Bytes()) {
				keys[r.Epoch], err = crdt.Unwrap(r.Epoch, w, priv)
				m.must(err)
			}
		}
	}
	e, _ := currentEpochLocked(a.store)
	snap := a.store.state.Snapshot(a.store.clock)
	a.store.mu.Unlock()
	plain, err := proto.Marshal(snapshotFrame(snap))
	m.must(err)
	boxes, err := crdt.SealSnapshot(plain, a.id, e, keys[e])
	m.must(err)
	clear := snap.Vector.Context()
	digest := crdt.SnapshotDigest(a.id, clear, boxes)
	forged := &meshv1.SealedSnapshot{Boxes: boxes, Maker: bytes.Clone(a.id[:]), Last: true}
	for _, d := range clear {
		forged.Vector = append(forged.Vector, &meshv1.Head{Origin: bytes.Clone(d.Origin[:]), Seq: d.Seq, Hash: make([]byte, 32)})
	}
	if forged.Sig, err = crdt.SignSnapshot(gone.key, digest); err != nil {
		t.Fatal(err)
	}
	h.snapshots = []*meshv1.SealedSnapshot{forged}
	if st := replay(h, keys); len(st.Vector) != 0 {
		t.Fatal("recovering took a snapshot another bridge signed as its maker's")
	}
	if forged.Sig, err = crdt.SignSnapshot(a.key, digest); err != nil {
		t.Fatal(err)
	}
	if st := replay(h, keys); len(st.Vector) == 0 {
		t.Fatal("recovering refused the same snapshot signed by its maker")
	}
}
