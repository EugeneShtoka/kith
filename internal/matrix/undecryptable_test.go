package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// An encrypted event with no session; with b.crypto nil it stays undecrypted.
const encryptedEventJSON = `{
	"type": "m.room.encrypted",
	"event_id": "$enc:x",
	"sender": "@bob:x",
	"origin_server_ts": 1700000000000,
	"content": {"algorithm": "m.megolm.v1.aes-sha2", "ciphertext": "AwgAEnB+..."}
}`

const plainEventJSON = `{
	"type": "m.room.message",
	"event_id": "$plain:x",
	"sender": "@bob:x",
	"origin_server_ts": 1700000001000,
	"content": {"msgtype": "m.text", "body": "readable"}
}`

// backendOn is an InProc with a fresh cache, pointed at srv.
func backendOn(t *testing.T, srv *httptest.Server) *InProc {
	t.Helper()
	cache := testCache(t)
	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	b := New(cache)
	b.client = client
	return b
}

// messagesServer serves one /messages page containing chunk, newest-first.
func messagesServer(t *testing.T, chunk string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = w.Write([]byte(`{"start": "s", "end": "e", "chunk": [` + chunk + `]}`))
		case strings.HasSuffix(r.URL.Path, "/joined_members"):
			_, _ = w.Write([]byte(`{"joined": {"@bob:x": {"display_name": "Bob"}}}`))
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Regression: an undecryptable placeholder must never be cached, or it outlives the
// session's arrival.
func TestTimelineDoesNotCacheTheUndecryptablePlaceholder(t *testing.T) {
	t.Parallel()

	b := backendOn(t, messagesServer(t, encryptedEventJSON))
	ctx := context.Background()

	page, err := b.Timeline(ctx, domain.RoomID("!r:x"), "", 50)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("got %d messages, want 1: %+v", len(page.Messages), page.Messages)
	}
	if got := page.Messages[0].Body; got != undecryptableBody {
		t.Errorf("body = %q, want the placeholder %q", got, undecryptableBody)
	}

	cached, err := b.cache.Messages(ctx, domain.RoomID("!r:x"), 50)
	if err != nil {
		t.Fatalf("cache.Messages: %v", err)
	}
	if len(cached) != 0 {
		t.Errorf("cached %d messages, want 0: %+v", len(cached), cached)
	}
}

// Readable neighbors of a placeholder are still cached.
func TestTimelineCachesReadableMessagesBesideAPlaceholder(t *testing.T) {
	t.Parallel()

	b := backendOn(t, messagesServer(t, encryptedEventJSON+","+plainEventJSON))
	ctx := context.Background()

	page, err := b.Timeline(ctx, domain.RoomID("!r:x"), "", 50)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(page.Messages) != 2 {
		t.Fatalf("got %d messages, want 2 (both are shown): %+v", len(page.Messages), page.Messages)
	}

	cached, err := b.cache.Messages(ctx, domain.RoomID("!r:x"), 50)
	if err != nil {
		t.Fatalf("cache.Messages: %v", err)
	}
	if len(cached) != 1 {
		t.Fatalf("cached %d messages, want just the readable one: %+v", len(cached), cached)
	}
	if got := cached[0].ID; got != domain.EventID("$plain:x") {
		t.Errorf("cached event = %q, want $plain:x", got)
	}
}

// FetchEvent must not cache the placeholder either.
func TestFetchEventDoesNotCacheTheUndecryptablePlaceholder(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/event/"):
			_, _ = w.Write([]byte(encryptedEventJSON))
		case strings.HasSuffix(r.URL.Path, "/joined_members"):
			_, _ = w.Write([]byte(`{"joined": {"@bob:x": {"display_name": "Bob"}}}`))
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	b := backendOn(t, srv)
	ctx := context.Background()

	msg, err := b.FetchEvent(ctx, domain.RoomID("!r:x"), domain.EventID("$enc:x"))
	if err != nil {
		t.Fatalf("FetchEvent: %v", err)
	}
	if msg.Body != undecryptableBody {
		t.Errorf("body = %q, want the placeholder %q", msg.Body, undecryptableBody)
	}

	cached, err := b.cache.Messages(ctx, domain.RoomID("!r:x"), 50)
	if err != nil {
		t.Fatalf("cache.Messages: %v", err)
	}
	if len(cached) != 0 {
		t.Errorf("cached %d messages, want 0: %+v", len(cached), cached)
	}
}
