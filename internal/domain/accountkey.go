package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

// accountKeyBytes is how much of the digest names an account.
const accountKeyBytes = 8

// AccountKey is the short, stable name a user's own files and sockets are addressed by:
// their cache, their crypto store, their scheduled-message queue, their daemon's
// socket.
func AccountKey(user string) string {
	sum := sha256.Sum256([]byte(user))
	return hex.EncodeToString(sum[:accountKeyBytes])
}
