package matrix

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/id"
)

// The SSSS backup secret decodes to a 32-byte key whether or not the base64 is padded.
func TestDecodeBackupSecret(t *testing.T) {
	t.Parallel()

	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	raw := key.Bytes()

	tests := map[string]string{
		"unpadded":            base64.RawStdEncoding.EncodeToString(raw),
		"padded":              base64.StdEncoding.EncodeToString(raw),
		"trailing whitespace": base64.StdEncoding.EncodeToString(raw) + "\n",
	}
	for name, secret := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeBackupSecret(secret)
			if err != nil {
				t.Fatalf("decodeBackupSecret(%q): %v", secret, err)
			}
			if !bytes.Equal(got, raw) {
				t.Errorf("decoded = %x, want %x", got, raw)
			}
		})
	}
}

func TestDecodeBackupSecretRejectsGarbage(t *testing.T) {
	t.Parallel()
	if _, err := decodeBackupSecret("not valid base64 !!!"); err == nil {
		t.Error("expected an error decoding garbage, got nil")
	}
}

// keptBackupKey is the backup key the crypto store keeps, "" for none.
func keptBackupKey(t *testing.T, mach *crypto.OlmMachine) string {
	t.Helper()
	secret, err := mach.CryptoStore.GetSecret(t.Context(), id.SecretMegolmBackupV1)
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if secret == "" {
		return ""
	}
	key, ok := parseBackupKey(secret)
	if !ok {
		t.Fatalf("the kept backup key %q does not parse", secret)
	}
	return base64.StdEncoding.EncodeToString(key.Bytes())
}

// A restore keeps the key that opened the latest backup version, in place of whatever
// was kept before. A key the version is not encrypted to (trusted by signature only)
// and a restore that fails leave the kept key as it was. Every pairing of what is
// kept, which key the version is encrypted to, and whether the import succeeds.
func TestImportKeyBackupKeepsTheKeyThatOpensIt(t *testing.T) {
	t.Parallel()

	user := id.UserID("@me:x")
	master := mustSigning(t)
	typed, other := newBackupKey(t), newBackupKey(t)
	encoded := func(k *backup.MegolmBackupKey) string { return base64.StdEncoding.EncodeToString(k.Bytes()) }

	kept := map[string]*backup.MegolmBackupKey{"nothing": nil, "another key": other, "the same key": typed}
	versionTo := map[string]*backup.MegolmBackupKey{"the typed key": typed, "another key, signed": other}
	for keptName, before := range kept {
		for versionName, versionKey := range versionTo {
			for _, importFails := range []bool{false, true} {
				name := fmt.Sprintf("kept %s, version to %s, import fails %v", keptName, versionName, importFails)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					authData, err := signedAuthData(user, master, versionKey)
					if err != nil {
						t.Fatalf("signedAuthData: %v", err)
					}
					mach := restoreServer(t, user, master.PublicKey(), authData, importFails)
					want := ""
					if before != nil {
						want = encoded(before)
						if perr := mach.CryptoStore.PutSecret(t.Context(), id.SecretMegolmBackupV1, want); perr != nil {
							t.Fatalf("put secret: %v", perr)
						}
					}

					_, err = importKeyBackup(t.Context(), mach, typed)
					if importFails != (err != nil) {
						t.Fatalf("importKeyBackup error = %v, want failure %v", err, importFails)
					}
					if !importFails && versionKey == typed {
						want = encoded(typed)
					}
					if got := keptBackupKey(t, mach); got != want {
						t.Errorf("kept backup key = %q, want %q", got, want)
					}
				})
			}
		}
	}
}

// restoreServer serves one signed backup version, and its keys or a failure.
func restoreServer(t *testing.T, user id.UserID, master id.Ed25519, authData backup.MegolmAuthData, keysFail bool) *crypto.OlmMachine {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/room_keys/version"):
			_ = json.NewEncoder(w).Encode(mautrix.RespRoomKeysVersion[backup.MegolmAuthData]{
				Algorithm: id.KeyBackupAlgorithmMegolmBackupV1, AuthData: authData, Version: "7", ETag: "1",
			})
		case strings.HasSuffix(r.URL.Path, "/room_keys/keys") && !keysFail:
			_, _ = io.WriteString(w, `{"rooms":{}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"errcode":"M_UNKNOWN","error":"down"}`)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := mautrix.NewClient(srv.URL, user, "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	store := crypto.NewMemoryStore(nil)
	if perr := store.PutCrossSigningKey(t.Context(), user, id.XSUsageMaster, master); perr != nil {
		t.Fatalf("put cross-signing key: %v", perr)
	}
	return crypto.NewOlmMachine(client, nil, store, nil)
}
