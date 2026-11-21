package crdt

import (
	"crypto/ecdh"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// The mesh's recovery key (planning/design-mailbox.md, Recovery) is 32 random bytes the
// person keeps. An X25519 key is derived from it, and its public half is the mesh's
// recovery key: every epoch is wrapped for it as for a bridge, so with the bytes a new
// kithd opens what a mailbox holds after every bridge is lost.

// RecoveryKeyName is the key the public half is written to. Any bridge may write it;
// concurrent writes all stand, and every one of them is wrapped for.
const RecoveryKeyName = "recovery"

// errNotASecret is a recovery key of the wrong length.
var errNotASecret = errors.New("crdt: a recovery key is 32 bytes")

// RecoveryPrivate is the X25519 key derived from secret, a mesh's recovery key.
func RecoveryPrivate(secret []byte) (*ecdh.PrivateKey, error) {
	if len(secret) != KeyLen {
		return nil, errNotASecret
	}
	d := sha256.Sum256(append([]byte("kith mesh recovery key\n"), secret...))
	key, err := ecdh.X25519().NewPrivateKey(d[:])
	if err != nil {
		return nil, fmt.Errorf("crdt: a recovery key: %w", err)
	}
	return key, nil
}

// RecoveryID names a recovery key's public half where a wrap names its bridge.
func RecoveryID(pub []byte) devicekey.ID {
	return devicekey.ID(sha256.Sum256(append([]byte(devicekey.MeshRecovery), pub...)))
}

// RecoveryKeys is each public half of a recovery key the state holds.
func (s State) RecoveryKeys() [][]byte {
	var out [][]byte
	for _, e := range s.Entries[RecoveryKeyName] {
		out = append(out, e.Value)
	}
	return out
}

// readRecoveryWrite checks a write to the recovery key: an X25519 public key.
func readRecoveryWrite(w Write) (bool, error) {
	if w.Key != RecoveryKeyName {
		return false, nil
	}
	if _, err := SealingKey(w.Value); err != nil {
		return true, fmt.Errorf("%w: %s: %w", ErrBadWrite, w.Key, err)
	}
	return true, nil
}
