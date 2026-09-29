package matrix

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/olm"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/api"
)

// newTestSession builds a real megolm session in roomID (an outbound key imported
// as inbound); the upload exports the ratchet, so a stub would prove nothing.
func newTestSession(t *testing.T, roomID id.RoomID) *crypto.InboundGroupSession {
	t.Helper()
	outbound, err := olm.NewOutboundGroupSession()
	if err != nil {
		t.Fatalf("new outbound group session: %v", err)
	}
	session, err := crypto.NewInboundGroupSession(
		id.SenderKey("sender-curve25519-key"), id.Ed25519("sender-ed25519-key"),
		roomID, outbound.Key(), 0, 0, nil, false)
	if err != nil {
		t.Fatalf("new inbound group session: %v", err)
	}
	return session
}

// backupServer fakes the key-backup endpoint, recording each PUT body.
type backupServer struct {
	puts []*mautrix.ReqKeyBackup
}

func (s *backupServer) start(t *testing.T) *mautrix.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		req := &mautrix.ReqKeyBackup{}
		if uerr := json.Unmarshal(body, req); uerr != nil {
			t.Errorf("decode request body: %v", uerr)
		}
		s.puts = append(s.puts, req)
		count := 0
		for _, room := range req.Rooms {
			count += len(room.Sessions)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mautrix.RespRoomKeysUpdate{Count: count, ETag: "1"})
	}))
	t.Cleanup(srv.Close)

	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

// newBackupKey returns a megolm backup key to encrypt for and decrypt with.
func newBackupKey(t *testing.T) *backup.MegolmBackupKey {
	t.Helper()
	key, err := backup.NewMegolmBackupKey()
	if err != nil {
		t.Fatalf("new backup key: %v", err)
	}
	return key
}

// The uploaded payload (hand-assembled, since mautrix's export() is unexported) must
// decrypt back into the session it came from.
func TestUploadRoomKeysRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	store := crypto.NewMemoryStore(nil)
	session := newTestSession(t, "!room:x")
	if err := store.PutGroupSession(ctx, session); err != nil {
		t.Fatalf("put group session: %v", err)
	}

	srv := &backupServer{}
	key := newBackupKey(t)
	uploaded, err := uploadRoomKeys(ctx, srv.start(t), store, "v1", key.PublicKey())
	if err != nil {
		t.Fatalf("uploadRoomKeys: %v", err)
	}
	if uploaded != 1 {
		t.Fatalf("uploaded = %d, want 1", uploaded)
	}
	if len(srv.puts) != 1 {
		t.Fatalf("PUTs = %d, want 1", len(srv.puts))
	}

	data, ok := srv.puts[0].Rooms["!room:x"].Sessions[session.ID()]
	if !ok {
		t.Fatalf("session %s was not in the upload", session.ID())
	}
	var encrypted backup.EncryptedSessionData[backup.MegolmSessionData]
	if uerr := json.Unmarshal(data.SessionData, &encrypted); uerr != nil {
		t.Fatalf("decode session data: %v", uerr)
	}
	decrypted, err := encrypted.Decrypt(key)
	if err != nil {
		t.Fatalf("decrypt session data: %v", err)
	}
	if decrypted.Algorithm != id.AlgorithmMegolmV1 {
		t.Errorf("algorithm = %q, want %q", decrypted.Algorithm, id.AlgorithmMegolmV1)
	}
	if decrypted.SenderClaimedKeys.Ed25519 != session.SigningKey {
		t.Errorf("sender claimed key = %q, want %q", decrypted.SenderClaimedKeys.Ed25519, session.SigningKey)
	}
	imported, err := olm.InboundGroupSessionImport([]byte(decrypted.SessionKey))
	if err != nil {
		t.Fatalf("import the uploaded session key: %v", err)
	}
	if imported.ID() != session.ID() {
		t.Errorf("restored session = %q, want %q", imported.ID(), session.ID())
	}
}

// Sessions are grouped by room and marked backed up so a second pass sends nothing.
func TestUploadRoomKeysGroupsByRoomAndMarks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	store := crypto.NewMemoryStore(nil)
	rooms := []id.RoomID{"!a:x", "!a:x", "!b:x"}
	for _, room := range rooms {
		if err := store.PutGroupSession(ctx, newTestSession(t, room)); err != nil {
			t.Fatalf("put group session: %v", err)
		}
	}

	srv := &backupServer{}
	client := srv.start(t)
	key := newBackupKey(t)

	uploaded, err := uploadRoomKeys(ctx, client, store, "v1", key.PublicKey())
	if err != nil {
		t.Fatalf("uploadRoomKeys: %v", err)
	}
	if uploaded != 3 {
		t.Fatalf("uploaded = %d, want 3", uploaded)
	}
	if got := len(srv.puts[0].Rooms); got != 2 {
		t.Errorf("rooms in the upload = %d, want 2", got)
	}
	if got := len(srv.puts[0].Rooms["!a:x"].Sessions); got != 2 {
		t.Errorf("sessions for !a:x = %d, want 2", got)
	}

	again, err := uploadRoomKeys(ctx, client, store, "v1", key.PublicKey())
	if err != nil {
		t.Fatalf("second uploadRoomKeys: %v", err)
	}
	if again != 0 {
		t.Errorf("second pass uploaded %d, want 0", again)
	}
	if len(srv.puts) != 1 {
		t.Errorf("PUTs after the second pass = %d, want 1", len(srv.puts))
	}
}

