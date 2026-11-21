package crdt_test

import (
	"errors"
	"testing"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// rotation is the two writes of old's move to next, its entry was before, signed by
// both keys; and the new entry.
func rotation(t *testing.T, was crdt.Member, old, next replica) (gone, made crdt.Write, m crdt.Member) {
	t.Helper()
	rot, key, err := devicekey.Rotate(old.key, next.key)
	if err != nil {
		t.Fatal(err)
	}
	m = was
	m.Key, m.Rotated = key, &rot
	was.Removed, was.RotatedTo = true, next.id.String()
	return write(t, old.id, was), write(t, next.id, m), m
}

// standing is the roster's entry of id as s settles it.
func standing(t *testing.T, s crdt.State, id devicekey.ID) crdt.Member {
	t.Helper()
	roster, err := s.Roster()
	if err != nil {
		t.Fatal(err)
	}
	return roster[id]
}

// A rotation is taken only whole: signed by both keys, the old entry removed as rotated
// to the new one of the same role, in one op. Anything less is refused and changes
// nothing.
func TestARotationIsTakenOnlySignedByBothKeys(t *testing.T) {
	t.Parallel()
	a, laptop, next, stranger := newReplica(t), newReplica(t), newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, laptop.id, laptop.member(crdt.RoleDevice, true))))
	gone, made, m := rotation(t, laptop.member(crdt.RoleDevice, true), laptop, next)
	// resigned is made with the old key's signature (or the new one's) another key's.
	resigned := func(oldSig bool) crdt.Write {
		forged, _, err := devicekey.Rotate(stranger.key, stranger.key)
		if err != nil {
			t.Fatal(err)
		}
		rot := *m.Rotated
		if oldSig {
			rot.FromSig = forged.FromSig
		} else {
			rot.ToSig = forged.ToSig
		}
		bad := m
		bad.Rotated = &rot
		return write(t, next.id, bad)
	}
	_, asBridge, _ := rotation(t, laptop.member(crdt.RoleBridge, true), laptop, next)
	for _, tc := range []struct {
		name   string
		writes []crdt.Write
		want   error
	}{
		{"whole", []crdt.Write{gone, made}, nil},
		{"the old key's signature another's", []crdt.Write{gone, resigned(true)}, crdt.ErrBadWrite},
		{"the new key's signature another's", []crdt.Write{gone, resigned(false)}, crdt.ErrBadWrite},
		{"the old entry alone", []crdt.Write{gone}, crdt.ErrBadWrite},
		{"the new entry as another role", []crdt.Write{gone, asBridge}, crdt.ErrBadWrite},
	} {
		nextID := next.id
		next, err := s.Apply(a.op(s, tc.writes...))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: Apply = %v, want %v", tc.name, err, tc.want)
			continue
		}
		if err != nil {
			continue
		}
		if m := standing(t, next, laptop.id); !m.Removed {
			t.Errorf("%s: the old key stands", tc.name)
		}
		if m := standing(t, next, nextID); m.Removed || m.Role != crdt.RoleDevice {
			t.Errorf("%s: the new key = %+v, want a device standing", tc.name, m)
		}
	}
}

// A bridge writes its own rotation with its new key, which the roster does not hold yet:
// taken only while the old key is a bridge of the roster, and once taken the old key
// writes nothing that has seen it. What it wrote before (concurrently) still counts.
func TestABridgeRotatesWithItsNewKey(t *testing.T) {
	t.Parallel()
	a, b, nextB, laptop, nextLaptop := newReplica(t), newReplica(t), newReplica(t), newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true)), write(t, laptop.id, laptop.member(crdt.RoleDevice, true))))

	gone, made, _ := rotation(t, b.member(crdt.RoleBridge, true), b, nextB)
	rotated := apply(t, s, nextB.op(s, gone, made))
	if m := standing(t, rotated, nextB.id); m.Removed || m.Role != crdt.RoleBridge {
		t.Fatalf("the new key = %+v, want a bridge standing", m)
	}
	if _, err := rotated.Apply(b.op(rotated, write(t, laptop.id, laptop.member(crdt.RoleDevice, true)))); !errors.Is(err, crdt.ErrNotAMember) {
		t.Errorf("the old key, having seen its rotation: %v, want %v", err, crdt.ErrNotAMember)
	}
	if _, err := rotated.Apply(b.op(s, write(t, laptop.id, laptop.member(crdt.RoleDevice, true)))); err != nil {
		t.Errorf("the old key, concurrently: %v, want taken", err)
	}
	after := apply(t, rotated, nextB.op(rotated, write(t, laptop.id, laptop.member(crdt.RoleDevice, true))))
	if after.Vector[nextB.id].Seq != 2 {
		t.Errorf("the new key's next op: seq %d, want 2", after.Vector[nextB.id].Seq)
	}

	gone, made, _ = rotation(t, laptop.member(crdt.RoleDevice, true), laptop, nextLaptop)
	if _, err := s.Apply(nextLaptop.op(s, gone, made)); !errors.Is(err, crdt.ErrNotAMember) {
		t.Errorf("a device rotating with its own new key: %v, want %v", err, crdt.ErrNotAMember)
	}
	removed := b.member(crdt.RoleBridge, true)
	removed.Removed = true
	out := apply(t, s, a.op(s, write(t, b.id, removed)))
	gone, made, _ = rotation(t, b.member(crdt.RoleBridge, true), b, nextB)
	if _, err := out.Apply(nextB.op(out, gone, made)); !errors.Is(err, crdt.ErrNotAMember) {
		t.Errorf("a removed bridge rotating: %v, want %v", err, crdt.ErrNotAMember)
	}
	if _, err := s.Apply(nextB.op(s, made)); !errors.Is(err, crdt.ErrNotAMember) {
		t.Errorf("the new entry alone, by the new key: %v, want %v", err, crdt.ErrNotAMember)
	}
}

