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
	return max(Clock(uint64(now.UnixMilli())<<counterBits), c) + 1 // #nosec G115 -- a time after 1970
}

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

// digest is what the signature covers and the version vector keeps: the body's hash.
func (o Op) digest() [sha256.Size]byte { return sha256.Sum256(o.Body()) }

// Sign signs the op with key, the origin's device key.
func (o *Op) Sign(key crypto.Signer) error {
	digest := o.digest()
	sig, err := key.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return fmt.Errorf("crdt: sign an op: %w", err)
	}
	o.Sig = sig
	return nil
}
