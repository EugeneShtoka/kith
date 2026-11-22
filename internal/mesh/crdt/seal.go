package crdt

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// Epoch names a content key: its number, and the bridge that made it. Two bridges may
// make the same number at once; both stand, and writers move to the later by (number,
// maker), so they converge on one.
type Epoch struct {
	Number uint64
	Maker  devicekey.ID
}

// After reports whether e is later than o by (number, maker).
func (e Epoch) After(o Epoch) bool {
	if e.Number != o.Number {
		return e.Number > o.Number
	}
	return bytes.Compare(e.Maker[:], o.Maker[:]) > 0
}

func (e Epoch) String() string { return fmt.Sprintf("%d by %s", e.Number, e.Maker) }

func appendEpoch(b []byte, e Epoch) []byte {
	return append(binary.AppendUvarint(b, e.Number), e.Maker[:]...)
}

func (r *bodyReader) epoch() Epoch {
	n := r.uvarint()
	return Epoch{Number: n, Maker: devicekey.ID(r.take(idLen))}
}

// ── Envelopes ──

// sealedFormat starts every envelope: what it is, and which encoding.
const sealedFormat = "kith mesh sealed 1\n"

// The envelope's shape: a content key's length, a nonce's, and the block that body ‖ sig
// are padded to, so that a read position, a draft and a config write do not tell
// themselves apart by size.
const (
	KeyLen   = 32
	nonceLen = 12
	padBlock = 4 << 10
)

// Why an envelope does not open.
var (
	ErrNotSealed = errors.New("crdt: an envelope does not read")
	ErrBadSeal   = errors.New("crdt: an envelope does not open to its op")
)

// Seal is op's envelope under the content key of epoch: its origin, seq and epoch in the
// clear, and body ‖ sig padded to a multiple of 4 KiB in an AES-256-GCM box whose
// additional data is those clear fields, so a box moved to another origin, seq or epoch
// does not open. The nonce is random: an epoch's maker moves on long before 2³² boxes.
func Seal(op Op, epoch Epoch, key []byte) ([]byte, error) {
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	return sealBox(aead, sealedHead(op.Origin, op.Seq, epoch), pad(appendBytes(appendBytes(nil, op.Body()), op.Sig)))
}

// pad is plain padded with zeros to a multiple of padBlock.
func pad(plain []byte) []byte {
	return append(plain, make([]byte, (padBlock-len(plain)%padBlock)%padBlock)...)
}

// sealBox is head, a random nonce, and plain sealed by aead with head as additional
// data.
func sealBox(aead cipher.AEAD, head, plain []byte) ([]byte, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crdt: seal: %w", err)
	}
	return aead.Seal(append(head, nonce...), nonce, plain, head), nil
}

// SealedEpoch is the epoch an envelope names.
func SealedEpoch(envelope []byte) (Epoch, error) {
	_, _, epoch, _, err := readSealed(envelope)
	return epoch, err
}

// Unseal checks that envelope, opened with key, is op's: named by op's origin and seq,
// and holding exactly its body and signature.
func Unseal(envelope []byte, key []byte, op Op) error {
	got, err := Open(envelope, key)
	if err != nil {
		return err
	}
	if got.Origin != op.Origin || got.Seq != op.Seq {
		return fmt.Errorf("%w: it names %s's op %d", ErrBadSeal, got.Origin, got.Seq)
	}
	if !bytes.Equal(got.Body(), op.Body()) || !bytes.Equal(got.Sig, op.Sig) {
		return fmt.Errorf("%w: it holds another op", ErrBadSeal)
	}
	return nil
}

// Open is the op envelope holds, opened with key: the op its clear origin and seq
// name, with its signature, and the envelope kept as its own. How a bridge reads what
// a mailbox passes on, which is envelopes alone.
func Open(envelope []byte, key []byte) (Op, error) {
	origin, seq, _, head, err := readSealed(envelope)
	if err != nil {
		return Op{}, err
	}
	plain, err := openBox(envelope, head, key)
	if err != nil {
		return Op{}, err
	}
	r := bodyReader{rest: plain}
	body, sig := r.field(), r.field()
	if r.bad {
		return Op{}, fmt.Errorf("%w: it holds no op", ErrBadSeal)
	}
	op, err := ParseOp(body, sig)
	if err != nil {
		return Op{}, fmt.Errorf("%w: %w", ErrBadSeal, err)
	}
	if op.Origin != origin || op.Seq != seq {
		return Op{}, fmt.Errorf("%w: it holds %s's op %d", ErrBadSeal, op.Origin, op.Seq)
	}
	op.Sealed = envelope
	return op, nil
}

