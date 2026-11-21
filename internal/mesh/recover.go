package mesh

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// Recovery (planning/design-mailbox.md, Recovery): a mesh's recovery key is written as
// its public half, every epoch is wrapped for it, and with the secret a new kithd, paired
// with the mailbox alone, opens what the mailbox holds and starts a new mesh from it.

// Why a recovery fails.
var (
	// ErrWrongRecoveryKey is a recovery key that opens nothing the mailbox holds: another
	// mesh's, or one made after what the mailbox holds was sealed.
	ErrWrongRecoveryKey = errors.New("mesh: the recovery key opens nothing the mailbox holds")
	// ErrNotFresh is a bridge in a mesh with others: recovering starts a mesh anew.
	ErrNotFresh = errors.New("mesh: this bridge is in a mesh with other bridges; recover on a new kithd, paired with the mailbox alone")
)

// NewRecoveryKey makes a recovery key for the mesh and writes its public half, signed
// by signer: a later one replaces it, and either opens what was sealed while it stood.
// It returns the key as the person writes it down (devicekey.Secret).
func (s *Store) NewRecoveryKey(ctx context.Context, signer crypto.Signer) (string, error) {
	secret := make([]byte, crdt.KeyLen)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("mesh: make a recovery key: %w", err)
	}
	priv, err := crdt.RecoveryPrivate(secret)
	if err != nil {
		return "", fmt.Errorf("mesh: %w", err)
	}
	origin, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return "", fmt.Errorf("mesh: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeLocked(ctx, signer, origin, []crdt.Write{{Key: crdt.RecoveryKeyName, Value: priv.PublicKey().Bytes()}}); err != nil {
		return "", err
	}
	return devicekey.Secret(secret, devicekey.MeshRecovery), nil
}

