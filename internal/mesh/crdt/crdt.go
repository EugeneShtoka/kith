// Package crdt is the mesh's data model: the hybrid logical clock, dots, version
// vectors, signed ops and the roster they keep (planning/design-mesh.md). Pure: the
// store that keeps a replica's ops is internal/mesh's.
package crdt

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// Clock is a hybrid logical clock: Unix milliseconds in the high 48 bits, a counter in
// the low 16. Ops are ordered by (clock, origin), so no two tie.
type Clock uint64

// counterBits is the width of the clock's counter.
const counterBits = 16

// Next is the clock of a write made at now after one at c: past both (Kulkarni et al.).
// The time is passed in, so a test drives it.
func (c Clock) Next(now time.Time) Clock {
	return max(At(now), c) + 1
}

// At is the first clock of the millisecond t falls in.
func At(t time.Time) Clock {
	return Clock(uint64(t.UnixMilli()) << counterBits) // #nosec G115 -- a time after 1970
}

// Time is the wall-clock time the clock carries.
func (c Clock) Time() time.Time { return time.UnixMilli(int64(c >> counterBits)) } // #nosec G115 -- 48 bits

// Dot names one op: its origin replica (a device ID) and that replica's number for it,
// 1, 2, 3… in the order it wrote them.
type Dot struct {
	Origin devicekey.ID
	Seq    uint64
}

// Head is the last op a version vector holds of an origin: its seq, every one before
// it held too, and its hash, so two replicas holding different ops under one dot see it.
type Head struct {
	Seq  uint64
	Hash [sha256.Size]byte
}

// Vector is a version vector: what a replica holds of each origin.
type Vector map[devicekey.ID]Head

// Covers reports whether v holds the op d names.
func (v Vector) Covers(d Dot) bool { return v[d.Origin].Seq >= d.Seq }

// Context is the vector as dots, by origin: what an op written now has seen.
func (v Vector) Context() []Dot {
	out := make([]Dot, 0, len(v))
	for origin, h := range v {
		out = append(out, Dot{Origin: origin, Seq: h.Seq})
	}
	slices.SortFunc(out, func(a, b Dot) int { return bytes.Compare(a.Origin[:], b.Origin[:]) })
	return out
}

// Write is one key an op writes, and its value.
type Write struct {
	Key   string
	Value []byte
}

// Op is one transaction of one replica: its writes, applied together or not at all,
// signed by the origin's device key.
type Op struct {
	Origin  devicekey.ID
	Seq     uint64
	Clock   Clock
	Context []Dot // the origin's version vector when it wrote
	Writes  []Write
	Sig     []byte // the origin's signature over Body's digest
}

// opFormat starts every op's body: what it is, and which encoding.
const opFormat = "kith mesh op 1\n"

// MaxOpSize bounds an op's body: a write larger is refused at its source, and by every
// replica it reaches.
const MaxOpSize = 1 << 20

// Body is the op as signed: every field but the signature, in one encoding, so every
// replica checks the same bytes.
func (o Op) Body() []byte {
	b := append([]byte(opFormat), o.Origin[:]...)
	b = binary.AppendUvarint(b, o.Seq)
	b = binary.AppendUvarint(b, uint64(o.Clock))
	b = binary.AppendUvarint(b, uint64(len(o.Context)))
	for _, d := range o.Context {
		b = append(b, d.Origin[:]...)
		b = binary.AppendUvarint(b, d.Seq)
	}
	b = binary.AppendUvarint(b, uint64(len(o.Writes)))
	for _, w := range o.Writes {
		b = appendBytes(b, []byte(w.Key))
		b = appendBytes(b, w.Value)
	}
	return b
}

func appendBytes(b, field []byte) []byte {
	return append(binary.AppendUvarint(b, uint64(len(field))), field...)
}

// Digest is what the signature covers and the version vector keeps: the body's hash.
func (o Op) Digest() [sha256.Size]byte { return sha256.Sum256(o.Body()) }

// Sign signs the op with key, the origin's device key.
func (o *Op) Sign(key crypto.Signer) error {
	digest := o.Digest()
	sig, err := key.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return fmt.Errorf("crdt: sign an op: %w", err)
	}
	o.Sig = sig
	return nil
}

// errNotABody is a kept or relayed op whose body does not read.
var errNotABody = errors.New("crdt: an op's body does not read")

// idLen is a device ID's length in a body.
const idLen = len(devicekey.ID{})

// ParseOp reads an op from its body, as Body writes it, and its signature: how a store
// keeps an op, and how a peer's arrives.
func ParseOp(body, sig []byte) (Op, error) {
	rest, ok := bytes.CutPrefix(body, []byte(opFormat))
	if !ok || len(rest) < idLen {
		return Op{}, errNotABody
	}
	op := Op{Origin: devicekey.ID(rest[:idLen]), Sig: sig}
	r := bodyReader{rest: rest[idLen:]}
	op.Seq = r.uvarint()
	op.Clock = Clock(r.uvarint())
	for n := r.count(uint64(idLen) + 1); n > 0; n-- {
		op.Context = append(op.Context, Dot{Origin: devicekey.ID(r.take(idLen)), Seq: r.uvarint()})
	}
	for n := r.count(2); n > 0; n-- {
		key := r.field()
		op.Writes = append(op.Writes, Write{Key: string(key), Value: r.field()})
	}
	if r.bad || len(r.rest) > 0 {
		return Op{}, errNotABody
	}
	return op, nil
}

// bodyReader reads a body field by field; one that does not read sets bad, and every
// read after it is zero.
type bodyReader struct {
	rest []byte
	bad  bool
}

func (r *bodyReader) fail() {
	r.bad, r.rest = true, nil
}

func (r *bodyReader) uvarint() uint64 {
	v, n := binary.Uvarint(r.rest)
	if n <= 0 {
		r.fail()
		return 0
	}
	r.rest = r.rest[n:]
	return v
}

// count reads how many items follow, each at least least bytes long: more than the
// rest of the body holds does not read.
func (r *bodyReader) count(least uint64) int {
	n := r.uvarint()
	if n > uint64(len(r.rest))/least {
		r.fail()
		return 0
	}
	return int(n) // #nosec G115 -- bounded by the body's length
}

// take reads the next n bytes, a copy.
func (r *bodyReader) take(n int) []byte {
	if n > len(r.rest) {
		r.fail()
		return make([]byte, n)
	}
	out := bytes.Clone(r.rest[:n])
	r.rest = r.rest[n:]
	return out
}

// field reads a length-prefixed field.
func (r *bodyReader) field() []byte {
	n := r.uvarint()
	if n > uint64(len(r.rest)) {
		r.fail()
		return nil
	}
	return r.take(int(n)) // #nosec G115 -- bounded by the body's length
}
