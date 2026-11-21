package devicekey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
)

// Rotation is a device's move from one key to another, signed by both: the old key
// says it hands over, the new one that its holder took it. From is the old key
// (PKIX); the new key is carried beside it.
type Rotation struct {
	From    []byte `json:"from"`
	FromSig []byte `json:"from_sig"`
	ToSig   []byte `json:"to_sig"`
}

// rotationFormat starts what a rotation's keys sign.
const rotationFormat = "kith rotation 1\n"

// rotationDigest is what both keys sign: the old key's ID and the new key (PKIX).
func rotationDigest(from ID, to []byte) [sha256.Size]byte {
	return sha256.Sum256(append(append([]byte(rotationFormat), from[:]...), to...))
}

// Rotate signs the move from old to next with both, and is the new key (PKIX).
func Rotate(old, next crypto.Signer) (Rotation, []byte, error) {
	from, err := x509.MarshalPKIXPublicKey(old.Public())
	if err != nil {
		return Rotation{}, nil, fmt.Errorf("devicekey: rotate: %w", err)
	}
	to, err := x509.MarshalPKIXPublicKey(next.Public())
	if err != nil {
		return Rotation{}, nil, fmt.Errorf("devicekey: rotate: %w", err)
	}
	digest := rotationDigest(sha256.Sum256(from), to)
	r := Rotation{From: from}
	if r.FromSig, err = old.Sign(rand.Reader, digest[:], crypto.SHA256); err != nil {
		return Rotation{}, nil, fmt.Errorf("devicekey: rotate: %w", err)
	}
	if r.ToSig, err = next.Sign(rand.Reader, digest[:], crypto.SHA256); err != nil {
		return Rotation{}, nil, fmt.Errorf("devicekey: rotate: %w", err)
	}
	return r, to, nil
}

// ErrBadRotation is a rotation that either key's signature does not verify.
var ErrBadRotation = errors.New("devicekey: the rotation is not signed by both keys")

// Verify checks that both keys signed r's move to to (PKIX), and is the old key's ID.
func (r Rotation) Verify(to []byte) (ID, error) {
	from := ID(sha256.Sum256(r.From))
	digest := rotationDigest(from, to)
	for _, k := range []struct{ key, sig []byte }{{r.From, r.FromSig}, {to, r.ToSig}} {
		parsed, err := x509.ParsePKIXPublicKey(k.key)
		pub, ok := parsed.(*ecdsa.PublicKey)
		if err != nil || !ok || !ecdsa.VerifyASN1(pub, digest[:], k.sig) {
			return ID{}, ErrBadRotation
		}
	}
	return from, nil
}
