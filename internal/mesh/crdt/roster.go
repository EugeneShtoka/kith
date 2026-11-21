package crdt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
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
// signs and pairs over TLS; a device paired by its pasted ID has none. A bridge's
// Sealing is the X25519 key content keys are wrapped to (EpochRecord), apart from the
// key it signs with; a thin device has none, holding no content key. A bridge's
// Addresses are where it is dialed (its [remote] listen); a bridge that only dials has
// none. A removed member stays listed as removed. Rotated is the rotation that made
// the entry, signed by the key it replaced and its own; RotatedTo, on the removed entry
// of that replaced key, names the entry it made (Settle).
type Member struct {
	Key       []byte    `json:"key,omitempty"`
	Sealing   []byte    `json:"sealing,omitempty"`
	Name      string    `json:"name,omitempty"`
	Role      Role      `json:"role"`
	Addresses []string  `json:"addresses,omitempty"`
	AddedAt   time.Time `json:"added_at"`
	AddedBy   string    `json:"added_by"`
	Removed   bool      `json:"removed,omitempty"`

	Rotated   *devicekey.Rotation `json:"rotated,omitempty"`
	RotatedTo string              `json:"rotated_to,omitempty"`
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

// IsRosterKey reports whether key is a roster entry's.
func IsRosterKey(key string) bool { return strings.HasPrefix(key, rosterPrefix) }

// rosterWrite is one roster write of an op: whose entry, as what, and the op.
type rosterWrite struct {
	id     devicekey.ID
	member Member
	op     *Op
}

// rosterAt is the roster as of op's context: every roster op taken whose dot the
// context (or the op's own origin before it) holds, each entry's writes no other of
// them had seen, resolved. Every replica holding op holds those same roster ops, so
// every replica reads the same members for it, whatever else it holds and whatever
// order it took them in.
func (s State) rosterAt(op Op) map[devicekey.ID]Member {
	held := make(map[devicekey.ID]uint64, len(op.Context)+1)
	for _, d := range op.Context {
		held[d.Origin] = max(held[d.Origin], d.Seq)
	}
	held[op.Origin] = max(held[op.Origin], op.Seq-1)
	byID := map[devicekey.ID][]rosterWrite{}
	for i := range s.RosterOps {
		o := &s.RosterOps[i]
		if o.Seq > held[o.Origin] {
			continue
		}
		for _, w := range o.Writes {
			id, m, err := readRosterWrite(w)
			if err != nil {
				continue // taken, so it read
			}
			byID[id] = append(byID[id], rosterWrite{id: id, member: m, op: o})
		}
	}
	out := make(map[devicekey.ID]Member, len(byID))
	for id, writes := range byID {
		var live []Entry
		var members []Member
		for i := range writes {
			w := &writes[i]
			dot := Dot{Origin: w.op.Origin, Seq: w.op.Seq}
			seen := false
			for j := range writes {
				if other := writes[j].op; other != w.op && covers(*other, dot) {
					seen = true
					break
				}
			}
			if !seen {
				live = append(live, Entry{Dot: dot, Clock: w.op.Clock})
				members = append(members, w.member)
			}
		}
		out[id] = members[resolve(live, members)]
	}
	settle(out)
	return out
}

// resolve is which of a roster entry's concurrent writes stands: a removal wins
// (removing is the safer error; re-pair), a plain one over a rotation's, and between
// two of a kind the later by (clock, origin). Every replica resolves the same writes
// alike.
func resolve(live []Entry, members []Member) int {
	best := 0
	for i := 1; i < len(live); i++ {
		if members[i].Removed != members[best].Removed {
			if members[i].Removed {
				best = i
			}
			continue
		}
		if plain, was := members[i].RotatedTo == "", members[best].RotatedTo == ""; members[i].Removed && plain != was {
			if plain {
				best = i
			}
			continue
		}
		if later(live[i], live[best]) {
			best = i
		}
	}
	return best
}

// later reports whether a was written after b by (clock, origin).
func later(a, b Entry) bool {
	if a.Clock != b.Clock {
		return a.Clock > b.Clock
	}
	return string(a.Dot.Origin[:]) > string(b.Dot.Origin[:])
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
	case m.RotatedTo != "" && !m.Removed, m.Rotated != nil && len(m.Key) == 0:
		return devicekey.ID{}, Member{}, fmt.Errorf("%w: %s: a rotation's entries out of shape", ErrBadWrite, w.Key)
	}
	if m.RotatedTo != "" {
		if _, err := devicekey.ParseID(m.RotatedTo); err != nil {
			return devicekey.ID{}, Member{}, fmt.Errorf("%w: %s: %w", ErrBadWrite, w.Key, err)
		}
	}
	if m.Rotated != nil {
		if _, err := m.Rotated.Verify(m.Key); err != nil {
			return devicekey.ID{}, Member{}, fmt.Errorf("%w: %s: %w", ErrBadWrite, w.Key, err)
		}
	}
	if len(m.Sealing) > 0 {
		if _, err := SealingKey(m.Sealing); err != nil {
			return devicekey.ID{}, Member{}, fmt.Errorf("%w: %s: %w", ErrBadWrite, w.Key, err)
		}
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

// Verify checks op's signature against key, the signer's (PKIX).
func Verify(key []byte, op Op) error { return verify(key, op.Digest(), op.Sig) }

func verify(key []byte, digest [sha256.Size]byte, sig []byte) error {
	pub, err := publicKey(key)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadSignature, err)
	}
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return ErrBadSignature
	}
	return nil
}