// SealedDot is the dot an envelope names in the clear: all a mailbox reads of one.
func SealedDot(envelope []byte) (Dot, error) {
	origin, seq, _, _, err := readSealed(envelope)
	return Dot{Origin: origin, Seq: seq}, err
}

// openBox opens the AES-256-GCM box that follows head (the bytes it binds) and its
// nonce in sealed, with key.
func openBox(sealed, head, key []byte) ([]byte, error) {
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	nonce := sealed[len(head) : len(head)+nonceLen]
	plain, err := aead.Open(nil, nonce, sealed[len(head)+nonceLen:], head)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadSeal, err)
	}
	return plain, nil
}

// readSealed reads an envelope's clear fields, and its head: the bytes the box binds.
func readSealed(envelope []byte) (origin devicekey.ID, seq uint64, epoch Epoch, head []byte, err error) {
	rest, ok := bytes.CutPrefix(envelope, []byte(sealedFormat))
	if !ok || len(rest) < idLen {
		return origin, 0, Epoch{}, nil, ErrNotSealed
	}
	origin = devicekey.ID(rest[:idLen])
	r := bodyReader{rest: rest[idLen:]}
	seq = r.uvarint()
	epoch = r.epoch()
	if r.bad || len(r.rest) < nonceLen {
		return origin, 0, Epoch{}, nil, ErrNotSealed
	}
	return origin, seq, epoch, envelope[:len(envelope)-len(r.rest)], nil
}

func sealedHead(origin devicekey.ID, seq uint64, epoch Epoch) []byte {
	b := append([]byte(sealedFormat), origin[:]...)
	return appendEpoch(binary.AppendUvarint(b, seq), epoch)
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crdt: a content key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crdt: a content key: %w", err)
	}
	return aead, nil
}

// ── Sealed snapshots ──

// snapshotFormat starts every box of a sealed snapshot, and its signed digest.
const (
	snapshotFormat = "kith mesh snapshot 1\n"
	snapshotSigned = "kith mesh sealed snapshot 1\n"
)

// snapshotIDLen is a sealed snapshot's random name, which every box of it carries: a
// box of another snapshot does not open in its place.
const snapshotIDLen = 16

// snapshotPiece is how much of a snapshot one box holds: a multiple of padBlock that
// keeps a box, head and tag included, within MaxOpSize.
const snapshotPiece = MaxOpSize - padBlock

// Why a sealed snapshot does not open.
var ErrBadSnapshotSeal = errors.New("crdt: a sealed snapshot does not open")

// SealSnapshot is plain (a snapshot, encoded) sealed by maker under the content key of
// epoch: length-prefixed, padded to a multiple of 4 KiB and cut into boxes of at most
// MaxOpSize. Each box's head (its additional data) names the maker, the snapshot, the
// box's place among how many, and the epoch, so boxes do not open moved, dropped or
// taken from another snapshot.
func SealSnapshot(plain []byte, maker devicekey.ID, epoch Epoch, key []byte) ([][]byte, error) {
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	id := make([]byte, snapshotIDLen)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("crdt: seal a snapshot: %w", err)
	}
	whole := pad(appendBytes(nil, plain))
	count := (len(whole) + snapshotPiece - 1) / snapshotPiece
	boxes := make([][]byte, 0, count)
	for i := range count {
		piece := whole[i*snapshotPiece : min(len(whole), (i+1)*snapshotPiece)]
		box, err := sealBox(aead, snapshotHead(maker, id, uint64(i), uint64(count), epoch), piece) // #nosec G115 -- a count of boxes
		if err != nil {
			return nil, err
		}
		boxes = append(boxes, box)
	}
	return boxes, nil
}

// SnapshotEpoch is the epoch a sealed snapshot's box names.
func SnapshotEpoch(box []byte) (Epoch, error) {
	h, err := readSnapshotHead(box)
	return h.epoch, err
}

// OpenSnapshot is what SealSnapshot sealed in boxes, made by maker, opened with key:
// every box of one snapshot, in order, none missing.
func OpenSnapshot(boxes [][]byte, maker devicekey.ID, key []byte) ([]byte, error) {
	var whole []byte
	var first snapshotHeader
	for i, box := range boxes {
		h, err := readSnapshotHead(box)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			first = h
		}
		if h.maker != maker || h.id != first.id || h.epoch != first.epoch || h.count != uint64(len(boxes)) || h.index != uint64(i) { // #nosec G115 -- a count of boxes
			return nil, fmt.Errorf("%w: box %d is not its %d of %d", ErrBadSnapshotSeal, i, i+1, len(boxes))
		}
		piece, err := openBox(box, h.head, key)
		if err != nil {
			return nil, fmt.Errorf("%w: box %d: %w", ErrBadSnapshotSeal, i, err)
		}
		whole = append(whole, piece...)
	}
	r := bodyReader{rest: whole}
	plain := r.field()
	if r.bad || len(boxes) == 0 {
		return nil, fmt.Errorf("%w: it holds no snapshot", ErrBadSnapshotSeal)
	}
	return plain, nil
}

