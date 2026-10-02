package session

import (
	"bytes"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An install from before keeps its keyring entries; a new instance of the same Matrix
// account has its own, and never reads the old ones: a second kith must not take
// over the first one's Matrix device, nor its encryption key.
func TestANewInstanceNeverTakesAnothersSession(t *testing.T) {
	keyring.MockInit()
	const user = "@ada:x"
	legacy := StoreFor(domain.Storage{Instance: domain.AccountKey(user), KeyringService: "kith", StateDir: t.TempDir()}, user)
	if legacy.Account != user || legacy.Service != "kith" {
		t.Fatalf("an install from before = %+v, want its old entry (kith, %s)", legacy, user)
	}
	if err := Save(legacy, domain.Session{UserID: user, AccessToken: "real"}, false); err != nil {
		t.Fatal(err)
	}
	realKey, err := LoadOrCreatePickleKey(legacy)
	if err != nil {
		t.Fatal(err)
	}
	sandbox := StoreFor(domain.Storage{Instance: "test", KeyringService: "kith", StateDir: t.TempDir()}, user)
	if _, found, err := Load(sandbox, false); err != nil || found {
		t.Errorf("a new instance found a session (%v, %v): it would share the real device", found, err)
	}
	if key, err := LoadOrCreatePickleKey(sandbox); err != nil || bytes.Equal(key, realKey) {
		t.Errorf("a new instance got the real encryption key (%v)", err)
	}
	if got, _, _ := Load(legacy, false); got.AccessToken != "real" {
		t.Errorf("the real session became %+v", got)
	}
}

// An API key belongs to the person: one kept under the default service is found from
// an instance with its own.
func TestASecretIsFoundUnderTheDefaultService(t *testing.T) {
	keyring.MockInit()
	if err := StoreSecret("kith", "groq", "k1"); err != nil {
		t.Fatal(err)
	}
	if got, err := Secret("kith-test", "groq"); err != nil || got != "k1" {
		t.Errorf("Secret under another service = (%q, %v), want the default service's", got, err)
	}
}
