package matrix

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/olm"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

const keyRoom = id.RoomID("!keys:x")

// keyWorld is a room with a megolm session kith does not hold, a homeserver that may
// keep that session in its key backup, and what kith asked for.
type keyWorld struct {
	t        *testing.T
	b        *InProc
	mach     *crypto.OlmMachine
	outbound olm.OutboundGroupSession
	inbound  *crypto.InboundGroupSession
	key      *backup.MegolmBackupKey
	// inBackup: the backup holds the session.
	inBackup bool

	mu        sync.Mutex
	requested []string // to-device room-key requests, as recipients
	asked     chan struct{}
}

func newKeyWorld(t *testing.T, inBackup bool) *keyWorld {
	t.Helper()
	w := &keyWorld{t: t, inBackup: inBackup, asked: make(chan struct{}, 8)}
	outbound, err := olm.NewOutboundGroupSession()
	if err != nil {
		t.Fatal(err)
	}
	w.outbound = outbound
	w.inbound, err = crypto.NewInboundGroupSession(id.SenderKey("bob-curve25519"), id.Ed25519("bob-ed25519"),
		keyRoom, outbound.Key(), 0, 0, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	w.key = newBackupKey(t)
	srv := httptest.NewServer(http.HandlerFunc(w.serve))
	t.Cleanup(srv.Close)
	w.b = backendOn(t, srv)
	w.mach = crypto.NewOlmMachine(w.b.client, nil, crypto.NewMemoryStore(nil), mautrix.NewMemoryStateStore().(crypto.StateStore))
	if err := w.mach.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *keyWorld) serve(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	switch {
	case strings.HasSuffix(r.URL.Path, "/room_keys/version"):
		pub := base64.RawStdEncoding.EncodeToString(w.key.PublicKey().Bytes())
		_, _ = rw.Write([]byte(`{"algorithm":"m.megolm_backup.v1.curve25519-aes-sha2","version":"1","count":1,"etag":"1",
			"auth_data":{"public_key":"` + pub + `"}}`))
	case strings.Contains(r.URL.Path, "/room_keys/keys/"):
		if !w.inBackup {
			rw.WriteHeader(http.StatusNotFound)
			_, _ = rw.Write([]byte(`{"errcode":"M_NOT_FOUND"}`))
			return
		}
		req, err := backupRequest([]*crypto.InboundGroupSession{w.inbound}, w.key.PublicKey())
		if err != nil {
			w.t.Error(err)
			return
		}
		if err := json.NewEncoder(rw).Encode(req.Rooms[keyRoom].Sessions[w.inbound.ID()]); err != nil {
			w.t.Error(err)
		}
	case strings.Contains(r.URL.Path, "/sendToDevice/m.room_key_request/"):
		var sent struct {
			Messages map[string]map[string]json.RawMessage `json:"messages"`
		}
		_ = json.Unmarshal(body, &sent)
		w.mu.Lock()
		for user, devices := range sent.Messages {
			for device := range devices {
				w.requested = append(w.requested, user+"/"+device)
			}
		}
		w.mu.Unlock()
		w.asked <- struct{}{}
		_, _ = rw.Write([]byte(`{}`))
	default:
		_, _ = rw.Write([]byte(`{}`))
	}
}

// encrypted is an event sent in the session, as /messages returns it.
func (w *keyWorld) encrypted(eventID, body string) *event.Event {
	w.t.Helper()
	plain, err := json.Marshal(map[string]any{
		"type": "m.room.message", "room_id": keyRoom,
		"content": map[string]any{"msgtype": "m.text", "body": body},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	ciphertext, err := w.outbound.Encrypt(plain)
	if err != nil {
		w.t.Fatal(err)
	}
	evt := &event.Event{
		ID: id.EventID(eventID), Type: event.EventEncrypted, RoomID: keyRoom, Sender: "@bob:x",
		Timestamp: 1_700_000_000_000,
	}
	raw, err := json.Marshal(map[string]any{
		"algorithm": id.AlgorithmMegolmV1, "sender_key": w.inbound.SenderKey, "device_id": "BOBPHONE",
		"session_id": w.outbound.ID(), "ciphertext": string(ciphertext),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	evt.Content.VeryRaw = raw
	if err := evt.Content.ParseRaw(event.EventEncrypted); err != nil {
		w.t.Fatal(err)
	}
	return evt
}

// wait adds events as waiting on their session and runs the ask the first one starts.
func (w *keyWorld) wait(ctx context.Context, events ...*event.Event) {
	w.t.Helper()
	// Bounded: a key that never comes must fail the test, not hang it.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	w.b.keys.start(ctx)
	var first *event.Event
	for _, evt := range events {
		if _, ask := w.b.keys.add(evt.Content.AsEncrypted().SessionID, evt); ask {
			if first != nil {
				w.t.Fatal("a second event on the same session asked again")
			}
			first = evt
		}
	}
	w.b.fetchKey(ctx, w.mach, first.RoomID, first.Sender, first.Content.AsEncrypted())
}

func (w *keyWorld) cached(eventID string) (domain.Message, bool) {
	w.t.Helper()
	m, ok, err := w.b.cache.Message(context.Background(), testIDs.room(keyRoom), testIDs.eventOf(eventID))
	if err != nil {
		w.t.Fatal(err)
	}
	return m, ok
}

// A session the backup holds is taken from it, with no request to anyone; both
// messages waiting on it are cached decrypted and handed on as late replacements.
func TestAMissingKeyIsTakenFromTheBackup(t *testing.T) {
	t.Parallel()
	w := newKeyWorld(t, true)
	ctx := context.Background()
	if err := w.mach.CryptoStore.PutSecret(ctx, id.SecretMegolmBackupV1,
		base64.RawStdEncoding.EncodeToString(w.key.Bytes())); err != nil {
		t.Fatal(err)
	}
	w.wait(ctx, w.encrypted("$one:x", "first words"), w.encrypted("$two:x", "second words"))

	for id, body := range map[string]string{"$one:x": "first words", "$two:x": "second words"} {
		if m, ok := w.cached(id); !ok || m.Body != body {
			t.Errorf("%s cached = %q, %v; want %q", id, m.Body, ok, body)
		}
	}
	if len(w.requested) != 0 {
		t.Errorf("asked %v though the backup held the key", w.requested)
	}
	for range 2 {
		select {
		case msg := <-w.b.out.msgs:
			if msg.Undecrypted || !msg.Late {
				t.Errorf("handed on %+v; want the decrypted message, late", msg)
			}
		default:
			t.Fatal("a decrypted message was not handed on")
		}
	}
}

// Without the backup key, our other devices and the sender's are asked; a session
// that then arrives decrypts what waits on it, and one that never comes leaves the
// stand-ins as they were and asks again next time.
func TestAMissingKeyIsAskedForAndWaitedFor(t *testing.T) {
	t.Parallel()

	w := newKeyWorld(t, false)
	ctx := context.Background()
	go func() {
		<-w.asked
		// The key comes from another device.
		if err := w.mach.StoreGroupSession(ctx, w.inbound); err != nil {
			t.Error(err)
		}
	}()
	w.wait(ctx, w.encrypted("$one:x", "first words"))
	if m, ok := w.cached("$one:x"); !ok || m.Body != "first words" {
		t.Errorf("cached = %q, %v; want the message once its key came", m.Body, ok)
	}
	w.mu.Lock()
	got := strings.Join(w.requested, " ")
	w.mu.Unlock()
	if !strings.Contains(got, "@me:x/*") || !strings.Contains(got, "@bob:x/*") {
		t.Errorf("asked %q; want our devices and the sender's", got)
	}

	never := newKeyWorld(t, false)
	stopped, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	never.wait(stopped, never.encrypted("$three:x", "never"))
	if _, ok := never.cached("$three:x"); ok {
		t.Error("cached a message whose key never came")
	}
	if events := never.b.keys.take(never.outbound.ID()); len(events) != 0 {
		t.Errorf("%d events still waiting after giving up; the next one would not ask", len(events))
	}
}

// An event found after its session came (another event's ask brought it) decrypts at
// once, asking nobody.
func TestAKeyAlreadyHeldIsNotAskedForAgain(t *testing.T) {
	t.Parallel()
	w := newKeyWorld(t, false)
	ctx := context.Background()
	if err := w.mach.StoreGroupSession(ctx, w.inbound); err != nil {
		t.Fatal(err)
	}
	w.wait(ctx, w.encrypted("$late:x", "found later"))
	if m, ok := w.cached("$late:x"); !ok || m.Body != "found later" {
		t.Errorf("cached = %q, %v; want it decrypted", m.Body, ok)
	}
	if len(w.requested) != 0 {
		t.Errorf("asked %v for a key already held", w.requested)
	}
}

// The decrypted copy replaces its stand-in in a timeline; a deleted one stays deleted.
func TestADecryptedMessageReplacesItsStandIn(t *testing.T) {
	t.Parallel()
	standIn := domain.Message{ID: mx + "$a", RoomID: mx + "!r", Body: undecryptableBody, Undecrypted: true, Timestamp: time.Unix(10, 0)}
	real := domain.Message{ID: mx + "$a", RoomID: mx + "!r", Body: "hello", Timestamp: time.Unix(10, 0)}
	got := domain.MergeMessages([]domain.Message{standIn}, []domain.Message{real})
	if len(got) != 1 || got[0].Body != "hello" || got[0].Undecrypted {
		t.Errorf("merged = %+v; want the decrypted message", got)
	}
	// A stand-in arriving after the message does not hide it again.
	got = domain.MergeMessages([]domain.Message{real}, []domain.Message{standIn})
	if got[0].Body != "hello" {
		t.Errorf("a late stand-in hid the message: %+v", got)
	}
}
