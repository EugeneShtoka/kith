package crdt

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// Entry is one value a key holds and the op that wrote it.
type Entry struct {
	Value []byte
	Dot   Dot
	Clock Clock
}

// State is a replica's entries, its version vector and the roster's ops.
type State struct {
	// Entries are each key's values that no write to the key has seen: one is the plain
	// case; more were written concurrently (siblings) and stay until a write that has
	// seen them all. Each key's are in dot order.
	Entries map[string][]Entry
	Vector  Vector
	// RosterOps are every roster op taken, in an order that follows causality: who was
	// a member as of any op's context is read from them, so they are kept whole, and a
	// snapshot carries them for its receiver to check back to the genesis.
	RosterOps []Op
}

// Why an op is refused.
var (
	ErrDuplicate    = errors.New("crdt: the op is held already")
	ErrNotReady     = errors.New("crdt: the op follows ops not held yet")
	ErrNotAMember   = errors.New("crdt: the op's signer was not a bridge of the roster as of the op")
	ErrBadSignature = errors.New("crdt: the op's signature does not verify")
	ErrBadWrite     = errors.New("crdt: the op writes what no replica takes")
)

// Apply is the state with op applied, all of it or none: op is refused unless it is the
// next of its origin, follows only ops held, writes entries that read, and is signed by
// a bridge of the roster as of the op's own context (or, on a state with no roster yet,
// is the genesis: signed by the bridge whose entry it writes). Membership is judged by
// what the op had seen, never by what the replica applying it holds, so every replica
// takes or refuses an op alike: a removed bridge's op written before it saw its removal
// counts everywhere, however the two reach each replica.
func (s State) Apply(op Op) (State, error) {
	held := s.Vector[op.Origin]
	switch {
	case op.Seq <= held.Seq:
		return s, ErrDuplicate
	case op.Seq != held.Seq+1 || !s.holds(op.Context):
		return s, ErrNotReady
	}
	next, err := s.take(op)
	if err != nil {
		return s, err
	}
	next.Vector[op.Origin] = Head{Seq: op.Seq, Hash: op.Digest()}
	return next, nil
}

// take is the state with op's writes applied, once its signer is checked: the
// membership and signature rules of Apply, without its order.
func (s State) take(op Op) (State, error) {
	if len(op.Body()) > MaxOpSize {
		return s, fmt.Errorf("%w: an op over %d bytes", ErrBadWrite, MaxOpSize)
	}
	writes, err := readWrites(op)
	if err != nil {
		return s, err
	}
	var roster map[devicekey.ID]Member
	if len(s.RosterOps) == 0 {
		if op.Seq != 1 || len(op.Context) > 0 {
			return s, ErrNotAMember
		}
		roster = writes // the genesis: its signer adds itself
	} else {
		roster = s.rosterAt(op)
	}
	signer, ok := roster[op.Origin]
	if !ok || signer.Removed || signer.Role != RoleBridge {
		return s, ErrNotAMember
	}
	if err := Verify(signer.Key, op); err != nil {
		return s, err
	}
	next := State{Entries: maps.Clone(s.Entries), Vector: maps.Clone(s.Vector), RosterOps: s.RosterOps}
	if next.Entries == nil {
		next.Entries, next.Vector = map[string][]Entry{}, Vector{}
	}
	dot := Dot{Origin: op.Origin, Seq: op.Seq}
	for _, w := range op.Writes {
		var live []Entry
		if len(w.Value) > 0 { // an empty value deletes what the op had seen
			live = append(live, Entry{Value: w.Value, Dot: dot, Clock: op.Clock})
		}
		for _, e := range next.Entries[w.Key] {
			if !covers(op, e.Dot) {
				live = append(live, e)
			}
		}
		slices.SortFunc(live, func(a, b Entry) int { return compareDots(a.Dot, b.Dot) })
		if len(live) > 0 {
			next.Entries[w.Key] = live
		} else {
			delete(next.Entries, w.Key)
		}
	}
	if len(writes) > 0 {
		next.RosterOps = append(slices.Clip(s.RosterOps), op)
	}
	return next, nil
}

