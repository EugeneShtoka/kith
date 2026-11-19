package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// sent is one event that reached the homeserver.
type sent struct {
	typ  string
	body map[string]any
}

// directServer answers joined_members with members and records each send.
func directServer(t *testing.T, members ...string) (*httptest.Server, func() []sent) {
	t.Helper()
	var mu sync.Mutex
	var sends []sent
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		switch path := r.URL.Path; {
		case strings.HasSuffix(path, "/joined_members"):
			joined := map[string]any{}
			for _, m := range members {
				joined[m] = map[string]any{}
			}
			_ = json.NewEncoder(rw).Encode(map[string]any{"joined": joined})
		case strings.Contains(path, "/send/"):
			parts := strings.Split(path, "/")
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			mu.Lock()
			sends = append(sends, sent{typ: parts[len(parts)-2], body: body})
			mu.Unlock()
			_, _ = rw.Write([]byte(`{"event_id":"$request:x"}`))
		default:
			http.Error(rw, "unexpected path: "+path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []sent {
		mu.Lock()
		defer mu.Unlock()
		return append([]sent(nil), sends...)
	}
}

// countingCrypto is fakeCrypto that keeps what it was asked to encrypt.
type countingCrypto struct {
	fakeCrypto
	mu    sync.Mutex
	asked []map[string]any
}

func (c *countingCrypto) Encrypt(ctx context.Context, room id.RoomID, typ event.Type, content any) (*event.EncryptedEventContent, error) {
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	c.mu.Lock()
	c.asked = append(c.asked, body)
	c.mu.Unlock()
	return c.fakeCrypto.Encrypt(ctx, room, typ, content)
}

// verifyingBackend is a backend on srv with a crypto machine and a verification helper,
// as after EnableEncryption.
func verifyingBackend(t *testing.T, srv *httptest.Server) (*InProc, *countingCrypto) {
	t.Helper()
	b := backendOn(t, srv)
	withSealedStore(b)
	crypto := &countingCrypto{}
	b.wireCrypto(crypto)
	cb := verifyCallbacks{ids: b.ids, me: b.client.UserID, people: &b.ver.people, out: b.ver.events}
	vh := verificationhelper.NewVerificationHelper(b.client, nil, &b.ver.store, cb, false, false, true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b.ver.publish(ctx, ctx, cancel, vh)
	return b, crypto
}

// A person is asked in their direct chat with one request: encrypted once where the
// room is, plain where it is not. The transaction it opens is the request's event, held
// for the helper to carry on with, and is another person's.
func TestVerifyPersonSendsOneRequestToTheOtherMember(t *testing.T) {
	t.Parallel()
	for _, room := range []domain.RoomID{sealedRoom, openRoom} {
		t.Run(string(room), func(t *testing.T) {
			t.Parallel()
			srv, sends := directServer(t, me, "@ann:x")
			b, crypto := verifyingBackend(t, srv)
			ctx := context.Background()

			txnID, err := b.VerifyPerson(ctx, room)
			if err != nil {
				t.Fatalf("VerifyPerson: %v", err)
			}
			got := sends()
			if len(got) != 1 {
				t.Fatalf("sends = %v, want one", got)
			}
			request := got[0].body
			wantType := event.EventMessage.Type
			if room == sealedRoom {
				wantType = event.EventEncrypted.Type
				if len(crypto.asked) != 1 {
					t.Fatalf("encrypted %d times, want once: %v", len(crypto.asked), crypto.asked)
				}
				request = crypto.asked[0]
			} else if len(crypto.asked) != 0 {
				t.Fatalf("encrypted %v in a plain room", crypto.asked)
			}
			if got[0].typ != wantType {
				t.Errorf("sent as %s, want %s", got[0].typ, wantType)
			}
			want := map[string]any{"msgtype": "m.key.verification.request", "to": "@ann:x", "methods": []any{"m.sas.v1"}}
			for k, v := range want {
				if fmt.Sprint(request[k]) != fmt.Sprint(v) {
					t.Errorf("request %s = %v, want %v (request %v)", k, request[k], v, request)
				}
			}

			if txnID != "$request:x" {
				t.Errorf("txn = %q, want the request's event", txnID)
			}
			txn, err := b.ver.store.GetVerificationTransaction(ctx, id.VerificationTransactionID(txnID))
			if err != nil || txn.TheirUserID != "@ann:x" || txn.RoomID != testIDs.nativeRoom(room) ||
				txn.VerificationState != verificationhelper.VerificationStateRequested {
				t.Errorf("held transaction = %+v, %v; want Ann's, requested, in the room", txn, err)
			}
			if !b.ver.people.take(id.VerificationTransactionID(txnID)) {
				t.Error("the transaction is not counted as another person's")
			}
		})
	}
}

// A room with anyone but one other person in it is refused before anything is sent: a
// group, a bridged chat with its bot, a room left alone in.
func TestVerifyPersonRefusesARoomWithoutExactlyOneOther(t *testing.T) {
	t.Parallel()
	for name, members := range map[string][]string{
		"group":   {me, "@ann:x", "@bob:x"},
		"bridged": {me, "@telegram_1:x", "@telegrambot:x"},
		"alone":   {me},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv, sends := directServer(t, members...)
			b, _ := verifyingBackend(t, srv)
			if _, err := b.VerifyPerson(context.Background(), openRoom); err == nil {
				t.Fatal("VerifyPerson succeeded")
			}
			if got := sends(); len(got) != 0 {
				t.Errorf("sent %v", got)
			}
		})
	}
}

// Only a verification with this account's own session asks it for the backup key
// afterwards; one with another person, asked by them or by us, does not, and is
// marked as theirs when it arrives.
func TestOnlyOwnVerificationRestoresKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		from     id.UserID // "": we asked them
		person   bool
		restores bool
	}{
		{"our other session asks", me, false, true},
		{"another person asks", "@ann:x", true, false},
		{"we ask another person", "", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out := make(chan domain.Verification, 4)
			restored := 0
			people := &personTxns{}
			cb := verifyCallbacks{ids: testIDs, me: me, people: people, out: out,
				onDone: func(id.VerificationTransactionID) { restored++ }}
			if c.from == "" {
				people.add("t")
			} else {
				cb.VerificationRequested(ctx, "t", c.from, "D")
				if v := <-out; v.Person != c.person {
					t.Errorf("request Person = %v, want %v", v.Person, c.person)
				}
			}
			cb.VerificationDone(ctx, "t", event.VerificationMethodSAS)
			if (restored == 1) != c.restores {
				t.Errorf("restored %d times, want %v", restored, c.restores)
			}
			if people.take("t") {
				t.Error("the transaction outlived its end")
			}
		})
	}
}

