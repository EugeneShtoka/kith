package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/session"
)

// The top-level account becomes a [[matrix.account]] the config loads as that one
// account; a config with none, or with profiles, is left alone.
func TestTheAccountMovesIntoMatrixAccount(t *testing.T) {
	const user = "@me:example.org"
	in := "# mine\nhomeserver = \"https://example.org\" # the server\nuser = \"@me:example.org\"\nallow_token_file = true\n\n[display]\nfps = 30\n"
	out, n, err := moveAccount(user)(ids{}, []byte(in))
	if err != nil || n != 2 {
		t.Fatalf("moveAccount = (%d, %v), want both keys moved", n, err)
	}
	cfg, err := config.Decode(string(out))
	if err != nil {
		t.Fatalf("the moved config does not load: %v\n%s", err, out)
	}
	want := config.MatrixAccount{Name: "me", Homeserver: "https://example.org", User: user}
	if len(cfg.Matrix.Accounts) != 1 || cfg.Matrix.Accounts[0] != want || !cfg.AllowTokenFile || cfg.Display.FPS != 30 {
		t.Errorf("moved config = %+v, %+v; want the one account and the rest kept\n%s", cfg.Matrix, cfg.Display, out)
	}
	if !strings.HasPrefix(string(out), "# mine\n") {
		t.Errorf("the comment moved:\n%s", out)
	}
	for _, alone := range []string{"[display]\nfps = 30\n", "[[profile]]\nname = \"w\"\nhomeserver = \"h\"\nuser = \"@w:x\"\n"} {
		if got, n, err := moveAccount(user)(ids{}, []byte(alone)); err != nil || n != 0 || string(got) != alone {
			t.Errorf("moveAccount(%q) = (%q, %d, %v), want it left alone", alone, got, n, err)
		}
	}
	if _, _, err := moveAccount(user)(ids{}, []byte("user = \"@me:x\"\n")); err == nil {
		t.Error("a user without a homeserver was moved")
	}
}

// The crypto store and session file take the names kithd opens, and the keyring
// entries are copied to the ones it reads, the old ones kept; an install named after
// its account (before instances) reads its entries from under the MXID.
func TestTheAccountsFilesAndKeysMoveWhereKithdLooks(t *testing.T) {
	const user = "@me:example.org"
	key := domain.AccountKey(user)
	for _, instance := range []string{"base", key} {
		keyring.MockInit()
		dir := t.TempDir()
		storage := domain.Storage{Instance: instance, DataDir: dir, StateDir: dir, KeyringService: "kith"}
		for _, f := range []string{storage.CryptoPath(), storage.CryptoPath() + "-wal", storage.SessionFile()} {
			if err := os.WriteFile(f, []byte(filepath.Base(f)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		old := instance
		if instance == key {
			old = user
		}
		saved := domain.Session{UserID: user, AccessToken: "token"}
		if err := session.Save(session.Store{Service: "kith", Account: old}, saved, false); err != nil {
			t.Fatal(err)
		}
		if err := keyring.Set("kith", old+"|pickle", "pickle"); err != nil {
			t.Fatal(err)
		}

		if n, err := migrateStores(storage, user, true); err != nil || n != 3 {
			t.Fatalf("%s: a dry run = (%d, %v), want three files reported", instance, n, err)
		}
		if _, err := os.Stat(storage.MatrixCryptoPath(key)); err == nil {
			t.Fatalf("%s: a dry run moved a file", instance)
		}
		if n, err := migrateStores(storage, user, false); err != nil || n != 3 {
			t.Fatalf("%s: migrateStores = (%d, %v)", instance, n, err)
		}
		if n, err := migrateKeyring(storage, user, false); err != nil || n != 2 {
			t.Fatalf("%s: migrateKeyring = (%d, %v)", instance, n, err)
		}
		for _, f := range []string{storage.MatrixCryptoPath(key), storage.MatrixCryptoPath(key) + "-wal", storage.MatrixSessionFile(key)} {
			if _, err := os.Stat(f); err != nil {
				t.Errorf("%s: %s is not where kithd opens it: %v", instance, filepath.Base(f), err)
			}
		}
		store := session.MatrixStore(storage, key)
		if got, found, err := session.Load(store, false); err != nil || !found || got != saved {
			t.Errorf("%s: the session where kithd reads it = (%+v, %t, %v)", instance, got, found, err)
		}
		if got, err := keyring.Get("kith", store.Account+"|pickle"); err != nil || got != "pickle" {
			t.Errorf("%s: the pickle key where kithd reads it = (%q, %v)", instance, got, err)
		}
		if _, err := keyring.Get("kith", old); err != nil {
			t.Errorf("%s: the old entry went: %v", instance, err)
		}
		if _, err := migrateKeyring(storage, user, false); err == nil {
			t.Errorf("%s: a second run overwrote the copied entries", instance)
		}
	}
}