// Roster is every device the state's roster lists, the removed among them: each
// entry's concurrent writes resolved.
func (s State) Roster() (map[devicekey.ID]Member, error) {
	out := map[devicekey.ID]Member{}
	for key, entries := range s.Entries {
		if !strings.HasPrefix(key, rosterPrefix) {
			continue
		}
		members := make([]Member, 0, len(entries))
		var id devicekey.ID
		for _, e := range entries {
			var m Member
			var err error
			if id, m, err = readRosterWrite(Write{Key: key, Value: e.Value}); err != nil {
				return nil, err
			}
			members = append(members, m)
		}
		if len(members) > 0 {
			out[id] = members[resolve(entries, members)]
		}
	}
	settle(out)
	return out, nil
}

// settle removes each entry a rotation made whose replaced key's entry does not stand
// as rotated to it, nor that one's in turn: a removal of the old key concurrent with the
// rotation won (resolve), or another rotation of it did, and the removal takes the new
// key along; so does the old key paired again (removing is the safer error). Every
// replica holding the same writes settles them alike.
func settle(roster map[devicekey.ID]Member) {
	var falls func(id devicekey.ID, depth int) bool
	falls = func(id devicekey.ID, depth int) bool {
		m := roster[id]
		if m.Rotated == nil {
			return false
		}
		from := devicekey.ID(sha256.Sum256(m.Rotated.From))
		old, ok := roster[from]
		return !ok || depth >= len(roster) || !old.Removed || old.RotatedTo != id.String() || falls(from, depth+1)
	}
	for id := range roster {
		if m := roster[id]; !m.Removed && falls(id, 0) {
			m.Removed = true
			roster[id] = m
		}
	}
}

// Successor is the member that holds id's place now: id itself while it stands, else
// the entry its rotation made, followed on; false when that chain ends in a removal.
func Successor(roster map[devicekey.ID]Member, id devicekey.ID) (devicekey.ID, bool) {
	for range len(roster) + 1 {
		m, ok := roster[id]
		switch {
		case !ok:
			return devicekey.ID{}, false
		case !m.Removed:
			return id, true
		case m.RotatedTo == "":
			return devicekey.ID{}, false
		}
		id, _ = devicekey.ParseID(m.RotatedTo) // readRosterWrite checked it
	}
	return devicekey.ID{}, false
}