// compareDots orders dots by origin, then seq.
func compareDots(a, b Dot) int {
	return cmp.Or(bytes.Compare(a.Origin[:], b.Origin[:]), cmp.Compare(a.Seq, b.Seq))
}

// readWrites checks every write op makes, and is the roster entries among them: a
// key written twice, a key of a kind no replica keeps, or a value that does not read
// refuses the op.
func readWrites(op Op) (map[devicekey.ID]Member, error) {
	roster := map[devicekey.ID]Member{}
	keys := make(map[string]bool, len(op.Writes))
	for _, w := range op.Writes {
		if keys[w.Key] {
			return nil, fmt.Errorf("%w: %s twice", ErrBadWrite, w.Key)
		}
		keys[w.Key] = true
		if strings.HasPrefix(w.Key, rosterPrefix) {
			id, m, err := readRosterWrite(w)
			if err != nil {
				return nil, err
			}
			roster[id] = m
			continue
		}
		if err := readWrite(op.Origin, w); err != nil {
			return nil, err
		}
	}
	return roster, nil
}

// holds reports whether s holds every op ctx names.
func (s State) holds(ctx []Dot) bool {
	for _, d := range ctx {
		if !s.Vector.Covers(d) {
			return false
		}
	}
	return true
}

// covers reports whether op had seen the op d names when it was written.
func covers(op Op, d Dot) bool {
	if d.Origin == op.Origin {
		return d.Seq < op.Seq
	}
	for _, c := range op.Context {
		if c.Origin == d.Origin {
			return c.Seq >= d.Seq
		}
	}
	return false
}

// accountsPrefix and accountsSuffix wrap the bridge's ID in its accounts key.
const (
	accountsPrefix = "bridge/"
	accountsSuffix = "/accounts"
)

// AccountsKey is the key of the accounts the bridge id runs, which it alone writes.
func AccountsKey(id devicekey.ID) string { return accountsPrefix + id.String() + accountsSuffix }

// Account is one network account a bridge runs: the network, the account's own ID on
// it, and how the bridge labels it.
type Account struct {
	Network string `json:"network"`
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
}

// EncodeAccounts is accounts as their key's value.
func EncodeAccounts(accounts []Account) ([]byte, error) {
	v, err := json.Marshal(accounts)
	if err != nil {
		return nil, fmt.Errorf("crdt: encode accounts: %w", err)
	}
	return v, nil
}

// ReadAccounts reads an accounts key's value.
func ReadAccounts(value []byte) ([]Account, error) {
	var out []Account
	if err := json.Unmarshal(value, &out); err != nil {
		return nil, fmt.Errorf("%w: accounts: %w", ErrBadWrite, err)
	}
	return out, nil
}

// readAccountsWrite checks a write by origin as an accounts write: only a bridge's own
// key, holding a list of accounts. Any other key is of a kind not kept yet.
func readAccountsWrite(origin devicekey.ID, w Write) error {
	idText, ok := strings.CutPrefix(w.Key, accountsPrefix)
	if ok {
		idText, ok = strings.CutSuffix(idText, accountsSuffix)
	}
	if !ok {
		return fmt.Errorf("%w: %q", ErrBadWrite, w.Key)
	}
	id, err := devicekey.ParseID(idText)
	switch {
	case err != nil:
		return fmt.Errorf("%w: %w", ErrBadWrite, err)
	case id != origin:
		return fmt.Errorf("%w: %s is that bridge's to write", ErrBadWrite, w.Key)
	}
	_, err = ReadAccounts(w.Value)
	return err
}

// Snapshot is a replica's state for a peer behind the horizon, whose ops it no longer
// keeps: every entry but the roster's with its dot and clock, the roster's ops, the
// vector, and the clock.
type Snapshot struct {
	Entries   map[string][]Entry
	RosterOps []Op
	Vector    Vector
	Clock     Clock
}

// Snapshot is s as a snapshot, its clock clock.
func (s State) Snapshot(clock Clock) Snapshot {
	entries := make(map[string][]Entry, len(s.Entries))
	for key, e := range s.Entries {
		if !strings.HasPrefix(key, rosterPrefix) {
			entries[key] = e
		}
	}
	return Snapshot{Entries: entries, RosterOps: s.RosterOps, Vector: maps.Clone(s.Vector), Clock: clock}
}

