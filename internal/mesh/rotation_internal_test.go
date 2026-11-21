package mesh

import (
	"crypto/x509"
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// as is the ID replica i says it is to replica j: its own, or, while j does not admit
// that one but still admits the key i rotated from, that key's, as kithd answers a peer
// pinning it (by SNI).
func (m *model) as(i, j int) devicekey.ID {
	r, peer := m.r[i], m.r[j].store
	if r.was != nil && peer.stillPaired(r.id) != nil && peer.stillPaired(r.wasID) == nil {
		return r.wasID
	}
	return r.id
}

// rotateKey is one of the rotation review's steps.
func (m *model) rotateKey() {
	switch n := m.rng.IntN(9); {
	case n < 4:
		m.rotateDevice()
	case n < 5:
		m.rotateBridge()
	case n < 7:
		m.forgeRotation()
	default:
		m.writeAsRotatedOut()
	}
}

// writer is a replica that writes as kithd would: a member, not the forger, not dormant.
func (m *model) writer(i int) bool { return m.member(i) && i != m.forger && i != m.dormant }

// rotateDevice has a bridge rotate a device's key while another bridge listing that
// device, if one does, removes it (before or after) or rotates it too, neither having
// seen the other's.
func (m *model) rotateDevice() {
	i := m.rng.IntN(len(m.r))
	if !m.writer(i) {
		return
	}
	keyed := slices.DeleteFunc(m.listedDevices(i), func(d string) bool { return m.deviceKeys[d] == nil })
	if len(keyed) == 0 {
		return
	}
	d := keyed[m.rng.IntN(len(keyed))]
	var others []int // the writers listing d too, which have not seen this rotation
	for j := range m.r {
		if j != i && m.writer(j) && slices.Contains(m.listedDevices(j), d) {
			others = append(others, j)
		}
	}
	if len(others) == 0 {
		m.rotateDeviceOn(i, d)
		return
	}
	j := others[m.rng.IntN(len(others))]
	switch m.rng.IntN(3) {
	case 0: // removed after the rotation was written, as by a later clock
		next := m.rotateDeviceOn(i, d)
		m.fall = append(m.fall, fall{key: next, removal: m.remove(j, d)})
	case 1: // removed before it was written
		removal := m.remove(j, d)
		m.fall = append(m.fall, fall{key: m.rotateDeviceOn(i, d), removal: removal})
	default:
		m.oneOf = append(m.oneOf, [2]devicekey.ID{m.rotateDeviceOn(i, d), m.rotateDeviceOn(j, d)})
		return
	}
	delete(m.deviceIDs, d) // never paired again, which would stand the new key back up
}

// listedDevices are the devices replica i's roster lists.
func (m *model) listedDevices(i int) []string {
	members, _, err := m.r[i].store.Roster()
	m.must(err)
	var out []string
	for _, d := range members {
		if d.Role == domain.RoleDevice {
			out = append(out, d.ID)
		}
	}
	return out
}

// remove has replica j remove old, and is the op it wrote.
func (m *model) remove(j int, old string) crdt.Dot {
	r := m.r[j]
	m.must(r.store.ChangeRoster(m.ctx, r.key, nil, []string{old}))
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	return crdt.Dot{Origin: r.id, Seq: r.store.state.Vector[r.id].Seq}
}

// rotateDeviceOn has replica i write the rotation of device d to a new key, signed by
// both, as RotateDevice does; it is the new key's ID.
func (m *model) rotateDeviceOn(i int, d string) devicekey.ID {
	next, id := newDeviceID(m.t)
	rot, key, err := devicekey.Rotate(m.deviceKeys[d], next)
	m.must(err)
	m.must(m.r[i].store.Rotate(m.ctx, m.r[i].key, mustID(m.t, d), key, rot))
	m.deviceKeys[id] = next
	return mustID(m.t, id)
}

// rotateBridge has a bridge rotate its own key, once a run each, as RotateBridge does:
// written by the new key, the old one kept to sync as with a peer pinning it. At times
// another bridge removes its old key meanwhile.
func (m *model) rotateBridge() {
	i, j := m.pair()
	r := m.r[i]
	if !m.writer(i) || r.was != nil {
		return
	}
	next, id := newDeviceID(m.t)
	r.store.mu.Lock()
	r.wasSeq = r.store.state.Vector[r.id].Seq
	r.store.mu.Unlock()
	m.must(r.store.RotateSelf(m.ctx, r.key, next))
	r.was, r.wasID, r.key, r.id = r.key, r.id, next, mustID(m.t, id)
	if i != 0 && m.removals < 2 && m.writer(j) && m.rng.IntN(3) == 0 {
		members, _, err := m.r[j].store.Roster()
		m.must(err)
		if slices.ContainsFunc(members, func(d domain.Device) bool { return d.ID == r.wasID.String() }) {
			m.removals++
			m.fall = append(m.fall, fall{key: r.id, removal: m.remove(j, r.wasID.String())})
		}
	}
}

// forgeRotation hands a bridge, as if relayed, an op of its own rotating a device with
// one of the two signatures another key's: refused, and nothing changes. Its own, so
// it is judged at once: one held back would meet the bridge's real op under its dot
// later, a fork its key did sign.
func (m *model) forgeRotation() {
	i := m.rng.IntN(len(m.r))
	r := m.r[i]
	if !m.writer(i) {
		return
	}
	victim, victimID := newDeviceID(m.t)
	next, nextID := newDeviceID(m.t)
	stranger, _ := newDeviceID(m.t)
	rot, key, err := devicekey.Rotate(victim, next)
	m.must(err)
	forged, _, err := devicekey.Rotate(stranger, stranger)
	m.must(err)
	if m.rng.IntN(2) == 0 {
		rot.FromSig = forged.FromSig
	} else {
		rot.ToSig = forged.ToSig
	}
	from, err := x509.MarshalPKIXPublicKey(victim.Public())
	m.must(err)
	old, err := crdt.Member{Key: from, Name: "victim", Role: crdt.RoleDevice, Removed: true, RotatedTo: nextID}.Encode()
	m.must(err)
	made, err := crdt.Member{Key: key, Name: "victim", Role: crdt.RoleDevice, Rotated: &rot}.Encode()
	m.must(err)
	r.store.mu.Lock()
	op := crdt.Op{
		Origin: r.id, Seq: r.store.state.Vector[r.id].Seq + 1, Clock: r.store.clock.Next(m.at(i)), Context: r.store.state.Vector.Context(),
		Writes: []crdt.Write{{Key: crdt.RosterKey(mustID(m.t, victimID)), Value: old}, {Key: crdt.RosterKey(mustID(m.t, nextID)), Value: made}},
	}
	r.store.mu.Unlock()
	m.must(op.Sign(r.key))
	m.offerRefused(i, op, "a rotation signed by one key only")
}

// writeAsRotatedOut has a bridge's old key sign an op that has seen its rotation, handed
// to a member as if relayed: refused, there or wherever it later goes.
func (m *model) writeAsRotatedOut() {
	i, j := m.pair()
	r := m.r[i]
	if r.was == nil || !m.member(j) {
		return
	}
	value, err := crdt.Member{Name: "late", Role: crdt.RoleDevice}.Encode()
	m.must(err)
	_, id := newDeviceID(m.t)
	r.store.mu.Lock()
	op := crdt.Op{
		Origin: r.wasID, Seq: r.wasSeq + 1, Clock: r.store.clock.Next(m.at(i)), Context: r.store.state.Vector.Context(),
		Writes: []crdt.Write{{Key: crdt.RosterKey(mustID(m.t, id)), Value: value}},
	}
	r.store.mu.Unlock()
	m.must(op.Sign(r.was))
	m.offerRefused(j, op, "an op of a rotated-out key written after its rotation")
}

// offerRefused hands replica j op as if relayed, and holds that j takes nothing of it.
func (m *model) offerRefused(j int, op crdt.Op, what string) {
	before := m.text(j)
	res, err := m.r[j].store.receive(m.ctx, []crdt.Op{op}, m.at(j))
	m.must(err)
	if res.applied > 0 || m.text(j) != before {
		m.t.Fatalf("replica %d took %s", j, what)
	}
}

// checkRotatedOutRefused holds that a member holding a bridge's rotation no longer syncs
// with its old key, and that no replica holds an op of an old key past its last before
// the rotation.
func (m *model) checkRotatedOutRefused() {
	for k, gone := range m.r {
		if gone.was == nil {
			continue
		}
		for i := range m.r {
			s := m.r[i].store
			s.mu.Lock()
			roster, err := s.state.Roster()
			past := s.state.Vector[gone.wasID].Seq > gone.wasSeq
			s.mu.Unlock()
			m.must(err)
			if past {
				m.t.Fatalf("replica %d holds an op replica %d's old key wrote after its rotation", i, k)
			}
			if old := roster[gone.wasID]; old.Removed && old.RotatedTo != "" && m.member(i) && s.stillPaired(gone.wasID) == nil {
				m.t.Fatalf("replica %d holds replica %d's rotation, and would still sync with its old key", i, k)
			}
		}
	}
}

// checkRotationsSettled holds, once settled, that no key a rotation made stands on the
// first replica when its old key was removed concurrently (and the removal reached it),
// nor both keys of two concurrent rotations of one key.
func (m *model) checkRotationsSettled() {
	roster := func() map[devicekey.ID]crdt.Member {
		s := m.r[0].store
		s.mu.Lock()
		defer s.mu.Unlock()
		out, err := s.state.Roster()
		m.must(err)
		return out
	}()
	m.r[0].store.mu.Lock()
	vector := m.r[0].store.state.Vector
	m.r[0].store.mu.Unlock()
	for _, f := range m.fall {
		if mem, ok := roster[f.key]; ok && !mem.Removed && vector.Covers(f.removal) {
			m.t.Fatalf("%s stands, made by a rotation of a key removed concurrently", f.key)
		}
	}
	for _, pair := range m.oneOf {
		a, aok := roster[pair[0]]
		b, bok := roster[pair[1]]
		if aok && bok && !a.Removed && !b.Removed {
			m.t.Fatalf("both %s and %s stand, made by two concurrent rotations of one key", pair[0], pair[1])
		}
	}
}

// A bridge syncing with a peer as the key it rotated from (the peer has not heard of
// the rotation) still writes as its new key: a fork it finds there is removed from the
// mesh, by an op of the new key's that every bridge takes.
func TestASessionAsTheOldKeyWritesAsTheNewOne(t *testing.T) {
	t.Parallel()
	m := newModel(t, 0)
	m.sealing = true
	rotated, forger, peer := m.r[1], m.r[2], m.r[0]
	next, id := newDeviceID(t)
	m.must(rotated.store.RotateSelf(m.ctx, rotated.key, next))
	rotated.was, rotated.wasID, rotated.key, rotated.id = rotated.key, rotated.id, next, mustID(t, id)

	// The forger signs two ops under one dot: one the peer takes, the other the rotated bridge.
	_, added := newDeviceID(t)
	m.must(forger.store.ChangeRoster(m.ctx, forger.key, []domain.Device{{ID: added, Role: domain.RoleDevice}}, nil))
	forger.store.mu.Lock()
	held := *forger.store.opAt(m.ctx, nil, forger.id, forger.store.state.Vector[forger.id].Seq)
	key := forger.store.keys.keys[mustEpoch(t, held.Sealed)].key
	forger.store.mu.Unlock()
	value, err := crdt.Member{Name: "twin", Role: crdt.RoleDevice}.Encode()
	m.must(err)
	_, other := newDeviceID(t)
	twin := crdt.Op{Origin: forger.id, Seq: held.Seq, Clock: held.Clock, Context: held.Context, Writes: []crdt.Write{{Key: crdt.RosterKey(mustID(t, other)), Value: value}}}
	m.must(twin.Sign(forger.key))
	twin.Sealed, err = crdt.Seal(twin, mustEpoch(t, held.Sealed), key)
	m.must(err)
	_, err = peer.store.receive(m.ctx, []crdt.Op{held}, m.at(0))
	m.must(err)
	_, err = rotated.store.receive(m.ctx, []crdt.Op{twin}, m.at(1))
	m.must(err)

	if m.as(1, 0) != rotated.wasID {
		t.Fatal("the peer has heard of the rotation already")
	}
	m.exchange(1, 0, false)
	if m.lists(1, 2) {
		t.Fatal("the rotated bridge found the fork syncing as its old key, and did not remove the forger")
	}
}

// mustEpoch is the epoch an envelope was sealed under.
func mustEpoch(t *testing.T, sealed []byte) crdt.Epoch {
	t.Helper()
	e, err := crdt.SealedEpoch(sealed)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
