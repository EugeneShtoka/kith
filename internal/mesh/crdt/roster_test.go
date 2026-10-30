package crdt_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// replica is a bridge's key and what it has written so far.
type replica struct {
	t   *testing.T
	key *ecdsa.PrivateKey
	id  devicekey.ID
}

func newReplica(t *testing.T) replica {
	t.Helper()
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return replica{t: t, key: key, id: id}
}

// member is r's roster entry as role, carrying its key when withKey.
func (r replica) member(role crdt.Role, withKey bool) crdt.Member {
	r.t.Helper()
	m := crdt.Member{Name: r.id.String()[:4], Role: role, AddedAt: time.Unix(1_800_000_000, 0).UTC()}
	if withKey {
		der, err := x509.MarshalPKIXPublicKey(r.key.Public())
		if err != nil {
			r.t.Fatal(err)
		}
		m.Key = der
	}
	return m
}

// write is m as the write of subject's entry.
func write(t *testing.T, subject devicekey.ID, m crdt.Member) crdt.Write {
	t.Helper()
	v, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return crdt.Write{Key: crdt.RosterKey(subject), Value: v}
}

// op is r's next op over s, writing writes, signed by r.
func (r replica) op(s crdt.State, writes ...crdt.Write) crdt.Op {
	r.t.Helper()
	op := crdt.Op{
		Origin: r.id, Seq: s.Vector[r.id].Seq + 1, Clock: crdt.Clock(0).Next(time.Now()),
		Context: s.Vector.Context(), Writes: writes,
	}
	if err := op.Sign(r.key); err != nil {
		r.t.Fatal(err)
	}
	return op
}

// genesis is the state after a's genesis.
func genesis(t *testing.T, a replica) crdt.State {
	t.Helper()
	s, err := crdt.State{}.Apply(a.op(crdt.State{}, write(t, a.id, a.member(crdt.RoleBridge, true))))
	if err != nil {
		t.Fatalf("genesis: %v", err)
	}
	return s
}

// apply applies op over s, failing the test on a refusal.
func apply(t *testing.T, s crdt.State, op crdt.Op) crdt.State {
	t.Helper()
	next, err := s.Apply(op)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return next
}