// A removal of a key concurrent with its rotation wins, on every replica in either
// order, and takes the new key with it; so does it with a rotation of the new key made
// meanwhile. Of two rotations of one key at once, one new key stands.
func TestARemovalConcurrentWithARotationRemovesTheNewKeyToo(t *testing.T) {
	t.Parallel()
	a, b, laptop, next, last := newReplica(t), newReplica(t), newReplica(t), newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true)), write(t, laptop.id, laptop.member(crdt.RoleDevice, true))))
	removed := laptop.member(crdt.RoleDevice, true)
	removed.Removed = true
	gone, made, m := rotation(t, laptop.member(crdt.RoleDevice, true), laptop, next)
	rotate := a.op(s, gone, made)
	afterRotate := apply(t, s, rotate)
	gone2, made2, _ := rotation(t, m, next, last)
	again := a.op(afterRotate, gone2, made2)
	other := newReplica(t)
	goneOther, madeOther, _ := rotation(t, laptop.member(crdt.RoleDevice, true), laptop, other)

	for _, tc := range []struct {
		name  string
		fromA []crdt.Op
		fromB crdt.Op
		gone  []devicekey.ID
		one   []devicekey.ID // exactly one of these stands
	}{
		{"a removal", []crdt.Op{rotate}, b.op(s, write(t, laptop.id, removed)), []devicekey.ID{laptop.id, next.id}, nil},
		{"a removal, the new key rotated on", []crdt.Op{rotate, again}, b.op(s, write(t, laptop.id, removed)),
			[]devicekey.ID{laptop.id, next.id, last.id}, nil},
		{"another rotation", []crdt.Op{rotate}, b.op(s, goneOther, madeOther), []devicekey.ID{laptop.id}, []devicekey.ID{next.id, other.id}},
	} {
		ab, ba := s, s
		for _, op := range tc.fromA {
			ab = apply(t, ab, op)
		}
		ab = apply(t, ab, tc.fromB)
		ba = apply(t, ba, tc.fromB)
		for _, op := range tc.fromA {
			ba = apply(t, ba, op)
		}
		for _, st := range []crdt.State{ab, ba} {
			for _, id := range tc.gone {
				if !standing(t, st, id).Removed {
					t.Errorf("%s: %s stands", tc.name, id.String()[:4])
				}
			}
			if len(tc.one) > 0 {
				n := 0
				for _, id := range tc.one {
					if !standing(t, st, id).Removed {
						n++
					}
				}
				if n != 1 {
					t.Errorf("%s: %d of the new keys stand, want 1", tc.name, n)
				}
			}
		}
		if standing(t, ab, next.id).Removed != standing(t, ba, next.id).Removed {
			t.Errorf("%s: the orders end differently", tc.name)
		}
	}
}

// A key paired again after its rotation, by a write that has seen the rotation, takes
// the new key down: the rotation no longer stands, and removing is the safer error.
func TestAKeyPairedAgainAfterItsRotationTakesTheNewOneDown(t *testing.T) {
	t.Parallel()
	a, laptop, next := newReplica(t), newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, laptop.id, laptop.member(crdt.RoleDevice, true))))
	gone, made, _ := rotation(t, laptop.member(crdt.RoleDevice, true), laptop, next)
	s = apply(t, s, a.op(s, gone, made))
	s = apply(t, s, a.op(s, write(t, laptop.id, laptop.member(crdt.RoleDevice, true))))
	if standing(t, s, laptop.id).Removed || !standing(t, s, next.id).Removed {
		t.Errorf("old %+v, new %+v; want the old standing and the new removed", standing(t, s, laptop.id), standing(t, s, next.id))
	}
}

// A removal concurrent with a rotation wins whichever was written later, and a bridge
// whose rotation it took down writes nothing once it holds the removal.
func TestARemovalBeatsARotationWrittenLater(t *testing.T) {
	t.Parallel()
	a, b, nextB, c := newReplica(t), newReplica(t), newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true)), write(t, c.id, c.member(crdt.RoleBridge, true))))
	removed := b.member(crdt.RoleBridge, true)
	removed.Removed = true
	gone, made, _ := rotation(t, b.member(crdt.RoleBridge, true), b, nextB)
	// at re-signs op as written at clock k.
	at := func(r replica, op crdt.Op, k crdt.Clock) crdt.Op {
		op.Clock = k
		if err := op.Sign(r.key); err != nil {
			t.Fatal(err)
		}
		return op
	}
	for _, rotationLater := range []bool{false, true} {
		clockRotate, clockRemove := crdt.Clock(1_000<<16), crdt.Clock(2_000<<16)
		if rotationLater {
			clockRotate, clockRemove = clockRemove, clockRotate
		}
		rotate := at(nextB, nextB.op(s, gone, made), clockRotate)
		remove := at(c, c.op(s, write(t, b.id, removed)), clockRemove)
		for _, order := range [][]crdt.Op{{rotate, remove}, {remove, rotate}} {
			st := apply(t, apply(t, s, order[0]), order[1])
			if !standing(t, st, nextB.id).Removed {
				t.Errorf("the rotation later %v: the new key stands", rotationLater)
			}
			// Its new key, once it holds the removal, writes nothing taken.
			if _, err := st.Apply(nextB.op(st, write(t, c.id, c.member(crdt.RoleBridge, true)))); !errors.Is(err, crdt.ErrNotAMember) {
				t.Errorf("the rotation later %v: the fallen key, having seen the removal: %v, want %v", rotationLater, err, crdt.ErrNotAMember)
			}
		}
	}
}
