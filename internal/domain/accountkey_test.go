package domain_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The key is a name on disk, so changing it renames every file a user already has.
func TestAccountKeyIsTheSpellingAlreadyOnDisk(t *testing.T) {
	t.Parallel()

	for _, user := range []string{
		"@alice:example.org", "@a:x", "", "@work:matrix.org",
		"@someone:with-a-very-long-homeserver-name.example.com",
	} {
		sum := sha256.Sum256([]byte(user))
		want := fmt.Sprintf("%x", sum[:8])
		if got := domain.AccountKey(user); got != want {
			t.Errorf("AccountKey(%q) = %q, want %q — every existing file for this account "+
				"would be abandoned and a fresh one started", user, got, want)
		}
	}
}

// Sixteen hex characters, and different accounts get different ones.
func TestAccountKeyIsStableAndDistinct(t *testing.T) {
	t.Parallel()

	a, b := domain.AccountKey("@a:x"), domain.AccountKey("@b:x")
	if a == b {
		t.Error("two accounts share a key; their caches and sockets would collide")
	}
	if len(a) != 16 {
		t.Errorf("key %q is %d characters, want 16", a, len(a))
	}
	if a != domain.AccountKey("@a:x") {
		t.Error("the key is not stable between calls")
	}
}