// The first op of a roster is its genesis, and only a bridge writing its own entry with
// its own key, signed by that key, makes one.
func TestOnlyABridgeSigningItsOwnEntryIsAGenesis(t *testing.T) {
	t.Parallel()
	a, b := newReplica(t), newReplica(t)
	for _, tc := range []struct {
		name string
		op   crdt.Op
		want error
	}{
		{"its own entry, its own key", a.op(crdt.State{}, write(t, a.id, a.member(crdt.RoleBridge, true))), nil},
		{"another's entry", a.op(crdt.State{}, write(t, b.id, b.member(crdt.RoleBridge, true))), crdt.ErrNotAMember},
		{"itself as a device", a.op(crdt.State{}, write(t, a.id, a.member(crdt.RoleDevice, true))), crdt.ErrNotAMember},
		{"itself removed", a.op(crdt.State{}, write(t, a.id, func() crdt.Member {
			m := a.member(crdt.RoleBridge, true)
			m.Removed = true
			return m
		}())), crdt.ErrNotAMember},
		{"a bridge with no key", a.op(crdt.State{}, write(t, a.id, a.member(crdt.RoleBridge, false))), crdt.ErrBadWrite},
		{"its entry carrying another's key", a.op(crdt.State{}, write(t, a.id, b.member(crdt.RoleBridge, true))), crdt.ErrBadWrite},
		{"not its first op", func() crdt.Op {
			op := a.op(crdt.State{}, write(t, a.id, a.member(crdt.RoleBridge, true)))
			op.Seq = 2
			return op
		}(), crdt.ErrNotReady},
	} {
		if _, err := (crdt.State{}).Apply(tc.op); !errors.Is(err, tc.want) {
			t.Errorf("%s: Apply = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// Only a bridge of the roster as it stands signs an op: a key never paired, a device (it
// writes through its bridge), and a bridge since removed are each refused, and the
// state is left as it was.
func TestAnOpSignedByANonMemberIsRefused(t *testing.T) {
	t.Parallel()
	a, stranger, device, gone := newReplica(t), newReplica(t), newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, device.id, device.member(crdt.RoleDevice, true)), write(t, gone.id, gone.member(crdt.RoleBridge, true))))
	removed := gone.member(crdt.RoleBridge, true)
	removed.Removed = true
	s = apply(t, s, a.op(s, write(t, gone.id, removed)))
	before := maps.Clone(s.Entries)
	for name, r := range map[string]replica{"a key never paired": stranger, "a device": device, "a bridge removed": gone} {
		op := r.op(s, write(t, r.id, r.member(crdt.RoleBridge, true)))
		if _, err := s.Apply(op); !errors.Is(err, crdt.ErrNotAMember) {
			t.Errorf("%s: Apply = %v, want %v", name, err, crdt.ErrNotAMember)
		}
	}
	if !maps.EqualFunc(s.Entries, before, func(x, y crdt.Entry) bool { return bytes.Equal(x.Value, y.Value) && x.Dot == y.Dot }) {
		t.Error("a refused op changed the state")
	}
}

// A signature covers the whole op: any field changed after signing, or a signature
// another key made, is refused, and the state it was offered to is left as it was.
func TestAForgedSignatureIsRefused(t *testing.T) {
	t.Parallel()
	a, b, laptop := newReplica(t), newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true))))
	add := write(t, laptop.id, laptop.member(crdt.RoleDevice, false))
	for _, tc := range []struct {
		name   string
		forge  func(op *crdt.Op)
		signer replica
	}{
		{"as written", func(*crdt.Op) {}, a},
		{"signed by another bridge", func(*crdt.Op) {}, b},
		{"its origin another bridge", func(op *crdt.Op) { op.Origin, op.Seq = b.id, 1 }, a},
		{"its clock moved", func(op *crdt.Op) { op.Clock++ }, a},
		{"its context changed", func(op *crdt.Op) { op.Context = nil }, a},
		{"a write's key changed", func(op *crdt.Op) { op.Writes[0].Key = crdt.RosterKey(b.id) }, a},
		{"a write's value changed", func(op *crdt.Op) {
			op.Writes[0] = write(t, laptop.id, func() crdt.Member { m := laptop.member(crdt.RoleDevice, false); m.Name = "evil"; return m }())
		}, a},
		{"a write added", func(op *crdt.Op) { op.Writes = append(op.Writes, write(t, b.id, b.member(crdt.RoleDevice, true))) }, a},
		{"its signature cut", func(op *crdt.Op) { op.Sig = op.Sig[:len(op.Sig)-1] }, a},
		{"no signature", func(op *crdt.Op) { op.Sig = nil }, a},
	} {
		op := a.op(s, add) // a's next op, as the forger claims it to be
		if err := op.Sign(tc.signer.key); err != nil {
			t.Fatal(err)
		}
		tc.forge(&op)
		_, err := s.Apply(op)
		if want := tc.name == "as written"; (err == nil) != want {
			t.Errorf("%s: Apply = %v, want taken %v", tc.name, err, want)
		}
		if err != nil && !errors.Is(err, crdt.ErrBadSignature) {
			t.Errorf("%s: Apply = %v, want %v", tc.name, err, crdt.ErrBadSignature)
		}
	}
}