// snapshotHeader is a box's clear head, read.
type snapshotHeader struct {
	maker        devicekey.ID
	id           [snapshotIDLen]byte
	index, count uint64
	epoch        Epoch
	head         []byte
}

func snapshotHead(maker devicekey.ID, id []byte, index, count uint64, epoch Epoch) []byte {
	b := append(append([]byte(snapshotFormat), maker[:]...), id...)
	b = binary.AppendUvarint(binary.AppendUvarint(b, index), count)
	return appendEpoch(b, epoch)
}

func readSnapshotHead(box []byte) (snapshotHeader, error) {
	rest, ok := bytes.CutPrefix(box, []byte(snapshotFormat))
	if !ok {
		return snapshotHeader{}, fmt.Errorf("%w: a box does not read", ErrBadSnapshotSeal)
	}
	r := bodyReader{rest: rest}
	h := snapshotHeader{maker: devicekey.ID(r.take(idLen)), id: [snapshotIDLen]byte(r.take(snapshotIDLen))}
	h.index, h.count, h.epoch = r.uvarint(), r.uvarint(), r.epoch()
	if r.bad || len(r.rest) < nonceLen {
		return snapshotHeader{}, fmt.Errorf("%w: a box does not read", ErrBadSnapshotSeal)
	}
	h.head = box[:len(box)-len(r.rest)]
	return h, nil
}

// SnapshotDigest is what a sealed snapshot's maker signs: the maker, the vector it
// covers in the clear (seqs, by origin), and every box.
func SnapshotDigest(maker devicekey.ID, vector []Dot, boxes [][]byte) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(snapshotSigned))
	h.Write(maker[:])
	sorted := slices.SortedFunc(slices.Values(vector), compareDots)
	h.Write(binary.AppendUvarint(nil, uint64(len(sorted))))
	for _, d := range sorted {
		h.Write(d.Origin[:])
		h.Write(binary.AppendUvarint(nil, d.Seq))
	}
	h.Write(binary.AppendUvarint(nil, uint64(len(boxes))))
	for _, b := range boxes {
		h.Write(appendBytes(nil, b))
	}
	var out [sha256.Size]byte
	h.Sum(out[:0])
	return out
}

// SignSnapshot signs a sealed snapshot's digest with its maker's device key.
func SignSnapshot(key crypto.Signer, digest [sha256.Size]byte) ([]byte, error) {
	sig, err := key.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("crdt: sign a snapshot: %w", err)
	}
	return sig, nil
}

// VerifySnapshot checks a sealed snapshot's signature against key, its maker's (PKIX).
func VerifySnapshot(key []byte, digest [sha256.Size]byte, sig []byte) error {
	return verify(key, digest, sig)
}

// ── Epoch records ──

// recordFormat starts every epoch record's body.
const recordFormat = "kith mesh epoch 1\n"

// Wrap is an epoch's content key sealed to one bridge's sealing key (HPKE, RFC 9180:
// DHKEM(X25519), HKDF-SHA256, AES-256-GCM): the encapsulated key and the ciphertext.
type Wrap struct {
	To  devicekey.ID
	Box []byte
}

// EpochRecord is an epoch's content key wrapped for bridges, signed by the bridge that
// wrote it: the epoch's maker, or a member adding a wrap for a bridge that joined or
// asked. Context is the maker's version vector when it made the epoch: a bridge seals
// under it only once that covers every bridge removal it holds (State.Since), and adds
// a wrap only once it holds it, so no wrap reaches a bridge removed before the epoch.
// Records are not ops: a bridge needs one before it can open any box.
type EpochRecord struct {
	Epoch   Epoch
	Context []Dot
	Wraps   []Wrap
	Signer  devicekey.ID
	Sig     []byte
}

// Body is the record as signed: every field but the signature.
func (r EpochRecord) Body() []byte {
	b := appendEpoch([]byte(recordFormat), r.Epoch)
	b = binary.AppendUvarint(b, uint64(len(r.Context)))
	for _, d := range r.Context {
		b = append(b, d.Origin[:]...)
		b = binary.AppendUvarint(b, d.Seq)
	}
	b = binary.AppendUvarint(b, uint64(len(r.Wraps)))
	for _, w := range r.Wraps {
		b = appendBytes(append(b, w.To[:]...), w.Box)
	}
	return append(b, r.Signer[:]...)
}

// Hash names a record: its body and signature.
func (r EpochRecord) Hash() [sha256.Size]byte {
	return sha256.Sum256(appendBytes(r.Body(), r.Sig))
}

