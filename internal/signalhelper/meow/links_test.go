//go:build signal

package meow

import (
	"encoding/hex"
	"testing"
)

// A username link's key opens the username Signal keeps encrypted for it (the vector
// is libsignal's own signal_username_link_create of "ofer.42"); a wrong key opens
// nothing.
func TestAUsernameLinkOpensItsUsername(t *testing.T) {
	t.Parallel()

	key, _ := hex.DecodeString("1f918c9503e9508582b69bba780a8769608361c3250d29e48dc16e00cdf6e46b")
	ciphertext, _ := hex.DecodeString("539c6f03363914def0734edae381bcc3679ed5342edee8d7f546d0045a0a9eaa" +
		"0a8f9a01e7f213d2973ef567b8ff422dc3f5ea892af34f1587cc71f758bc69ae" +
		"b294d233780b4d3d7652ed8885a269cc4603da93ab1bdb0d7d488a102b1d235a" +
		"a08fffd9cc72c8e55fd2b14fad979ba1")
	if got, err := decryptUsername(key, ciphertext); err != nil || got != "ofer.42" {
		t.Errorf("decryptUsername = (%q, %v), want ofer.42", got, err)
	}
	key[0] ^= 1
	if got, err := decryptUsername(key, ciphertext); err == nil {
		t.Errorf("a wrong key opened %q", got)
	}
}

// A username is looked up by libsignal's hash of it (the vector is libsignal's own
// UsernamesTest).
func TestAUsernameIsLookedUpByItsHash(t *testing.T) {
	t.Parallel()

	got, err := usernameHash("he110.42")
	if want := "f63f0521eb3adfe1d936f4b626b89558483507fbdb838fc554af059111cf322e"; err != nil || hex.EncodeToString(got) != want {
		t.Errorf("usernameHash(he110.42) = (%x, %v), want %s", got, err, want)
	}
	if _, err := usernameHash("no discriminator"); err == nil {
		t.Error("a malformed username hashed")
	}
}
