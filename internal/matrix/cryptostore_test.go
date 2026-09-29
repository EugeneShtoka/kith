package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// keysServer answers what the crypto helper's Init asks (our own keys: none yet, so
// it uploads them) and the encryption state of sealedRoom.
func keysServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		switch path := r.URL.Path; {
		case strings.HasSuffix(path, "/keys/query"):
			_, _ = rw.Write([]byte(`{"device_keys": {}}`))
		case strings.HasSuffix(path, "/keys/upload"):
			_, _ = rw.Write([]byte(`{"one_time_key_counts": {"signed_curve25519": 50}}`))
		case strings.Contains(path, "/state/m.room.encryption"):
			_, _ = rw.Write([]byte(`{"algorithm":"m.megolm.v1.aes-sha2"}`))
		default:
			http.Error(rw, "unexpected path: "+path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// cryptoBackendOn is a logged-in backend on srv with its crypto store open.
func cryptoBackendOn(t *testing.T, srv *httptest.Server) *InProc {
	t.Helper()
	b := backendOn(t, srv)
	b.client.DeviceID = "DEV"
	if err := b.OpenCryptoStore(context.Background(), filepath.Join(t.TempDir(), "crypto.db")); err != nil {
		t.Fatalf("OpenCryptoStore() error = %v", err)
	}
	t.Cleanup(b.Stop)
	return b
}

// The state store is in place before any RPC runs, and sync keeps it current: the
// crypto helper feeds only a store it made itself, and an unfed store would read a
// room that turned on encryption as plain.
func TestTheCryptoStoreFollowsSync(t *testing.T) {
	t.Parallel()

	b := cryptoBackendOn(t, keysServer(t))
	ctx := context.Background()
	if b.stateStore() == nil {
		t.Fatal("no state store after OpenCryptoStore")
	}
	if encrypted, err := b.stateStore().IsEncrypted(ctx, id.RoomID(sealedRoom)); err != nil || encrypted {
		t.Fatalf("IsEncrypted before any state = %v, %v; want false", encrypted, err)
	}

	empty := ""
	evt := &event.Event{
		Type: event.StateEncryption, RoomID: id.RoomID(sealedRoom), StateKey: &empty,
		Content: event.Content{Parsed: &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}},
	}
	b.client.Syncer.(mautrix.DispatchableSyncer).Dispatch(ctx, evt)
	if !b.roomEncrypted(ctx, sealedRoom) {
		t.Error("a synced m.room.encryption never reached the state store")
	}
}

// On a degraded start, encryption comes up while RPCs already run: they read the
// state store (ours, and mautrix's after each call) and the sync store. Meaningful
// under -race, which fails on any unordered write.
func TestEncryptionComesUpUnderLiveRPCs(t *testing.T) {
	t.Parallel()

	b := cryptoBackendOn(t, keysServer(t))
	ctx := context.Background()

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Go(func() {
		<-start
		if err := b.EnableEncryption(ctx, []byte("0123456789abcdef0123456789abcdef")); err != nil {
			t.Errorf("EnableEncryption() error = %v", err)
		}
	})
	for range 4 {
		wg.Go(func() {
			<-start
			for range 50 {
				b.roomEncrypted(ctx, sealedRoom)
				// mautrix writes the answer into client.StateStore after the call.
				b.encryptedWithoutCrypto(ctx, sealedRoom)
				if err := b.resetSyncPosition(ctx); err != nil {
					t.Errorf("resetSyncPosition() error = %v", err)
					return
				}
			}
		})
	}
	close(start)
	wg.Wait()

	if b.cryptoHelper() == nil {
		t.Fatal("no crypto machine after EnableEncryption")
	}
	if b.client.StateStore != b.stateStore() || b.client.StateStore == nil {
		t.Error("the state store changed or vanished when encryption came up")
	}
}