// Sign signs the record with key, its signer's device key.
func (r *EpochRecord) Sign(key crypto.Signer) error {
	digest := sha256.Sum256(r.Body())
	sig, err := key.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return fmt.Errorf("crdt: sign an epoch record: %w", err)
	}
	r.Sig = sig
	return nil
}

// VerifyRecord checks r's signature against key, its signer's (PKIX).
func VerifyRecord(key []byte, r EpochRecord) error {
	return verify(key, sha256.Sum256(r.Body()), r.Sig)
}

// errNotARecord is an epoch record whose body does not read.
var errNotARecord = errors.New("crdt: an epoch record's body does not read")

// ParseEpochRecord reads a record from its body, as Body writes it, and its signature.
func ParseEpochRecord(body, sig []byte) (EpochRecord, error) {
	rest, ok := bytes.CutPrefix(body, []byte(recordFormat))
	if !ok {
		return EpochRecord{}, errNotARecord
	}
	r := bodyReader{rest: rest}
	rec := EpochRecord{Epoch: r.epoch(), Sig: sig}
	for n := r.count(uint64(idLen) + 1); n > 0; n-- {
		rec.Context = append(rec.Context, Dot{Origin: devicekey.ID(r.take(idLen)), Seq: r.uvarint()})
	}
	for n := r.count(uint64(idLen) + 1); n > 0; n-- {
		to := devicekey.ID(r.take(idLen))
		rec.Wraps = append(rec.Wraps, Wrap{To: to, Box: r.field()})
	}
	rec.Signer = devicekey.ID(r.take(idLen))
	if r.bad || len(r.rest) > 0 {
		return EpochRecord{}, errNotARecord
	}
	return rec, nil
}

// WrapKey wraps key, epoch's content key, for the bridge to, whose sealing key is
// sealing (X25519, its roster entry's).
func WrapKey(epoch Epoch, key []byte, to devicekey.ID, sealing []byte) (Wrap, error) {
	pub, err := SealingKey(sealing)
	if err != nil {
		return Wrap{}, err
	}
	pk, err := hpke.NewDHKEMPublicKey(pub)
	if err != nil {
		return Wrap{}, fmt.Errorf("crdt: wrap a content key: %w", err)
	}
	box, err := hpke.Seal(pk, hpke.HKDFSHA256(), hpke.AES256GCM(), wrapInfo(epoch, to), key)
	if err != nil {
		return Wrap{}, fmt.Errorf("crdt: wrap a content key: %w", err)
	}
	return Wrap{To: to, Box: box}, nil
}

// Unwrap opens w, a wrap of epoch's content key, with priv, the sealing key of the
// bridge it is for.
func Unwrap(epoch Epoch, w Wrap, priv *ecdh.PrivateKey) ([]byte, error) {
	k, err := hpke.NewDHKEMPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("crdt: open a wrap: %w", err)
	}
	key, err := hpke.Open(k, hpke.HKDFSHA256(), hpke.AES256GCM(), wrapInfo(epoch, w.To), w.Box)
	if err != nil || len(key) != KeyLen {
		return nil, fmt.Errorf("crdt: a wrap of epoch %s does not open", epoch)
	}
	return key, nil
}

func wrapInfo(epoch Epoch, to devicekey.ID) []byte {
	return append(appendEpoch([]byte("kith mesh content key\n"), epoch), to[:]...)
}

// SealingKey reads a roster entry's sealing key: X25519.
func SealingKey(raw []byte) (*ecdh.PublicKey, error) {
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("crdt: a sealing key: %w", err)
	}
	return pub, nil
}

// SignerOf is the member that signed op as of op's own context (the roster it is judged
// by), or the new key of the bridge's rotation op writes: one whose entry carries a
// sealing key wrote it sealed, so an op of its taken without its envelope was stripped
// of it on the way.
func (s State) SignerOf(op Op) Member {
	writes, _ := readWrites(op) // none when they do not read: the roster's member, if any
	m, _ := s.signerOf(op, writes)
	return m
}

// Since reports whether ctx (an epoch's context) covers every removal of a bridge the
// state's roster holds: an epoch made before one is known to the bridge removed, and
// nothing is sealed under it once the removal is known. A rotation's is none: the
// replica holding the epoch's keys goes on under its new key.
func (s State) Since(ctx []Dot) bool {
	op := Op{Context: ctx}
	for i := range s.RosterOps {
		o := &s.RosterOps[i]
		for _, w := range o.Writes {
			if _, m, err := readRosterWrite(w); err == nil && m.Removed && m.RotatedTo == "" && m.Role == RoleBridge &&
				!covers(op, Dot{Origin: o.Origin, Seq: o.Seq}) {
				return false
			}
		}
	}
	return true
}
