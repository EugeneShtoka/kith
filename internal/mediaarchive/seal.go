package mediaarchive

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// A sealed copy ([storage] encrypt_media) is AES-256-GCM under a key HKDF makes of the
// cache's key, written as the nonce then the ciphertext, its name (the hash, without
// SealedExt) the additional data, so one copy cannot pass for another's.

// SealedExt ends a sealed copy's name; the plain copy's ends in its file type's.
const SealedExt = ".sealed"

// errUnsealed is a sealed copy that does not open under the key: another machine's, or
// damaged.
var errUnsealed = errors.New("mediaarchive: a sealed copy does not open under this key")

// Sealer seals and opens copies under one key.
type Sealer struct{ aead cipher.AEAD }

// NewSealer is the sealer under the key cacheKey makes.
func NewSealer(cacheKey []byte) (*Sealer, error) {
	key, err := hkdf.Key(sha256.New, cacheKey, nil, "kith media archive v1", 32)
	if err != nil {
		return nil, fmt.Errorf("mediaarchive: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("mediaarchive: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("mediaarchive: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal is data sealed as the copy named file (a base name ending in SealedExt).
func (s *Sealer) Seal(file string, data []byte) []byte {
	return s.aead.Seal(nil, nil, data, sealedAs(file))
}

// Open is the bytes of the sealed copy named file.
func (s *Sealer) Open(file string, sealed []byte) ([]byte, error) {
	data, err := s.aead.Open(nil, nil, sealed, sealedAs(file))
	if err != nil {
		return nil, errUnsealed
	}
	return data, nil
}

// sealedAs is what a sealed copy is bound to: its name without SealedExt.
func sealedAs(file string) []byte {
	return []byte(strings.TrimSuffix(filepath.Base(file), SealedExt))
}
