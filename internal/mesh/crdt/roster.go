package crdt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// Role is what a roster member is: a bridge (a kithd, which keeps a replica and signs
// ops) or a device (a client of a bridge, whose writes are its bridge's ops).
type Role string

// The roles.
const (
	RoleBridge Role = "bridge"
	RoleDevice Role = "device"
)

// Member is a roster entry's value: who paired the device and when, its role, and the
// public key that names it (PKIX), when known. A bridge's key is always known, since it
// signs; a device paired by its pasted ID has none. A removed member stays listed as
// removed.
type Member struct {
	Key     []byte    `json:"key,omitempty"`
	Name    string    `json:"name,omitempty"`
	Role    Role      `json:"role"`
	AddedAt time.Time `json:"added_at"`
	AddedBy string    `json:"added_by"`
	Removed bool      `json:"removed,omitempty"`
}

// Encode is the member as a roster write's value.
func (m Member) Encode() ([]byte, error) {
	v, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("crdt: encode a roster entry: %w", err)
	}
	return v, nil
}

// rosterPrefix starts every roster key; the device ID follows.
const rosterPrefix = "roster/"

// RosterKey is the key of id's roster entry.
func RosterKey(id devicekey.ID) string { return rosterPrefix + id.String() }

// Entry is a key's current value and the op that wrote it.
type Entry struct {
	Value []byte
	Dot   Dot
	Clock Clock
}

// State is a replica's entries and its version vector.
type State struct {
	Entries map[string]Entry
	Vector  Vector
}

// Why an op is refused.
var (
	ErrDuplicate    = errors.New("crdt: the op is held already")
	ErrNotReady     = errors.New("crdt: the op follows ops not held yet")
	ErrNotAMember   = errors.New("crdt: the op's signer is not a bridge of the roster")
	ErrBadSignature = errors.New("crdt: the op's signature does not verify")
	ErrBadWrite     = errors.New("crdt: the op writes what no replica takes")
)

// Apply is the state with op applied, all of it or none: op is refused unless it is the
// next of its origin, follows only ops held, writes roster entries that read, and is
// signed by a bridge of the roster as it stands (or, on an empty state, is the genesis:
// signed by the bridge whose entry it writes). Membership is checked against the roster
// the op is applied over: ops are applied in causal order, and with one replica that is
// the roster the op's context names.
func (s State) Apply(op Op) (State, error) {
	held := s.Vector[op.Origin]
	switch {
	case op.Seq <= held.Seq:
		return s, ErrDuplicate
	case op.Seq != held.Seq+1 || !s.holds(op.Context):
		return s, ErrNotReady
	}
	writes := make(map[devicekey.ID]Member, len(op.Writes))
	for _, w := range op.Writes {
		id, m, err := readRosterWrite(w)
		if err != nil {
			return s, err
		}
		if _, twice := writes[id]; twice {
			return s, fmt.Errorf("%w: %s twice", ErrBadWrite, w.Key)
		}
		writes[id] = m
	}
	roster, err := s.Roster()
	if err != nil {
		return s, err
	}
	if len(s.Vector) == 0 {
		roster = writes // the genesis: its signer adds itself
	}
	signer, ok := roster[op.Origin]
	if !ok || signer.Removed || signer.Role != RoleBridge {
		return s, ErrNotAMember
	}
	if err := verify(signer.Key, op); err != nil {
		return s, err
	}
	next := State{Entries: maps.Clone(s.Entries), Vector: maps.Clone(s.Vector)}
	if next.Entries == nil {
		next.Entries, next.Vector = map[string]Entry{}, Vector{}
	}
	for id, m := range writes {
		key := RosterKey(id)
		written := Entry{Value: valueOf(op, key), Dot: Dot{Origin: op.Origin, Seq: op.Seq}, Clock: op.Clock}
		if old, ok := next.Entries[key]; ok && !covers(op, old.Dot) && !wins(m, written, old) {
			continue
		}
		next.Entries[key] = written
	}
	next.Vector[op.Origin] = Head{Seq: op.Seq, Hash: op.digest()}
	return next, nil
}