// Recover makes this bridge (signer), in a mesh of its own (ErrNotFresh otherwise), the
// start of the mesh whose recovery key secret is, from what the mailbox holds, over
// frames: it asks for everything, opens the epochs wrapped for the key, replays the
// snapshots and ops in a mesh of their own (each checked as any bridge checks them),
// and writes what that holds as its own ops: each key's latest value (a key's
// concurrent values but the latest are dropped), the old mesh's members as removed, and
// the recovery key, so the same one goes on opening the new mesh. Accounts stay with
// the bridges that ran them; the mailbox is this mesh's already.
func (s *Store) Recover(ctx context.Context, signer crypto.Signer, secret []byte, mailbox devicekey.ID, frames Frames) (domain.MeshRecovered, error) {
	self, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return domain.MeshRecovered{}, fmt.Errorf("mesh: %w", err)
	}
	if ferr := s.fresh(self); ferr != nil {
		return domain.MeshRecovered{}, ferr
	}
	priv, err := crdt.RecoveryPrivate(secret)
	if err != nil {
		return domain.MeshRecovered{}, fmt.Errorf("mesh: %w", err)
	}
	h, err := everything(self, mailbox, frames)
	if err != nil {
		return domain.MeshRecovered{}, err
	}
	keys := map[crdt.Epoch][]byte{}
	for i := range h.records {
		r := &h.records[i]
		for _, w := range r.Wraps {
			if w.To == crdt.RecoveryID(priv.PublicKey().Bytes()) {
				if key, uerr := crdt.Unwrap(r.Epoch, w, priv); uerr == nil {
					keys[r.Epoch] = key
				}
			}
		}
	}
	old := replay(h, keys)
	if len(keys) == 0 || len(old.Vector) == 0 {
		return domain.MeshRecovered{}, ErrWrongRecoveryKey
	}
	writes, members, err := adoptWrites(old, self)
	if err != nil {
		return domain.MeshRecovered{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeAllLocked(ctx, signer, self, writes); err != nil {
		return domain.MeshRecovered{}, err
	}
	n := len(slices.DeleteFunc(slices.Clone(writes), func(w crdt.Write) bool { return crdt.IsRosterKey(w.Key) }))
	return domain.MeshRecovered{Keys: n, Members: members}, nil
}

// fresh refuses a store whose roster lists a bridge other than self.
func (s *Store) fresh(self devicekey.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	roster, err := s.state.Roster()
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	for id := range roster {
		if m := roster[id]; id != self && !m.Removed && m.Role == crdt.RoleBridge {
			return ErrNotFresh
		}
	}
	return nil
}

// held is everything a mailbox passed on in one session: epoch records, sealed
// snapshots and envelopes.
type held struct {
	records   []crdt.EpochRecord
	snapshots []*meshv1.SealedSnapshot
	envelopes [][]byte
}

// everything asks the mailbox for all it holds, saying Hello as self with nothing held,
// and gathers what it passes on until it says it has passed on all it held then (its
// first Ack).
func everything(self, mailbox devicekey.ID, frames Frames) (held, error) {
	var out held
	if err := frames.Send(helloFrame(self, time.Now(), nil, nil)); err != nil {
		return out, fmt.Errorf("mesh: say hello: %w", err)
	}
	f, err := frames.Receive()
	if err != nil {
		return out, fmt.Errorf("mesh: read the mailbox's hello: %w", err)
	}
	h := f.GetHello()
	switch {
	case h == nil || h.GetMailbox() == nil || !bytes.Equal(h.GetReplica(), mailbox[:]):
		return out, fmt.Errorf("%w: it did not say hello as the mailbox", errProtocol)
	case h.GetProtocol() != protocol:
		return out, fmt.Errorf("%w: it speaks protocol %d, this kith %d", errProtocol, h.GetProtocol(), protocol)
	}
	out.records = parseRecords(h.GetEpochs().GetRecords())
	var gathering *meshv1.SealedSnapshot
	for {
		if f, err = frames.Receive(); err != nil {
			return out, fmt.Errorf("mesh: read what the mailbox holds: %w", err)
		}
		switch {
		case f.GetAck() != nil:
			return out, nil
		case f.GetEpochs() != nil:
			out.records = append(out.records, parseRecords(f.GetEpochs().GetRecords())...)
		case f.GetEnvelopes() != nil:
			out.envelopes = append(out.envelopes, f.GetEnvelopes().GetEnvelopes()...)
		case f.GetSealedSnapshot() != nil:
			chunk := f.GetSealedSnapshot()
			if gathering == nil {
				gathering = &meshv1.SealedSnapshot{}
			}
			gathering.Boxes = append(gathering.Boxes, chunk.GetBoxes()...)
			if chunk.GetLast() {
				gathering.Maker, gathering.Vector, gathering.Sig, gathering.Last = chunk.GetMaker(), chunk.GetVector(), chunk.GetSig(), true
				out.snapshots, gathering = append(out.snapshots, gathering), nil
			}
		default:
			return out, fmt.Errorf("%w: a frame out of place from a mailbox", errProtocol)
		}
	}
}

// parseRecords is each of wire that reads as an epoch record.
func parseRecords(wire []*meshv1.EpochRecord) []crdt.EpochRecord {
	var out []crdt.EpochRecord
	for _, w := range wire {
		if r, err := crdt.ParseEpochRecord(w.GetBody(), w.GetSig()); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// replay is the mesh h holds, as far as keys open it: each snapshot that opens, signed
// by its maker, a bridge of its roster, merged in turn; then each envelope that opens,
// applied in an order that follows causality, as any bridge applies a peer's ops.
// What does not open, or is refused, is passed over.
func replay(h held, keys map[crdt.Epoch][]byte) crdt.State {
	var st crdt.State
	for _, sealed := range h.snapshots {
		if snap, ok := openSealed(sealed, keys); ok {
			if next, err := mergeSigned(st, sealed, snap); err == nil {
				st = next
			}
		}
	}
	var ops []crdt.Op
	for _, env := range h.envelopes {
		e, err := crdt.SealedEpoch(env)
		if key, ok := keys[e]; err == nil && ok {
			if op, oerr := crdt.Open(env, key); oerr == nil {
				ops = append(ops, op)
			}
		}
	}
	slices.SortFunc(ops, byClock)
	for progress := true; progress; {
		progress = false
		var rest []crdt.Op
		for i := range ops {
			next, err := st.Apply(ops[i])
			switch {
			case err == nil:
				st, progress = next, true
			case errors.Is(err, crdt.ErrNotReady):
				rest = append(rest, ops[i])
			}
		}
		ops = rest
	}
	return st
}

// openSealed opens a sealed snapshot with keys, when they hold its epoch's.
func openSealed(sealed *meshv1.SealedSnapshot, keys map[crdt.Epoch][]byte) (*crdt.Snapshot, bool) {
	if len(sealed.GetBoxes()) == 0 || len(sealed.GetMaker()) != len(devicekey.ID{}) {
		return nil, false
	}
	e, err := crdt.SnapshotEpoch(sealed.GetBoxes()[0])
	key, ok := keys[e]
	if err != nil || !ok {
		return nil, false
	}
	plain, err := crdt.OpenSnapshot(sealed.GetBoxes(), devicekey.ID(sealed.GetMaker()), key)
	if err != nil {
		return nil, false
	}
	var whole meshv1.Snapshot
	if proto.Unmarshal(plain, &whole) != nil {
		return nil, false
	}
	whole.Last = true
	snap, err := gather(nil, &whole)
	return snap, err == nil
}

// mergeSigned is st with snap merged, once sealed's signature is its maker's, a bridge
// of the roster merged.
func mergeSigned(st crdt.State, sealed *meshv1.SealedSnapshot, snap *crdt.Snapshot) (crdt.State, error) {
	next, _, err := st.Merge(*snap)
	if err != nil {
		return st, fmt.Errorf("mesh: %w", err)
	}
	roster, err := next.Roster()
	if err != nil {
		return st, fmt.Errorf("mesh: %w", err)
	}
	clear, err := vectorOf(sealed.GetVector())
	if err != nil {
		return st, err
	}
	maker := devicekey.ID(sealed.GetMaker())
	m := roster[maker]
	if m.Removed || m.Role != crdt.RoleBridge {
		return st, errBadSealedSnapshot
	}
	if err := crdt.VerifySnapshot(m.Key, crdt.SnapshotDigest(maker, clear.Context(), sealed.GetBoxes()), sealed.GetSig()); err != nil {
		return st, fmt.Errorf("%w: %w", errBadSealedSnapshot, err)
	}
	return next, nil
}

// adoptWrites is what old (a mesh replayed) becomes as self's writes: each key's latest
// value but the roster's and the accounts' (a bridge's own), then each of its members
// but self and the mailboxes, as removed. members are those members as they stood.
func adoptWrites(old crdt.State, self devicekey.ID) (writes []crdt.Write, members []domain.Device, err error) {
	for _, key := range slices.Sorted(maps.Keys(old.Entries)) {
		entries := old.Entries[key]
		if crdt.IsRosterKey(key) || strings.HasPrefix(key, "bridge/") || len(entries) == 0 {
			continue
		}
		latest := slices.MaxFunc(entries, func(a, b crdt.Entry) int {
			return cmp.Or(cmp.Compare(a.Clock, b.Clock), bytes.Compare(a.Dot.Origin[:], b.Dot.Origin[:]))
		})
		writes = append(writes, crdt.Write{Key: key, Value: latest.Value})
	}
	roster, err := old.Roster()
	if err != nil {
		return nil, nil, fmt.Errorf("mesh: %w", err)
	}
	for _, id := range slices.SortedFunc(maps.Keys(roster), func(a, b devicekey.ID) int { return bytes.Compare(a[:], b[:]) }) {
		m := roster[id]
		if id == self || m.Role == crdt.RoleMailbox {
			continue
		}
		if !m.Removed {
			members = append(members, deviceOf(id, &m))
		}
		m.Removed, m.RotatedTo = true, ""
		value, eerr := m.Encode()
		if eerr != nil {
			return nil, nil, fmt.Errorf("mesh: %w", eerr)
		}
		writes = append(writes, crdt.Write{Key: crdt.RosterKey(id), Value: value})
	}
	return writes, members, nil
}
