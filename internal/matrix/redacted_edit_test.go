package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A deleted edit, as the daemon handles it live: the message it showed goes back to
// the version before it, and the client is sent that, marked to replace what it shows.
func TestADeletedEditRevertsTheMessage(t *testing.T) {
	t.Parallel()

	original := domain.Message{ID: "$m", Sender: "@dana:x", Body: "v0", Timestamp: time.UnixMilli(5000)}
	edit := domain.Message{ID: "$m", Sender: "@dana:x", Body: "v1 secret", RevisionID: "$e1",
		Edited: true, Timestamp: time.UnixMilli(7000), EditedAt: time.UnixMilli(7000)}

	for _, tc := range []struct {
		name   string
		keep   bool
		server http.HandlerFunc // nil: the version is cached, nothing is asked
		// want are the bodies sent to the client, in order.
		want []string
	}{
		{name: "kept history reverts at once", keep: true, want: []string{"v0"}},
		{name: "the server is asked for the version before", server: serveOriginal("v0"), want: []string{"", "v0"}},
		{name: "a server that fails leaves it empty", server: func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"errcode":"M_UNKNOWN"}`, http.StatusInternalServerError)
		}, want: []string{""}},
		{name: "a server with only the deleted edit leaves it empty", server: serveOriginal(""), want: []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			b := backendWithCache(t, "@me:x")
			if tc.server != nil {
				srv := httptest.NewServer(tc.server)
				t.Cleanup(srv.Close)
				client, err := mautrix.NewClient(srv.URL, "@me:x", "token")
				if err != nil {
					t.Fatal(err)
				}
				b.client = client
			}
			b.keepDeleted.Store(tc.keep)
			b.out.open()
			save := b.cache.SaveMessages
			if tc.keep {
				save = b.cache.SaveMessagesWithRevisions
			}
			if err := save(ctx, "!r:x", []domain.Message{original, edit}); err != nil {
				t.Fatal(err)
			}

			b.onRedaction(ctx, &event.Event{Type: event.EventRedaction, RoomID: "!r:x",
				Sender: "@dana:x", Redacts: "$e1", Timestamp: 9000})
			b.fetches.wg.Wait()

			var got []string
			for len(b.out.msgs) > 0 {
				msg := <-b.out.msgs
				if msg.ID != "$m" || !msg.Reverted {
					t.Errorf("sent %+v, want $m marked Reverted", msg)
				}
				if strings.Contains(msg.Body, "secret") {
					t.Errorf("sent the deleted edit's words: %q", msg.Body)
				}
				got = append(got, msg.Body)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("sent bodies %q, want %q", got, tc.want)
			}
			cached, _, err := b.cache.MessageByID(ctx, "!r:x", "$m")
			if err != nil || cached.Body != tc.want[len(tc.want)-1] {
				t.Errorf("cached body %q (%v), want %q", cached.Body, err, tc.want[len(tc.want)-1])
			}
		})
	}
}

// serveOriginal is a homeserver holding $m with body (empty: redacted) and one
// replacement, the deleted edit, whose content the server has stripped.
func serveOriginal(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/relations/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"chunk": []map[string]any{{
				"event_id": "$e1", "type": "m.room.message", "sender": "@dana:x",
				"origin_server_ts": 7000, "room_id": "!r:x", "content": map[string]any{},
			}}})
		case strings.Contains(r.URL.Path, "/event/"):
			content := map[string]any{}
			if body != "" {
				content = map[string]any{"msgtype": "m.text", "body": body}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"event_id": "$m", "type": "m.room.message", "sender": "@dana:x",
				"origin_server_ts": 5000, "room_id": id.RoomID("!r:x"), "content": content,
			})
		default:
			http.NotFound(w, r)
		}
	}
}

// Deleting many edits at once asks the server for at most readFetchWorkers versions at
// a time, and for each deleted edit once.
func TestDeletedEditFetchesAreBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var mu sync.Mutex
	inFlight, most := 0, 0
	perPath := map[string]int{}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		perPath[r.URL.Path]++
		most = max(most, inFlight)
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		serveOriginal("v0")(w, r)
	}))
	t.Cleanup(srv.Close)
	b := backendWithCache(t, "@me:x")
	client, err := mautrix.NewClient(srv.URL, "@me:x", "token")
	if err != nil {
		t.Fatal(err)
	}
	b.client = client
	b.out.open()
	const n = 3 * readFetchWorkers
	for i := range n {
		id := domain.EventID(fmt.Sprintf("$m%d", i))
		rev := domain.EventID(fmt.Sprintf("$e%d", i))
		msgs := []domain.Message{
			{ID: id, Sender: "@dana:x", Body: "v0", Timestamp: time.UnixMilli(5000)},
			{ID: id, Sender: "@dana:x", Body: "v1", RevisionID: rev, Edited: true,
				Timestamp: time.UnixMilli(7000), EditedAt: time.UnixMilli(7000)},
		}
		if err := b.cache.SaveMessages(ctx, "!r:x", msgs); err != nil {
			t.Fatal(err)
		}
	}
	redact := func(i int) {
		b.onRedaction(ctx, &event.Event{Type: event.EventRedaction, RoomID: "!r:x",
			Sender: "@dana:x", Redacts: id.EventID(fmt.Sprintf("$e%d", i)), Timestamp: 9000})
	}
	for i := range n {
		redact(i)
	}
	redact(0) // the same deleted edit again
	time.Sleep(50 * time.Millisecond)
	close(release)
	b.fetches.wg.Wait()
	for len(b.out.msgs) > 0 {
		<-b.out.msgs
	}
	if most > readFetchWorkers {
		t.Errorf("%d fetches ran at once, want at most %d", most, readFetchWorkers)
	}
	for path, times := range perPath {
		if times != 1 {
			t.Errorf("%s was asked %d times, want once: each deleted edit is fetched once", path, times)
		}
	}
}