// A session backed up to one version is offered again for a new version.
func TestUploadRoomKeysReuploadsForANewVersion(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	store := crypto.NewMemoryStore(nil)
	if err := store.PutGroupSession(ctx, newTestSession(t, "!room:x")); err != nil {
		t.Fatalf("put group session: %v", err)
	}
	srv := &backupServer{}
	client := srv.start(t)
	key := newBackupKey(t)

	if _, err := uploadRoomKeys(ctx, client, store, "v1", key.PublicKey()); err != nil {
		t.Fatalf("uploadRoomKeys v1: %v", err)
	}
	uploaded, err := uploadRoomKeys(ctx, client, store, "v2", key.PublicKey())
	if err != nil {
		t.Fatalf("uploadRoomKeys v2: %v", err)
	}
	if uploaded != 1 {
		t.Errorf("uploaded into v2 = %d, want 1", uploaded)
	}
}

// forgetfulStore accepts a session and forgets it was marked.
type forgetfulStore struct {
	crypto.Store
	sessions []*crypto.InboundGroupSession
}

func (f *forgetfulStore) GetGroupSessionsWithoutKeyBackupVersion(
	context.Context, id.KeyBackupVersion,
) dbutil.RowIter[*crypto.InboundGroupSession] {
	return dbutil.NewSliceIter(f.sessions)
}

func (f *forgetfulStore) PutGroupSession(context.Context, *crypto.InboundGroupSession) error {
	return nil
}

// A store whose marks do not stick must not cause an infinite upload loop.
func TestUploadRoomKeysStopsWhenMarkingDoesNotStick(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	store := &forgetfulStore{sessions: []*crypto.InboundGroupSession{newTestSession(t, "!room:x")}}
	srv := &backupServer{}
	_, err := uploadRoomKeys(ctx, srv.start(t), store, "v1", newBackupKey(t).PublicKey())
	if err == nil {
		t.Fatal("expected an error when the store never records the backup version")
	}
	if len(srv.puts) != 1 {
		t.Errorf("PUTs = %d, want 1 — it should give up after one round, not keep going", len(srv.puts))
	}
}

func TestHoldsRoomKeys(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	store := crypto.NewMemoryStore(nil)
	held, err := holdsRoomKeys(ctx, store)
	if err != nil {
		t.Fatalf("holdsRoomKeys: %v", err)
	}
	if held {
		t.Error("an empty store reported holding room keys")
	}
	if perr := store.PutGroupSession(ctx, newTestSession(t, "!room:x")); perr != nil {
		t.Fatalf("put group session: %v", perr)
	}
	held, err = holdsRoomKeys(ctx, store)
	if err != nil {
		t.Fatalf("holdsRoomKeys: %v", err)
	}
	if !held {
		t.Error("a store with one session reported holding none")
	}
}

// A version's advertised key decodes padded or not; garbage is refused.
func TestBackupPublicKey(t *testing.T) {
	t.Parallel()

	key := newBackupKey(t)
	raw := key.PublicKey().Bytes()
	for name, encoded := range map[string]string{
		"unpadded": base64.RawStdEncoding.EncodeToString(raw),
		"padded":   base64.StdEncoding.EncodeToString(raw),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := backupPublicKey(backup.MegolmAuthData{PublicKey: id.Ed25519(encoded)})
			if err != nil {
				t.Fatalf("backupPublicKey: %v", err)
			}
			if !got.Equal(key.PublicKey()) {
				t.Error("decoded a different public key than the one encoded")
			}
		})
	}

	t.Run("too short", func(t *testing.T) {
		t.Parallel()
		short := base64.RawStdEncoding.EncodeToString([]byte("nowhere near 32 bytes"))
		if _, err := backupPublicKey(backup.MegolmAuthData{PublicKey: id.Ed25519(short)}); err == nil {
			t.Error("expected an error for a public key of the wrong length")
		}
	})
}

// "No backup" must come back as ErrNoKeyBackup, which the sweep treats as a state.
func TestTrustedBackupReportsNoBackup(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"errcode":"M_NOT_FOUND","error":"No current backup version"}`)
	}))
	t.Cleanup(srv.Close)

	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	mach := crypto.NewOlmMachine(client, nil, crypto.NewMemoryStore(nil), nil)
	if _, _, err := trustedBackup(t.Context(), mach); !errors.Is(err, api.ErrNoKeyBackup) {
		t.Errorf("trustedBackup error = %v, want %v", err, api.ErrNoKeyBackup)
	}
}
