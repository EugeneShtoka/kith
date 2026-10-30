package crdt_test

import (
	"errors"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// A Telegram chat. Invented.
const tgChat = "telegram:200/300"

// writes is the writes s makes for reads, failing the test on an error.
func readWrites(t *testing.T, s crdt.State, reads ...crdt.Read) []crdt.Write {
	t.Helper()
	w, err := s.ReadWrites(reads)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// A read position only moves forward: a read through an older message writes nothing,
// and of two written at once (neither having seen the other) the further is the
// position, on every replica. A read clears the room's mark; a thread's position is
// its own.
func TestAReadPositionOnlyMovesForward(t *testing.T) {
	t.Parallel()
	a, b := newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true))))
	s = apply(t, s, a.op(s, readWrites(t, s, crdt.Read{Room: tgChat, Event: "e2", At: 2000})...))
	if w := readWrites(t, s, crdt.Read{Room: tgChat, Event: "e1", At: 1000}); len(w) != 0 {
		t.Fatalf("an older read writes %v", w)
	}
	// Concurrent: a's later read and b's earlier one, neither having seen the other.
	base := s
	s = apply(t, s, a.op(s, readWrites(t, s, crdt.Read{Room: tgChat, Event: "e4", At: 4000})...))
	s = apply(t, s, b.op(base, readWrites(t, base, crdt.Read{Room: tgChat, Event: "e3", At: 3000})...))
	if got := s.Reads(); len(got) != 1 || got[0].At != 4000 || len(s.Entries[crdt.ReadKey(tgChat)]) != 2 {
		t.Fatalf("Reads = %v over %v, want the further of two siblings", got, s.Entries[crdt.ReadKey(tgChat)])
	}
	s = apply(t, s, b.op(s, crdt.Write{Key: crdt.MarkedKey(tgChat), Value: []byte("true")}))
	w := readWrites(t, s, crdt.Read{Room: tgChat, Event: "e4", At: 4000}, crdt.Read{Room: tgChat, Root: "r1", Event: "e5", At: 5000})
	s = apply(t, s, a.op(s, w...))
	if m := s.Marks()[tgChat]; m.Marked {
		t.Errorf("read, and still marked: %+v", m)
	}
	if got := s.Reads(); len(got) != 2 || got[1].Root != "r1" || got[1].At != 5000 || got[1].Room != tgChat {
		t.Errorf("Reads = %+v, want the room's and the thread's", got)
	}
	if _, ok := s.MarkWrite(tgChat, false); ok {
		t.Error("clearing a mark held cleared writes it again")
	}
}

// A position or a mark is a room's (and a thread's root's), holding a position or a
// mark, or deleted; anything else is refused.
func TestAReadWriteThatDoesNotReadIsRefused(t *testing.T) {
	t.Parallel()
	a := newReplica(t)
	s := genesis(t, a)
	for name, w := range map[string]crdt.Write{
		"no room":           {Key: crdt.ReadKey(""), Value: []byte(`{"at":1}`)},
		"no position":       {Key: crdt.ReadKey(tgChat), Value: []byte("soon")},
		"before 1970":       {Key: crdt.ReadKey(tgChat), Value: []byte(`{"at":-1}`)},
		"a root on a room":  {Key: crdt.ReadKey(tgChat), Value: []byte(`{"at":1,"root":"r1"}`)},
		"another root":      {Key: crdt.ThreadKey(tgChat, "r1"), Value: []byte(`{"at":1,"root":"r2"}`)},
		"a thread of none":  {Key: crdt.ThreadKey("", "r1"), Value: []byte(`{"at":1,"root":"r1"}`)},
		"a deleted thread":  {Key: "thread/telegram:200"},
		"a mark of nothing": {Key: crdt.MarkedKey(""), Value: []byte("true")},
		"a mark not a mark": {Key: crdt.MarkedKey(tgChat), Value: []byte("yes")},
		"an alias deleted":  {Key: crdt.AliasKey(lidChat)},
	} {
		if _, err := s.Apply(a.op(s, w)); !errors.Is(err, crdt.ErrBadWrite) {
			t.Errorf("%s: Apply = %v, want %v", name, err, crdt.ErrBadWrite)
		}
	}
}

