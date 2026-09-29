package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// accountDataServer holds room account data of one type per room and records writes.
type accountDataServer struct {
	*httptest.Server
	mu     sync.Mutex
	stored map[string]string // room ID → the JSON content of org.kith.spam
	puts   int
}

func newAccountDataServer(t *testing.T) *accountDataServer {
	t.Helper()
	s := &accountDataServer{stored: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// .../user/{user}/rooms/{room}/account_data/{type}
		parts := strings.Split(r.URL.Path, "/")
		at := -1
		for i, p := range parts {
			if p == "account_data" {
				at = i
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if at < 2 || at+1 >= len(parts) || parts[at+1] != spamType {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errcode":"M_UNRECOGNIZED"}`)
			return
		}
		room := parts[at-1]
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			s.stored[room] = string(body)
			s.puts++
			_, _ = io.WriteString(w, `{}`)
		case http.MethodGet:
			content, ok := s.stored[room]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"errcode":"M_NOT_FOUND","error":"not found"}`)
				return
			}
			_, _ = io.WriteString(w, content)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *accountDataServer) content(t *testing.T, room string) spamContent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.stored[room]
	if !ok {
		t.Fatalf("no account data for %s", room)
	}
	var out spamContent
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("account data for %s = %s: %v", room, raw, err)
	}
	return out
}

func (s *accountDataServer) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

// Releasing a room replaces the verdict with a release in account data.
func TestReleasingARoomWritesTheReleaseToAccountData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newAccountDataServer(t)
	b := backendWith(t, srv.Server, nil)

	caught := domain.SpamVerdict{Room: "!a:x", Rule: domain.SpamFirstMessage, Filter: "crypto", At: time.Now()}
	if err := b.MarkSpam(ctx, caught); err != nil {
		t.Fatalf("MarkSpam(caught) error = %v", err)
	}
	if got := srv.content(t, "!a:x"); got.Rule != int(domain.SpamFirstMessage) || got.Released {
		t.Fatalf("account data = %+v, want the verdict", got)
	}

	if err := b.MarkSpam(ctx, domain.SpamVerdict{Room: "!a:x", Released: true}); err != nil {
		t.Fatalf("MarkSpam(released) error = %v", err)
	}
	got := srv.content(t, "!a:x")
	if got.Rule != int(domain.SpamNotSpam) || !got.Released {
		t.Errorf("account data = %+v, want the verdict gone and the release recorded", got)
	}
	if got.At == 0 {
		t.Error("the release carries no time")
	}

	rooms, err := b.SpamRooms(ctx)
	if err != nil {
		t.Fatalf("SpamRooms() error = %v", err)
	}
	if len(rooms) != 1 || !rooms[0].Released || rooms[0].Spam() {
		t.Errorf("SpamRooms() = %+v, want the mirrored release", rooms)
	}
}

// A rule never overwrites a release, even one not yet in this machine's cache: the
// homeserver's copy is checked before writing.
func TestARuleCannotOverwriteARelease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newAccountDataServer(t)
	// Released elsewhere: on the homeserver, and not in this machine's cache.
	srv.stored["!a:x"] = `{"rule":0,"released":true,"at_ms":1700000000000}`
	b := backendWith(t, srv.Server, nil)

	err := b.MarkSpam(ctx, domain.SpamVerdict{Room: "!a:x", Rule: domain.SpamDirect, Filter: "crypto"})
	if !errors.Is(err, domain.ErrSpamReleased) {
		t.Fatalf("MarkSpam(rule over a release) error = %v, want ErrSpamReleased", err)
	}
	if srv.writes() != 0 {
		t.Errorf("the release was overwritten: %+v", srv.content(t, "!a:x"))
	}
	// The release it found is mirrored.
	rooms, _ := b.SpamRooms(ctx)
	if len(rooms) != 1 || !rooms[0].Released {
		t.Errorf("SpamRooms() = %+v, want the release the server had", rooms)
	}
}

// Verdict and release content parse and mirror into SpamRooms.
func TestSpamAccountDataParsesAndMirrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newAccountDataServer(t)
	b := backendWith(t, srv.Server, nil)

	old := &event.Event{Type: event.Type{Type: spamType, Class: event.AccountDataEventType}}
	old.Content.VeryRaw = json.RawMessage(`{"rule":2,"filter":"crypto","at_ms":1700000000000}`)
	content, present := spamFrom([]*event.Event{old})
	if !present || content.Rule != 2 || content.Released {
		t.Fatalf("spamFrom(old content) = %+v, %v; want the verdict", content, present)
	}
	b.spamMirror(ctx, "!old:x", content)

	released := &event.Event{Type: event.Type{Type: spamType, Class: event.AccountDataEventType}}
	released.Content.VeryRaw = json.RawMessage(`{"rule":0,"released":true,"at_ms":1700000000000}`)
	content, present = spamFrom([]*event.Event{released})
	if !present || !content.Released {
		t.Fatalf("spamFrom(release) = %+v, %v; want a release", content, present)
	}
	b.spamMirror(ctx, "!rel:x", content)

	rooms, err := b.SpamRooms(ctx)
	if err != nil {
		t.Fatalf("SpamRooms() error = %v", err)
	}
	byRoom := map[domain.RoomID]domain.SpamVerdict{}
	for _, v := range rooms {
		byRoom[v.Room] = v
	}
	if v := byRoom["!old:x"]; !v.Spam() || v.Rule != domain.SpamFirstMessage || v.Filter != "crypto" {
		t.Errorf("old verdict mirrored as %+v", v)
	}
	if v := byRoom["!rel:x"]; !v.Released || v.Spam() {
		t.Errorf("release mirrored as %+v", v)
	}
}