// ErrBadSnapshot is a snapshot that does not hold together: an entry its own vector
// does not cover, or written by a bridge other than the one that owns its key.
var ErrBadSnapshot = errors.New("crdt: the snapshot does not hold together")

// Merge is s with snap merged in. The roster's ops each pass Apply's membership and
// signature rules, so a replica bootstrapping from a snapshot checks the roster back to
// its genesis rather than take the sender's word for who is paired. Every other key
// merges entry by entry: an entry of s whose dot snap's vector covers but snap lacks
// was replaced or deleted meanwhile, and goes; an entry of snap whose dot s covers but
// lacks went here, and is not brought back. forks are the dots both vectors hold with
// different hashes: an origin that signed two ops under one dot.
func (s State) Merge(snap Snapshot) (next State, forks []Dot, err error) {
	if bad := snap.check(); bad != nil {
		return s, nil, bad
	}
	next = s
	for _, op := range snap.RosterOps {
		if s.Vector.Covers(Dot{Origin: op.Origin, Seq: op.Seq}) || slices.ContainsFunc(next.RosterOps, func(o Op) bool {
			return o.Origin == op.Origin && o.Seq == op.Seq
		}) {
			continue
		}
		if next, err = next.take(op); err != nil {
			return s, nil, fmt.Errorf("the snapshot's roster: %w", err)
		}
	}
	next.Entries = maps.Clone(next.Entries)
	if next.Entries == nil {
		next.Entries = map[string][]Entry{}
	}
	keys := map[string]bool{}
	for key := range s.Entries {
		keys[key] = !strings.HasPrefix(key, rosterPrefix)
	}
	for key := range snap.Entries {
		keys[key] = true
	}
	for key, merge := range keys {
		if !merge {
			continue
		}
		if live := join(s.Entries[key], s.Vector, snap.Entries[key], snap.Vector); len(live) > 0 {
			next.Entries[key] = live
		} else {
			delete(next.Entries, key)
		}
	}
	next.Vector = maps.Clone(s.Vector)
	if next.Vector == nil {
		next.Vector = Vector{}
	}
	for origin, h := range snap.Vector {
		mine, ok := next.Vector[origin]
		switch {
		case !ok || h.Seq > mine.Seq:
			next.Vector[origin] = h
		case h.Seq == mine.Seq && h.Hash != mine.Hash:
			forks = append(forks, Dot{Origin: origin, Seq: h.Seq})
		}
	}
	return next, forks, nil
}

// check refuses a snapshot whose entries its own vector does not cover, or that are
// not what their keys hold.
func (snap Snapshot) check() error {
	for key, entries := range snap.Entries {
		for _, e := range entries {
			if !snap.Vector.Covers(e.Dot) {
				return fmt.Errorf("%w: %s at %d is past its vector", ErrBadSnapshot, key, e.Dot.Seq)
			}
			if err := readWrite(e.Dot.Origin, Write{Key: key, Value: e.Value}); err != nil {
				return fmt.Errorf("%w: %w", ErrBadSnapshot, err)
			}
		}
	}
	return nil
}

// join is one key's entries merged: mine (as of mv) and theirs (as of tv). An entry
// one side lacks though its vector covers the entry's dot was replaced there, and goes.
func join(mine []Entry, mv Vector, theirs []Entry, tv Vector) []Entry {
	var live []Entry
	for _, e := range mine {
		if slices.ContainsFunc(theirs, sameEntry(e)) || !tv.Covers(e.Dot) {
			live = append(live, e)
		}
	}
	for _, e := range theirs {
		if !slices.ContainsFunc(mine, sameEntry(e)) && !mv.Covers(e.Dot) {
			live = append(live, e)
		}
	}
	slices.SortFunc(live, func(a, b Entry) int { return compareDots(a.Dot, b.Dot) })
	return live
}

// sameEntry matches an entry written by the same op.
func sameEntry(e Entry) func(Entry) bool {
	return func(o Entry) bool { return o.Dot == e.Dot }
}
