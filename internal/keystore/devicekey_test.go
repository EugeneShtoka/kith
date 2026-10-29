package keystore

import (
	"errors"
	"os"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func deviceStoreIn(t *testing.T) Store {
	t.Helper()
	return DeviceStore(domain.Storage{Instance: "test", StateDir: t.TempDir(), KeyringService: "kith"}, "device")
}

func TestADeviceKeyIsMadeOnceAndKept(t *testing.T) {
	keyring.MockInit()
	st := deviceStoreIn(t)
	first, err := LoadOrCreateDeviceKey(st, false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreateDeviceKey(st, false)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Equal(first) {
		t.Fatal("the second load made another key")
	}
	if _, err := os.Stat(st.File); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a keyring key left a file: %v", err)
	}
	// Each name its own key.
	client, cerr := LoadOrCreateDeviceKey(DeviceStore(domain.Storage{Instance: "test", KeyringService: "kith"}, "client-device"), false)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if client.Equal(first) {
		t.Fatal("kithd's key and the client's are one")
	}
}

func TestADeviceKeyWithoutAKeyringNeedsTheOptIn(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret store"))
	st := deviceStoreIn(t)
	if _, err := LoadOrCreateDeviceKey(st, false); !errors.Is(err, ErrNoSecretStore) {
		t.Fatalf("without allow_token_file: %v, want ErrNoSecretStore", err)
	}
	first, err := LoadOrCreateDeviceKey(st, true)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(st.File)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the key's file: %v, %v; want 0600", info, err)
	}
	again, err := LoadOrCreateDeviceKey(st, true)
	if err != nil || !again.Equal(first) {
		t.Fatalf("the file's key read back as another: %v", err)
	}
	if err = os.Chmod(st.File, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadOrCreateDeviceKey(st, true); err == nil {
		t.Fatal("a world-readable key file was taken")
	}
	if err = os.Chmod(st.File, 0o600); err != nil {
		t.Fatal(err)
	}

	// The keyring answering later takes the file's key, so the ID stays.
	keyring.MockInit()
	moved, err := LoadOrCreateDeviceKey(st, true)
	if err != nil || !moved.Equal(first) {
		t.Fatalf("the keyring took another key than the file's: %v", err)
	}
	if _, err := os.Stat(st.File); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file stayed once the keyring held its key: %v", err)
	}
}

func TestACorruptDeviceKeyIsKeptAndReported(t *testing.T) {
	keyring.MockInit()
	st := deviceStoreIn(t)
	if err := keyring.Set(st.Service, st.Account, "garbage"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateDeviceKey(st, false); !errors.Is(err, ErrCorruptDeviceKey) {
		t.Fatalf("err = %v, want ErrCorruptDeviceKey", err)
	}
	if kept, _ := keyring.Get(st.Service, st.Account); kept != "garbage" {
		t.Fatalf("the corrupt key was replaced by %q", kept)
	}
}