// Ops of one origin are taken once each, in order, and only over what they had seen.
func TestAnOpIsTakenOnceAndInOrder(t *testing.T) {
	t.Parallel()
	a, laptop, phone := newReplica(t), newReplica(t), newReplica(t)
	s := genesis(t, a)
	first := a.op(s, write(t, laptop.id, laptop.member(crdt.RoleDevice, false)))
	next := apply(t, s, first)
	if _, err := next.Apply(first); !errors.Is(err, crdt.ErrDuplicate) {
		t.Errorf("the same op again: %v, want %v", err, crdt.ErrDuplicate)
	}
	ahead := a.op(next, write(t, phone.id, phone.member(crdt.RoleDevice, false)))
	if _, err := s.Apply(ahead); !errors.Is(err, crdt.ErrNotReady) {
		t.Errorf("an op past one not held: %v, want %v", err, crdt.ErrNotReady)
	}
	twice := a.op(next, write(t, phone.id, phone.member(crdt.RoleDevice, false)), write(t, phone.id, phone.member(crdt.RoleDevice, false)))
	if _, err := next.Apply(twice); !errors.Is(err, crdt.ErrBadWrite) {
		t.Errorf("an entry written twice in one op: %v, want %v", err, crdt.ErrBadWrite)
	}
	if _, err := next.Apply(a.op(next, crdt.Write{Key: "config/display.theme", Value: []byte(`"dark"`)})); !errors.Is(err, crdt.ErrBadWrite) {
		t.Errorf("a key of a kind not kept yet: %v, want %v", err, crdt.ErrBadWrite)
	}
}

// Two bridges changing one entry concurrently end alike in either order: a removal
// wins over an add, and of two adds the later by (clock, origin).
func TestConcurrentRosterWritesEndAlikeInEitherOrder(t *testing.T) {
	t.Parallel()
	a, b, laptop := newReplica(t), newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true)), write(t, laptop.id, laptop.member(crdt.RoleDevice, false))))
	removed := laptop.member(crdt.RoleDevice, false)
	removed.Removed = true
	renamed := laptop.member(crdt.RoleDevice, false)
	renamed.Name = "renamed"
	// at re-signs op as written at clock c.
	at := func(r replica, op crdt.Op, c crdt.Clock) crdt.Op {
		op.Clock = c
		if err := op.Sign(r.key); err != nil {
			t.Fatal(err)
		}
		return op
	}
	for _, tc := range []struct {
		name  string
		fromA crdt.Member
		fromB crdt.Member
		want  func(aLater bool) crdt.Member
	}{
		{"a removal and a rename", removed, renamed, func(bool) crdt.Member { return removed }},
		{"a rename and a removal", renamed, removed, func(bool) crdt.Member { return removed }},
		{"two renames", renamed, laptop.member(crdt.RoleDevice, false), func(aLater bool) crdt.Member {
			if aLater {
				return renamed
			}
			return laptop.member(crdt.RoleDevice, false)
		}},
	} {
		for _, aLater := range []bool{false, true} {
			clockA, clockB := crdt.Clock(1_000<<16), crdt.Clock(2_000<<16)
			if aLater {
				clockA, clockB = clockB, clockA
			}
			opA := at(a, a.op(s, write(t, laptop.id, tc.fromA)), clockA)
			opB := at(b, b.op(s, write(t, laptop.id, tc.fromB)), clockB)
			ab := apply(t, apply(t, s, opA), opB)
			ba := apply(t, apply(t, s, opB), opA)
			key := crdt.RosterKey(laptop.id)
			if !bytes.Equal(ab.Entries[key].Value, ba.Entries[key].Value) {
				t.Errorf("%s, a later %v: the orders end differently", tc.name, aLater)
			}
			roster, err := ab.Roster()
			if err != nil {
				t.Fatal(err)
			}
			if want := tc.want(aLater); roster[laptop.id].Removed != want.Removed || roster[laptop.id].Name != want.Name {
				t.Errorf("%s, a later %v: %+v, want %+v", tc.name, aLater, roster[laptop.id], want)
			}
		}
	}
}

// The clock moves forward whatever the wall clock does.
func TestTheClockOnlyMovesForward(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	c := crdt.Clock(0).Next(now)
	for _, at := range []time.Time{now, now.Add(-time.Hour), now.Add(time.Millisecond), now.Add(-time.Millisecond)} {
		next := c.Next(at)
		if next <= c {
			t.Fatalf("Next(%v) = %d, not past %d", at, next, c)
		}
		c = next
	}
	if later := crdt.Clock(0).Next(now.Add(time.Second)); later <= c {
		t.Fatalf("a second later = %d, not past %d", later, c)
	}
}
