package crdt_test

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// contentKey is a content key made afresh: never a literal.
func contentKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, crdt.KeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

// An envelope opens, under its epoch's key, to its own op and no other: moved to
// another op, its clear fields rewritten to name another origin, seq or epoch, a byte
// of its box changed, or opened with another key, it does not. Whatever the op's
// size, what the box holds is padded to a multiple of 4 KiB.
func TestAnEnvelopeOpensOnlyAsItsOwnOp(t *testing.T) {
	t.Parallel()
	a, b := newReplica(t), newReplica(t)
	s := genesis(t, a)
	small := a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true)))
	next := apply(t, s, small)
	big := a.op(next, crdt.Write{Key: crdt.AccountsKey(a.id), Value: []byte(`[{"network":"slack","id":"` + string(bytes.Repeat([]byte("x"), 5000)) + `"}]`)})
	epoch, key := crdt.Epoch{Number: 1, Maker: a.id}, contentKey(t)
	head := len("kith mesh sealed 1\n") + len(devicekey.ID{})
	for _, op := range []crdt.Op{small, big} {
		env, err := crdt.Seal(op, epoch, key)
		if err != nil {
			t.Fatal(err)
		}
		if err := crdt.Unseal(env, key, op); err != nil {
			t.Fatalf("op %d's own envelope: %v", op.Seq, err)
		}
		if got, err := crdt.SealedEpoch(env); err != nil || got != epoch {
			t.Errorf("op %d's envelope names epoch %v (%v), want %v", op.Seq, got, err, epoch)
		}
		// head, seq (one byte), number (one byte), maker, nonce; the box's tag is 16 bytes.
		if boxed := len(env) - head - 2 - len(devicekey.ID{}) - 12 - 16; boxed%(4<<10) != 0 {
			t.Errorf("op %d's box holds %d bytes, not a multiple of 4 KiB", op.Seq, boxed)
		}
		other := op
		other.Seq++
		otherBody := op
		otherBody.Clock++
		rewrite := func(at int, b byte) []byte {
			out := bytes.Clone(env)
			out[at] = b
			return out
		}
		for name, c := range map[string]struct {
			env []byte
			key []byte
			op  crdt.Op
		}{
			"moved to the next op":       {env, key, other},
			"its seq rewritten to match": {rewrite(head, byte(other.Seq)), key, other},
			"its origin rewritten":       {rewrite(len("kith mesh sealed 1\n"), env[len("kith mesh sealed 1\n")]^1), key, op},
			"its epoch rewritten":        {rewrite(head+1, 2), key, op},
			"a byte of its box changed":  {rewrite(len(env)-1, env[len(env)-1]^1), key, op},
			"another key":                {env, contentKey(t), op},
			"another op under its dot":   {env, key, otherBody},
		} {
			if err := crdt.Unseal(c.env, c.key, c.op); err == nil {
				t.Errorf("op %d's envelope, %s: opened", op.Seq, name)
			}
		}
	}
}

// An epoch record reads back as it was written, and verifies only against its
// signer's key, and only as signed.
func TestAnEpochRecordVerifiesOnlyAsItsSignerSignedIt(t *testing.T) {
	t.Parallel()
	a, b := newReplica(t), newReplica(t)
	sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	epoch := crdt.Epoch{Number: 3, Maker: a.id}
	w, err := crdt.WrapKey(epoch, contentKey(t), b.id, sealing.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	rec := crdt.EpochRecord{Epoch: epoch, Context: []crdt.Dot{{Origin: a.id, Seq: 4}, {Origin: b.id, Seq: 1}}, Wraps: []crdt.Wrap{w}, Signer: a.id}
	if err = rec.Sign(a.key); err != nil {
		t.Fatal(err)
	}
	got, err := crdt.ParseEpochRecord(rec.Body(), rec.Sig)
	if err != nil || !bytes.Equal(got.Body(), rec.Body()) || got.Hash() != rec.Hash() {
		t.Fatalf("the record read back: %+v (%v), want %+v", got, err, rec)
	}
	if err := crdt.VerifyRecord(a.member(crdt.RoleBridge, true).Key, got); err != nil {
		t.Errorf("against its signer's key: %v", err)
	}
	if err := crdt.VerifyRecord(b.member(crdt.RoleBridge, true).Key, got); !errors.Is(err, crdt.ErrBadSignature) {
		t.Errorf("against another's key: %v, want %v", err, crdt.ErrBadSignature)
	}
	got.Wraps[0].To = a.id
	if err := crdt.VerifyRecord(a.member(crdt.RoleBridge, true).Key, got); !errors.Is(err, crdt.ErrBadSignature) {
		t.Errorf("its wrap readdressed: %v, want %v", err, crdt.ErrBadSignature)
	}
	if _, err := crdt.ParseEpochRecord(rec.Body()[:len(rec.Body())-1], rec.Sig); err == nil {
		t.Error("a record cut short reads")
	}
}

// A wrap opens to its content key only for the bridge it was made for, and only as a
// wrap of its own epoch.
func TestAWrapOpensOnlyForItsBridgeAndEpoch(t *testing.T) {
	t.Parallel()
	a, b := newReplica(t), newReplica(t)
	mine, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	epoch, key := crdt.Epoch{Number: 1, Maker: a.id}, contentKey(t)
	w, err := crdt.WrapKey(epoch, key, b.id, mine.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := crdt.Unwrap(epoch, w, mine); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("the wrap for this bridge: %v", err)
	}
	readdressed := w
	readdressed.To = a.id
	for name, c := range map[string]struct {
		epoch crdt.Epoch
		w     crdt.Wrap
		priv  *ecdh.PrivateKey
	}{
		"another bridge's key": {epoch, w, theirs},
		"another epoch":        {crdt.Epoch{Number: 2, Maker: a.id}, w, mine},
		"readdressed":          {epoch, readdressed, mine},
	} {
		if _, err := crdt.Unwrap(c.epoch, c.w, c.priv); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	if _, err := crdt.WrapKey(epoch, key, b.id, []byte("not a key")); err == nil {
		t.Error("a wrap for a sealing key that does not read was made")
	}
}

// An epoch serves for sealing only once its context covers every removal of a bridge
// the state holds; a device's removal does not count.
func TestAnEpochIsSinceARemovalOnlyOnceItCoversIt(t *testing.T) {
	t.Parallel()
	a, b, c := newReplica(t), newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true)), write(t, c.id, c.member(crdt.RoleDevice, false))))
	before := s.Vector.Context()
	gone := c.member(crdt.RoleDevice, false)
	gone.Removed = true
	s = apply(t, s, a.op(s, write(t, c.id, gone)))
	if !s.Since(before) {
		t.Error("a device's removal holds an epoch made before it back")
	}
	removed := b.member(crdt.RoleBridge, true)
	removed.Removed = true
	s = apply(t, s, a.op(s, write(t, b.id, removed)))
	if s.Since(before) {
		t.Error("an epoch made before a bridge's removal serves after it")
	}
	if !s.Since(s.Vector.Context()) {
		t.Error("an epoch made after a bridge's removal does not serve")
	}
}
