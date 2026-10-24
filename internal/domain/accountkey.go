package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

// accountKeyBytes is how much of the digest names an account.
const accountKeyBytes = 8

// AccountKey is the short stable name of a user's files and sockets (cache, crypto
// store, scheduled queue, daemon socket).
func AccountKey(user string) string {
	sum := sha256.Sum256([]byte(user))
	return hex.EncodeToString(sum[:accountKeyBytes])
}

// isAccountKey reports whether s has the shape AccountKey returns: lowercase hex.
func isAccountKey(s string) bool {
	if len(s) != 2*accountKeyBytes {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