// A chat merged into its number's: what the mesh holds of the LID's reads moves under
// the number's keys (the further position, the mark when either is marked) and the
// LID's keys go, on any replica, from a snapshot too. A read of the LID written by a
// bridge that had not seen the merge moves the same way when it arrives. Two rooms
// merged into each other move nothing round in a loop.
func TestAMergedChatsReadsMoveUnderItsNumber(t *testing.T) {
	t.Parallel()
	a, b := newReplica(t), newReplica(t)
	s := genesis(t, a)
	s = apply(t, s, a.op(s, write(t, b.id, b.member(crdt.RoleBridge, true))))
	s = apply(t, s, a.op(s, readWrites(t, s,
		crdt.Read{Room: lidChat, Event: "e5", At: 5000}, crdt.Read{Room: numberChat, Event: "e3", At: 3000},
		crdt.Read{Room: lidChat, Root: "r1", Event: "e2", At: 2000}, crdt.Read{Room: numberChat, Root: "r1", Event: "e4", At: 4000})...))
	s = apply(t, s, a.op(s, crdt.Write{Key: crdt.MarkedKey(lidChat), Value: []byte("true")}))
	unmerged := s
	aliases := map[string]string{lidChat: numberChat}
	folds, err := s.FoldWrites(aliases)
	if err != nil {
		t.Fatal(err)
	}
	s = apply(t, s, a.op(s, append(folds, crdt.Write{Key: crdt.AliasKey(lidChat), Value: []byte(numberChat)})...))
	want := []crdt.Read{{Room: numberChat, Event: "e5", At: 5000}, {Room: numberChat, Root: "r1", Event: "e4", At: 4000}}
	if got := s.Reads(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Reads = %+v, want %+v", got, want)
	}
	if marks := s.Marks(); len(marks) != 1 || !marks[numberChat].Marked {
		t.Errorf("Marks = %+v, want the number's marked alone", marks)
	}
	if again, ferr := s.FoldWrites(s.Aliases()); ferr != nil || len(again) != 0 {
		t.Errorf("folding again writes %v, %v", again, ferr)
	}
	snapped, _, err := unmerged.Merge(s.Snapshot(0))
	if err != nil || len(snapped.Reads()) != 2 {
		t.Errorf("from a snapshot: %+v, %v", snapped.Reads(), err)
	}

	// b reads the LID, not having seen the merge; a holds it beside the merge, and moves
	// it again.
	s = apply(t, s, b.op(unmerged, readWrites(t, unmerged, crdt.Read{Room: lidChat, Event: "e9", At: 9000})...))
	folds, err = s.FoldWrites(s.Aliases())
	if err != nil {
		t.Fatal(err)
	}
	s = apply(t, s, a.op(s, folds...))
	if got := s.Reads(); len(got) != 2 || got[0].Room != numberChat || got[0].At != 9000 {
		t.Errorf("after the raced read moved: %+v", got)
	}

	looped, err := s.FoldWrites(map[string]string{lidChat: numberChat, numberChat: lidChat})
	if err != nil || len(looped) != 0 {
		t.Errorf("two rooms merged into each other write %v, %v", looped, err)
	}
}

// A bridge joining a mesh carries its reads in: each position further than the mesh's,
// and each mark of a room the mesh holds none of.
func TestAJoiningBridgeCarriesItsReadsIn(t *testing.T) {
	t.Parallel()
	a, b := newReplica(t), newReplica(t)
	mesh := genesis(t, a)
	mesh = apply(t, mesh, a.op(mesh, readWrites(t, mesh, crdt.Read{Room: tgChat, Event: "e5", At: 5000}, crdt.Read{Room: numberChat, Event: "e1", At: 1000})...))
	mesh = apply(t, mesh, a.op(mesh, crdt.Write{Key: crdt.MarkedKey(tgChat), Value: []byte("true")}))
	own := genesis(t, b)
	own = apply(t, own, b.op(own, readWrites(t, own, crdt.Read{Room: tgChat, Event: "e2", At: 2000}, crdt.Read{Room: numberChat, Event: "e3", At: 3000})...))
	own = apply(t, own, b.op(own, crdt.Write{Key: crdt.MarkedKey(tgChat), Value: []byte("false")}, crdt.Write{Key: crdt.MarkedKey(numberChat), Value: []byte("true")}))
	got, err := mesh.AdoptWrites(own)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != crdt.MarkedKey(numberChat) || got[1].Key != crdt.ReadKey(numberChat) {
		t.Errorf("AdoptWrites = %v, want the number's mark and position alone", got)
	}
}

// Of two marks written at once, neither having seen the other, the newer is the
// room's, on every replica: a mark unread concurrent with a read that clears it.
func TestOfTwoConcurrentMarksTheNewerHolds(t *testing.T) {
	t.Parallel()
	a, b := newReplica(t), newReplica(t)
	base := genesis(t, a)
	base = apply(t, base, a.op(base, write(t, b.id, b.member(crdt.RoleBridge, true))))
	marked := a.op(base, crdt.Write{Key: crdt.MarkedKey(tgChat), Value: []byte("true")})
	cleared := b.op(base, readWrites(t, base, crdt.Read{Room: tgChat, Event: "e1", At: 1000})...)
	cleared.Writes = append(cleared.Writes, crdt.Write{Key: crdt.MarkedKey(tgChat), Value: []byte("false")})
	cleared.Clock = marked.Clock.Next(marked.Clock.Time().Add(time.Second)) // written a second later
	if err := cleared.Sign(b.key); err != nil {
		t.Fatal(err)
	}
	for name, order := range map[string][]crdt.Op{"marked first": {marked, cleared}, "cleared first": {cleared, marked}} {
		s := base
		for _, op := range order {
			s = apply(t, s, op)
		}
		if m := s.Marks()[tgChat]; m.Marked || m.Dot.Origin != b.id {
			t.Errorf("%s: the mark is %+v, want the newer write's, cleared", name, m)
		}
	}
}
