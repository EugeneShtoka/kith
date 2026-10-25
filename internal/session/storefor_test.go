package session

import (
	"bytes"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Each Matrix account of each instance has its own session and encryption key: a
// second instance, or a second account, never reads the first one's.
func TestEachAccountOfEachInstanceKeepsItsOwnSession(t *testing.T) {
	keyring.MockInit()
	real := MatrixStore(domain.Storage{Instance: "main", KeyringService: "kith", StateDir: t.TempDir()}, domain.AccountKey("@ada:x"))
	if err := Save(real, domain.Session{UserID: "@ada:x", AccessToken: "real"}, false); err != nil {
		t.Fatal(err)
	}
	realKey, err := LoadOrCreatePickleKey(real)
	if err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]Store{
		"another instance": MatrixStore(domain.Storage{Instance: "test", KeyringService: "kith", StateDir: t.TempDir()}, domain.AccountKey("@ada:x")),
		"another account":  MatrixStore(domain.Storage{Instance: "main", KeyringService: "kith", StateDir: t.TempDir()}, domain.AccountKey("@bob:x")),
	} {
		if _, found, err := Load(other, false); err != nil || found {
			t.Errorf("%s found a session (%v, %v): it would share the real device", name, found, err)
		}
		if key, err := LoadOrCreatePickleKey(other); err != nil || bytes.Equal(key, realKey) {
			t.Errorf("%s got the real encryption key (%v)", name, err)
		}
	}
	if got, _, _ := Load(real, false); got.AccessToken != "real" {
		t.Errorf("the real session became %+v", got)
	}
}

func TestASecretIsFoundUnderTheDefaultService(t *testing.T) {
	keyring.MockInit()
	if err := StoreSecret("kith", "groq", "k1"); err != nil {
		t.Fatal(err)
	}
	if got, err := Secret("kith-test", "groq"); err != nil || got != "k1" {
		t.Errorf("Secret under another service = (%q, %v), want the default service's", got, err)
	}
}