// A canceled verification with another person is forgotten.
func TestACanceledPersonVerificationIsForgotten(t *testing.T) {
	t.Parallel()
	people := &personTxns{}
	cb := verifyCallbacks{ids: testIDs, me: me, people: people, out: make(chan domain.Verification, 4)}
	cb.VerificationRequested(context.Background(), "t", "@ann:x", "D")
	cb.VerificationCancelled(context.Background(), "t", event.VerificationCancelCodeUser, "no")
	if people.take("t") {
		t.Error("a canceled transaction is still another person's")
	}
}

// The helper's store is written from VerifyPerson while the helper reads it, under -race.
func TestVerifyStoreTakesWritesFromOutsideTheHelper(t *testing.T) {
	t.Parallel()
	var store verifyStore
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 8 {
		txnID := id.VerificationTransactionID(fmt.Sprint(i))
		wg.Go(func() {
			_ = store.SaveVerificationTransaction(ctx, verificationhelper.VerificationTransaction{TransactionID: txnID})
		})
		wg.Go(func() { _, _ = store.GetAllVerificationTransactions(ctx) })
	}
	wg.Wait()
	if all, _ := store.GetAllVerificationTransactions(ctx); len(all) != 8 {
		t.Errorf("held %d transactions, want 8", len(all))
	}
}
