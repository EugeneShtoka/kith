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

// accountDataServer holds room account data of one type per room, and the account's
// person verdicts, and records writes.
type accountDataServer struct {
	*httptest.Server
	mu     sync.Mutex
	stored map[string]string // room ID → the JSON content of org.kith.tags; peopleType → org.kith.people
	puts   int
}

func newAccountDataServer(t *testing.T) *accountDataServer {
	t.Helper()
	s := &accountDataServer{stored: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// .../user/{user}/rooms/{room}/account_data/{type}, or .../user/{user}/account_data/{type}
		parts := strings.Split(r.URL.Path, "/")
		at := -1
		for i, p := range parts {
			if p == "account_data" {
				at = i
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if at < 2 || at+1 >= len(parts) || (parts[at+1] != tagsType && parts[at+1] != peopleType) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errcode":"M_UNRECOGNIZED"}`)
			return
		}
		room := parts[at-1]
		if parts[at+1] == peopleType {
			room = peopleType
		}
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

func (s *accountDataServer) content(t *testing.T, room string) tagsContent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.stored[room]
	if !ok {
		t.Fatalf("no account data for %s", room)
	}
	var out tagsContent
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

// A move and a release are written per tag into the room's one account data value;
// another tag's entry is kept, and the mirror follows.
func TestMovesAndReleasesAreWrittenPerTag(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newAccountDataServer(t)
	b := backendWith(t, srv.Server, nil)

	moved := domain.TagVerdict{Room: "!a:x", Tag: "Spam", Rule: domain.MoveFirstMessage, Filter: "crypto", At: time.Now()}
	if err := b.MarkTag(ctx, moved); err != nil {
		t.Fatalf("MarkTag(move) error = %v", err)
	}
	if err := b.MarkTag(ctx, domain.TagVerdict{Room: "!a:x", Tag: "Leads", Rule: domain.MoveAlways, Filter: "quotes"}); err != nil {
		t.Fatalf("MarkTag(second tag) error = %v", err)
	}
	if got := srv.content(t, "!a:x").Verdicts; got["spam"].Rule != int(domain.MoveFirstMessage) || got["leads"].Filter != "quotes" {
		t.Fatalf("account data = %+v, want both tags' moves", got)
	}

	if err := b.MarkTag(ctx, domain.TagVerdict{Room: "!a:x", Tag: "SPAM", Released: true}); err != nil {
		t.Fatalf("MarkTag(release) error = %v", err)
	}
	got := srv.content(t, "!a:x").Verdicts
	if !got["spam"].Released || got["spam"].Rule != 0 || got["spam"].At == 0 || got["leads"].Rule != int(domain.MoveAlways) {
		t.Errorf("account data = %+v, want Spam released with a time and Leads kept", got)
	}
	mirrored, err := b.cache.RoomVerdicts(ctx, "!a:x")
	if err != nil || len(mirrored) != 2 {
		t.Fatalf("mirror = %+v, %v; want both", mirrored, err)
	}

	if err := b.RetagVerdicts(ctx, "Leads", "Prospects"); err != nil {
		t.Fatalf("RetagVerdicts error = %v", err)
	}
	if got := srv.content(t, "!a:x").Verdicts; got["prospects"].Filter != "quotes" || len(got) != 2 {
		t.Errorf("after retag: %+v, want Leads' move under prospects", got)
	}
}

// A filter's move never overwrites a release, even one only on the homeserver.
func TestAMoveCannotOverwriteARelease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newAccountDataServer(t)
	// Released elsewhere: on the homeserver, not in this cache.
	srv.stored["!a:x"] = `{"verdicts":{"spam":{"released":true,"at_ms":1700000000000}}}`
	b := backendWith(t, srv.Server, nil)

	err := b.MarkTag(ctx, domain.TagVerdict{Room: "!a:x", Tag: "Spam", Rule: domain.MoveDirect, Filter: "crypto"})
	if !errors.Is(err, domain.ErrReleased) {
		t.Fatalf("MarkTag(move over a release) error = %v, want ErrReleased", err)
	}
	if srv.writes() != 0 {
		t.Errorf("the release was overwritten: %+v", srv.content(t, "!a:x"))
	}
	// The release it found is mirrored.
	if got, _ := b.cache.TagVerdicts(ctx); len(got) != 1 || !got[0].Released {
		t.Errorf("mirror = %+v, want the release the server had", got)
	}
}

// Tag account data in a sync parses and mirrors.
func TestTagAccountDataParsesAndMirrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newAccountDataServer(t)
	b := backendWith(t, srv.Server, nil)

	evt := &event.Event{Type: event.Type{Type: tagsType, Class: event.AccountDataEventType}}
	evt.Content.VeryRaw = json.RawMessage(`{"verdicts":{"spam":{"rule":2,"filter":"crypto","at_ms":1700000000000},"leads":{"released":true,"at_ms":1700000000000}}}`)
	content, present := tagsFrom([]*event.Event{evt})
	if !present || len(content.Verdicts) != 2 {
		t.Fatalf("tagsFrom = %+v, %v; want both entries", content, present)
	}
	b.mirrorVerdicts(ctx, "!a:x", content.verdicts("!a:x"))
	got, err := b.cache.RoomVerdicts(ctx, "!a:x")
	if err != nil {
		t.Fatal(err)
	}
	byTag := map[string]domain.TagVerdict{}
	for _, v := range got {
		byTag[v.Tag] = v
	}
	if v := byTag["spam"]; !v.In() || v.Rule != domain.MoveFirstMessage || v.Filter != "crypto" {
		t.Errorf("move mirrored as %+v", v)
	}
	if v := byTag["leads"]; !v.Released || v.In() {
		t.Errorf("release mirrored as %+v", v)
	}
	if _, present := tagsFrom([]*event.Event{{Type: event.Type{Type: "m.other"}}}); present {
		t.Error("another type read as tag data")
	}
}
