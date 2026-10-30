package crdt_test

import (
	"errors"
	"testing"

	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// A WhatsApp chat known by the person's LID, merged into their number's. Invented.
const (
	lidChat    = "whatsapp:15550100/88001234567@lid"
	numberChat = "whatsapp:15550100/15550199@s.whatsapp.net"
)

// Any bridge writes a room's alias, naming another room; one naming none, or itself,
// is refused. A snapshot carries it, and its receiver reads it alike.
func TestAnAliasNamesTheRoomItJoined(t *testing.T) {
	t.Parallel()
	a, b := newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true))))
	for name, w := range map[string]crdt.Write{
		"no room":        {Key: crdt.AliasKey(lidChat)},
		"itself":         {Key: crdt.AliasKey(lidChat), Value: []byte(lidChat)},
		"no merged room": {Key: crdt.AliasKey(""), Value: []byte(numberChat)},
	} {
		if _, err := s.Apply(b.op(s, w)); !errors.Is(err, crdt.ErrBadWrite) {
			t.Errorf("%s: Apply = %v, want %v", name, err, crdt.ErrBadWrite)
		}
	}
	s = apply(t, s, b.op(s, crdt.Write{Key: crdt.AliasKey(lidChat), Value: []byte(numberChat)}))
	if got := s.Aliases(); got[lidChat] != numberChat || len(got) != 1 {
		t.Fatalf("Aliases = %v", got)
	}
	got, _, err := crdt.State{}.Merge(s.Snapshot(0))
	if err != nil || got.Aliases()[lidChat] != numberChat {
		t.Fatalf("from a snapshot: %v, %v", got.Aliases(), err)
	}
}

// Two bridges writing one alias at once, each having seen neither: equal values read
// as the one room; differing ones as the later write's, on every replica alike.
func TestConcurrentAliasesReadAlikeInEitherOrder(t *testing.T) {
	t.Parallel()
	a, b := newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true))))
	for _, other := range []string{numberChat, "whatsapp:15550100/15550188@s.whatsapp.net"} {
		fromA := a.op(s, crdt.Write{Key: crdt.AliasKey(lidChat), Value: []byte(numberChat)})
		fromB := b.op(s, crdt.Write{Key: crdt.AliasKey(lidChat), Value: []byte(other)})
		ab := apply(t, apply(t, s, fromA), fromB)
		ba := apply(t, apply(t, s, fromB), fromA)
		// The later write by (clock, origin), as a register reads.
		want := numberChat
		if fromB.Clock > fromA.Clock || (fromB.Clock == fromA.Clock && string(fromB.Origin[:]) > string(fromA.Origin[:])) {
			want = other
		}
		if x, y := ab.Aliases()[lidChat], ba.Aliases()[lidChat]; x != want || y != want {
			t.Errorf("writing %s beside %s: read %s one way, %s the other, want %s", other, numberChat, x, y, want)
		}
	}
}
