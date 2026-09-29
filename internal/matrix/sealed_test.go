package matrix

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const (
	sealedRoom = domain.RoomID("!sealed:x")
	openRoom   = domain.RoomID("!open:x")
)

// wire records what reached the homeserver: each send's event type and each upload.
type wire struct {
	mu      sync.Mutex
	sent    []string
	uploads int
}

func (w *wire) snapshot() ([]string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.sent...), w.uploads
}

// sealedServer answers sends, uploads and the encryption state of sealedRoom (set)
// and openRoom (absent).
func sealedServer(t *testing.T) (*httptest.Server, *wire) {
	t.Helper()
	w := &wire{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/state/m.room.encryption"):
			if strings.Contains(path, string(sealedRoom)) {
				_, _ = rw.Write([]byte(`{"algorithm":"m.megolm.v1.aes-sha2"}`))
				return
			}
			rw.WriteHeader(http.StatusNotFound)
			_, _ = rw.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"no encryption"}`))
		case strings.Contains(path, "/send/"):
			parts := strings.Split(path, "/")
			w.mu.Lock()
			w.sent = append(w.sent, parts[len(parts)-2])
			w.mu.Unlock()
			_, _ = rw.Write([]byte(`{"event_id":"$e:x"}`))
		case strings.HasSuffix(path, "/upload"):
			w.mu.Lock()
			w.uploads++
			w.mu.Unlock()
			_, _ = rw.Write([]byte(`{"content_uri":"mxc://x/f"}`))
		default:
			http.Error(rw, "unexpected path: "+path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, w
}

// fakeCrypto seals every event; it stands in for the crypto helper mautrix calls.
type fakeCrypto struct{}

func (fakeCrypto) Encrypt(context.Context, id.RoomID, event.Type, any) (*event.EncryptedEventContent, error) {
	return &event.EncryptedEventContent{Algorithm: id.AlgorithmMegolmV1, MegolmCiphertext: []byte("sealed")}, nil
}

func (fakeCrypto) Decrypt(context.Context, *event.Event) (*event.Event, error) {
	return nil, errors.New("fake crypto decrypts nothing")
}

func (fakeCrypto) WaitForSession(context.Context, id.RoomID, id.SenderKey, id.SessionID, time.Duration) bool {
	return false
}

func (fakeCrypto) RequestSession(context.Context, id.RoomID, id.SenderKey, id.SessionID, id.UserID, id.DeviceID) {
}

func (fakeCrypto) Init(context.Context) error { return nil }

// withSealedStore gives b a state store that has seen both rooms' state, as the crypto
// helper's store has after a sync: sealedRoom is encrypted, and openRoom is plain (its
// power levels are known, and it has no encryption event).
func withSealedStore(b *InProc) {
	store := mautrix.NewMemoryStateStore()
	// The memory store cannot fail.
	_ = store.SetEncryptionEvent(context.Background(), id.RoomID(sealedRoom),
		&event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1})
	for _, room := range []domain.RoomID{sealedRoom, openRoom} {
		_ = store.SetPowerLevels(context.Background(), id.RoomID(room), &event.PowerLevelsEventContent{})
	}
	b.client.StateStore = store
}

func sampleFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("the plan, in full"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// With no crypto machine, nothing reaches an encrypted room: mautrix would post it in
// the clear, and for a file that includes the key that decrypts it.
func TestNoCryptoRefusesEncryptedRoomsAndSendsToOthers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		store   func(*InProc) // nil: no state store, as before the crypto machine
		room    domain.RoomID
		file    bool
		refused bool
	}{
		{"message, encrypted room, homeserver asked", nil, sealedRoom, false, true},
		{"file, encrypted room, homeserver asked", nil, sealedRoom, true, true},
		{"message, encrypted room, state store", withSealedStore, sealedRoom, false, true},
		{"file, encrypted room, state store", withSealedStore, sealedRoom, true, true},
		{"message, unencrypted room", nil, openRoom, false, false},
		{"file, unencrypted room", nil, openRoom, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, w := sealedServer(t)
			b := backendOn(t, srv)
			if c.store != nil {
				c.store(b)
			}
			ctx := context.Background()

			var err error
			if c.file {
				err = b.SendFile(ctx, c.room, sampleFile(t), "")
			} else {
				err = b.Send(ctx, c.room, domain.Draft{Body: "the plan"})
			}
			sent, uploads := w.snapshot()
			if c.refused {
				if !errors.Is(err, api.ErrNoEncryption) {
					t.Fatalf("err = %v, want ErrNoEncryption", err)
				}
				if len(sent) != 0 || uploads != 0 {
					t.Fatalf("reached the homeserver: sends %v, uploads %d", sent, uploads)
				}
				return
			}
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if len(sent) != 1 || sent[0] != event.EventMessage.Type {
				t.Fatalf("sends = %v, want one %s", sent, event.EventMessage.Type)
			}
		})
	}
}

// Sends racing the crypto machine's arrival: each is refused or goes out encrypted,
// never in the clear, and -race sees client.Crypto written under sendMu.
func TestSendsRacingEncryptionNeverGoOutInTheClear(t *testing.T) {
	t.Parallel()
	srv, w := sealedServer(t)
	b := backendOn(t, srv)
	withSealedStore(b) // the crypto helper always brings one
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			err := b.Send(ctx, sealedRoom, domain.Draft{Body: "the plan"})
			if err != nil && !errors.Is(err, api.ErrNoEncryption) {
				t.Errorf("send: %v", err)
			}
		})
	}
	wg.Go(func() { b.wireCrypto(fakeCrypto{}) })
	wg.Wait()

	if err := b.Send(ctx, sealedRoom, domain.Draft{Body: "after"}); err != nil {
		t.Fatalf("send once crypto is wired: %v", err)
	}
	sent, _ := w.snapshot()
	for _, typ := range sent {
		if typ != event.EventEncrypted.Type {
			t.Fatalf("an event went out as %s: %v", typ, sent)
		}
	}
	if len(sent) == 0 {
		t.Fatal("nothing was sent once crypto was wired")
	}
}

// A reaction is content too: mautrix sends m.reaction in the clear whatever the room,
// so an encrypted room gets it sealed by the machine, or refused without one.
func TestReactionsAreSealedInEncryptedRooms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		crypto  bool
		room    domain.RoomID
		refused bool
		want    string
	}{
		{"encrypted room, no machine", false, sealedRoom, true, ""},
		{"encrypted room, machine", true, sealedRoom, false, event.EventEncrypted.Type},
		{"unencrypted room, no machine", false, openRoom, false, event.EventReaction.Type},
		{"unencrypted room, machine", true, openRoom, false, event.EventReaction.Type},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, w := sealedServer(t)
			b := backendOn(t, srv)
			if c.crypto {
				withSealedStore(b)
				b.wireCrypto(fakeCrypto{})
			}

			err := b.SendReaction(context.Background(), c.room, "$target:x", "👍")
			sent, _ := w.snapshot()
			if c.refused {
				if !errors.Is(err, api.ErrNoEncryption) {
					t.Fatalf("err = %v, want ErrNoEncryption", err)
				}
				if len(sent) != 0 {
					t.Fatalf("reached the homeserver: %v", sent)
				}
				return
			}
			if err != nil {
				t.Fatalf("send reaction: %v", err)
			}
			if len(sent) != 1 || sent[0] != c.want {
				t.Fatalf("sends = %v, want one %s", sent, c.want)
			}
		})
	}
}
