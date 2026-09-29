package matrix

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/olm"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/api"
)

// accountState is what the fake homeserver says about secret storage and backup.
type accountState struct {
	defaultKey string // empty → 404, as an account with no secret storage answers
	version    string // empty → 404, as an account with no backup answers
	// broken makes the default-key read fail with neither answer.
	broken bool
}

func (a accountState) machine(t *testing.T) *crypto.OlmMachine {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/account_data/m.secret_storage.default_key"):
			if a.broken {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"errcode":"M_UNKNOWN","error":"database is on fire"}`)
				return
			}
			if a.defaultKey == "" {
				notFound(w)
				return
			}
			_, _ = io.WriteString(w, `{"key":"`+a.defaultKey+`"}`)
		case strings.HasSuffix(r.URL.Path, "/room_keys/version"):
			if a.version == "" {
				notFound(w)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"algorithm": id.KeyBackupAlgorithmMegolmBackupV1,
				"version":   a.version,
				"count":     0,
				"etag":      "1",
				"auth_data": map[string]any{"public_key": "irrelevant"},
			})
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			notFound(w)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return crypto.NewOlmMachine(client, nil, crypto.NewMemoryStore(nil), nil)
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"errcode":"M_NOT_FOUND","error":"Not found"}`)
}

// Bootstrapping replaces the account's identity, so anything other than "definitely
// nothing set up" (including an unreadable answer) must refuse.
func TestRefuseIfSetUp(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		state accountState
		want  error
	}{
		"nothing set up":        {accountState{}, nil},
		"secret storage exists": {accountState{defaultKey: "abc"}, api.ErrKeyBackupExists},
		"backup version exists": {accountState{version: "7"}, api.ErrKeyBackupExists},
		"both exist":            {accountState{defaultKey: "abc", version: "7"}, api.ErrKeyBackupExists},
		"homeserver cannot say": {accountState{broken: true}, errUnreadable},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := refuseIfSetUp(t.Context(), tc.state.machine(t))
			switch {
			case errors.Is(tc.want, errUnreadable):
				if err == nil || errors.Is(err, api.ErrKeyBackupExists) {
					t.Errorf("error = %v, want a plain failure — an unreadable answer must not "+
						"be treated as either 'set up' or 'empty'", err)
				}
			case tc.want == nil && err != nil:
				t.Errorf("error = %v, want nil", err)
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// errUnreadable marks a row expecting "some error, but not a refusal".
var errUnreadable = errors.New("an error that is not a refusal")

// Interop: the auth-data signature written here must pass mautrix's verifier.
func TestSignedAuthDataVerifies(t *testing.T) {
	t.Parallel()

	master, err := olm.NewPKSigning()
	if err != nil {
		t.Fatalf("new signing key: %v", err)
	}
	backupKey := newBackupKey(t)
	user := id.UserID("@me:x")

	authData, err := signedAuthData(user, master, backupKey)
	if err != nil {
		t.Fatalf("signedAuthData: %v", err)
	}

	pub, err := backupPublicKey(authData)
	if err != nil {
		t.Fatalf("backupPublicKey: %v", err)
	}
	if !pub.Equal(backupKey.PublicKey()) {
		t.Error("auth_data advertises a different public key than the backup's")
	}

	// Checked the way GetAndVerifyLatestKeyBackupVersion checks it.
	srv := &versionServer{authData: authData, version: "7"}
	mach := srv.machine(t, user, master.PublicKey())
	version, got, err := trustedBackup(t.Context(), mach)
	if err != nil {
		t.Fatalf("trustedBackup rejected a version this client signed: %v", err)
	}
	if version != "7" {
		t.Errorf("version = %q, want %q", version, "7")
	}
	if !got.Equal(backupKey.PublicKey()) {
		t.Error("trustedBackup returned a different public key than the version advertises")
	}
}

// versionServer serves one backup version.
type versionServer struct {
	authData backup.MegolmAuthData
	version  string
}

func (s *versionServer) machine(t *testing.T, user id.UserID, master id.Ed25519) *crypto.OlmMachine {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mautrix.RespRoomKeysVersion[backup.MegolmAuthData]{
			Algorithm: id.KeyBackupAlgorithmMegolmBackupV1,
			AuthData:  s.authData,
			Version:   id.KeyBackupVersion(s.version),
			ETag:      "1",
		})
	}))
	t.Cleanup(srv.Close)

	client, err := mautrix.NewClient(srv.URL, user, "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	store := crypto.NewMemoryStore(nil)
	// On a real device the master key is cached from /keys/query.
	if perr := store.PutCrossSigningKey(t.Context(), user, id.XSUsageMaster, master); perr != nil {
		t.Fatalf("put cross-signing key: %v", perr)
	}
	return crypto.NewOlmMachine(client, nil, store, nil)
}

// A version nothing vouches for must not be uploaded to (attacker-chosen key).
func TestTrustedBackupRefusesAnUnsignedVersion(t *testing.T) {
	t.Parallel()

	backupKey := newBackupKey(t)
	unsigned, err := signedAuthData("@me:x", mustSigning(t), backupKey)
	if err != nil {
		t.Fatalf("signedAuthData: %v", err)
	}
	unsigned.Signatures = nil

	srv := &versionServer{authData: unsigned, version: "7"}
	// A master key is known, and it did not sign this.
	mach := srv.machine(t, "@me:x", mustSigning(t).PublicKey())
	if _, _, err := trustedBackup(t.Context(), mach); err == nil {
		t.Error("trustedBackup accepted a version with no signature from this account")
	}
}

func mustSigning(t *testing.T) olm.PKSigning {
	t.Helper()
	key, err := olm.NewPKSigning()
	if err != nil {
		t.Fatalf("new signing key: %v", err)
	}
	return key
}