// valueOf is the value op writes to key.
func valueOf(op Op, key string) []byte {
	for _, w := range op.Writes {
		if w.Key == key {
			return w.Value
		}
	}
	return nil
}

// holds reports whether s holds every op ctx names.
func (s State) holds(ctx []Dot) bool {
	for _, d := range ctx {
		if s.Vector[d.Origin].Seq < d.Seq {
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

// wins reports whether m, written as written, takes the place of a roster entry written
// concurrently: a removal wins (removing is the safer error; re-pair), and between two
// of a kind the later by (clock, origin).
func wins(m Member, written, old Entry) bool {
	var was Member
	if json.Unmarshal(old.Value, &was) != nil || m.Removed != was.Removed {
		return m.Removed
	}
	if written.Clock != old.Clock {
		return written.Clock > old.Clock
	}
	return string(written.Dot.Origin[:]) > string(old.Dot.Origin[:])
}

// readRosterWrite reads a write as a roster entry: its key names a device ID, its value
// a member of a known role, whose key, when it carries one, is a P-256 key with that ID.
// A bridge that is not removed carries its key.
func readRosterWrite(w Write) (devicekey.ID, Member, error) {
	idText, ok := strings.CutPrefix(w.Key, rosterPrefix)
	if !ok {
		return devicekey.ID{}, Member{}, fmt.Errorf("%w: %q", ErrBadWrite, w.Key)
	}
	id, err := devicekey.ParseID(idText)
	if err != nil {
		return devicekey.ID{}, Member{}, fmt.Errorf("%w: %w", ErrBadWrite, err)
	}
	var m Member
	if err := json.Unmarshal(w.Value, &m); err != nil {
		return devicekey.ID{}, Member{}, fmt.Errorf("%w: %s: %w", ErrBadWrite, w.Key, err)
	}
	switch {
	case m.Role != RoleBridge && m.Role != RoleDevice:
		return devicekey.ID{}, Member{}, fmt.Errorf("%w: %s: role %q", ErrBadWrite, w.Key, m.Role)
	case m.Role == RoleBridge && !m.Removed && len(m.Key) == 0:
		return devicekey.ID{}, Member{}, fmt.Errorf("%w: %s: a bridge with no key", ErrBadWrite, w.Key)
	}
	if len(m.Key) > 0 {
		pub, err := publicKey(m.Key)
		if err != nil {
			return devicekey.ID{}, Member{}, fmt.Errorf("%w: %s: %w", ErrBadWrite, w.Key, err)
		}
		if keyID, err := devicekey.IDOf(pub); err != nil || keyID != id {
			return devicekey.ID{}, Member{}, fmt.Errorf("%w: %s: the key is another device's", ErrBadWrite, w.Key)
		}
	}
	return id, m, nil
}

// publicKey reads a member's key: P-256, as every device key is.
func publicKey(der []byte) (*ecdsa.PublicKey, error) {
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("crdt: a member's key: %w", err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("crdt: a member's key is not ECDSA P-256")
	}
	return pub, nil
}

// verify checks op's signature against the signer's key.
func verify(key []byte, op Op) error {
	pub, err := publicKey(key)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadSignature, err)
	}
	digest := op.digest()
	if !ecdsa.VerifyASN1(pub, digest[:], op.Sig) {
		return ErrBadSignature
	}
	return nil
}

// Roster is every device the state's roster lists, the removed among them.
func (s State) Roster() (map[devicekey.ID]Member, error) {
	out := map[devicekey.ID]Member{}
	for key, e := range s.Entries {
		if !strings.HasPrefix(key, rosterPrefix) {
			continue
		}
		id, m, err := readRosterWrite(Write{Key: key, Value: e.Value})
		if err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, nil
}
