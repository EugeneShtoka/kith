package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
	"maunium.net/go/mautrix/event"
)

func TestIgnoredContentRoundTrip(t *testing.T) {
	t.Parallel()
	in := []string{"@bob:x", "@ann:x", "@carl:x"}
	content := ignoredContentOf(ids{}, in)
	got := content.users(ids{})
	// users() returns alphabetical order, independent of the input.
	want := []string{"@ann:x", "@bob:x", "@carl:x"}
	if !sameStringSet(got, want) {
		t.Errorf("roundtrip = %v, want %v", got, want)
	}
	// Each value is kept as the spec-mandated empty object, not elided.
	for _, id := range want {
		if string(content.IgnoredUsers[id]) != "{}" {
			t.Errorf("ignored_users[%s] = %q, want {}", id, content.IgnoredUsers[id])
		}
	}
}

func TestIgnoredFromReadsOnlyItsOwnType(t *testing.T) {
	t.Parallel()
	events := []*event.Event{
		{Type: event.Type{Type: "m.other"}},
		mkIgnoredEvent(t, []string{"@spam:x"}),
	}
	content, present := ignoredFrom(events)
	if !present {
		t.Fatal("ignoredFrom: want present")
	}
	if got := content.users(ids{}); len(got) != 1 || got[0] != "@spam:x" {
		t.Errorf("users = %v, want [@spam:x]", got)
	}
	if _, present := ignoredFrom([]*event.Event{{Type: event.Type{Type: "m.other"}}}); present {
		t.Error("another type read as ignored data")
	}
}

func TestIgnoredFromUnparseableIsAbsent(t *testing.T) {
	t.Parallel()
	// A server briefly answering junk must not blank the mirror: absence beats
	// a bad read.
	evt := &event.Event{Type: event.Type{Type: ignoredType, Class: event.AccountDataEventType}}
	evt.Content.VeryRaw = json.RawMessage(`{"ignored_users":"not an object"}`)
	if _, present := ignoredFrom([]*event.Event{evt}); present {
		t.Error("unparseable ignored data read as present")
	}
}

func TestIgnoreUserRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &fakeAccountData{data: map[string]json.RawMessage{}}
	srv := store.serve(t)
	defer srv.Close()
	b := backendWith(t, srv, nil)
	if err := b.Ignore(ctx, "@spam:x"); err != nil {
		t.Fatal(err)
	}
	if err := b.Ignore(ctx, "@spam2:x"); err != nil {
		t.Fatal(err)
	}
	got, err := b.Ignored(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"@spam2:x", "@spam:x"}; !sameStringSet(got, want) {
		t.Errorf("Ignored = %v, want %v (one write per call, both kept)", got, want)
	}
	if uerr := b.Unignore(ctx, "@spam:x"); uerr != nil {
		t.Fatal(uerr)
	}
	got, err = b.Ignored(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"@spam2:x"}; !sameStringSet(got, want) {
		t.Errorf("after unignore = %v, want %v", got, want)
	}
	// Unignoring someone not in the set is a no-op: no error, no server write.
	writesBefore := store.writes()
	if err := b.Unignore(ctx, "@never:x"); err != nil {
		t.Fatal(err)
	}
	if store.writes() != writesBefore {
		t.Errorf("Unignore on a non-member wrote to the server (%d → %d)", writesBefore, store.writes())
	}
}

// A user named with kith's "matrix:" prefix is the same user as the bare MXID: the
// homeserver gets Matrix's form, and unignoring by either spelling finds them.
func TestIgnoreUserByEitherSpelling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &fakeAccountData{data: map[string]json.RawMessage{}}
	b := backendWith(t, store.serve(t), nil)
	if err := b.Ignore(ctx, "matrix:@spam:x"); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Ignored(ctx); err != nil || !sameStringSet(got, []string{"@spam:x"}) {
		t.Fatalf("Ignored = %v, %v; want [@spam:x]", got, err)
	}
	if err := b.Unignore(ctx, "matrix:@spam:x"); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Ignored(ctx); err != nil || len(got) != 0 {
		t.Errorf("after unignore = %v, %v; want empty", got, err)
	}
}

// A server refusal on write surfaces up as an error, not a silent success; the
// mirror is left alone (the server copy is the record, so a failed write must
// not leave a lie in the cache).
func TestIgnoreUserPropagatesServerErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND"}`))
			return
		}
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	b := backendWith(t, srv, nil)
	err := b.Ignore(ctx, "@someone:x")
	if err == nil {
		t.Fatal("Ignore succeeded despite a 403 from the server")
	}
	// The cache must have no row for a write that failed.
	got, cerr := b.cache.MatrixIgnored(ctx, domain.ProtocolMatrix)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if len(got) != 0 {
		t.Errorf("mirror after a refused write = %v, want empty (server copy is the record)", got)
	}
}

// fakeAccountData holds one Matrix account's data in memory, serving the two
// endpoints the ignored list uses so tests do not need a real homeserver.
type fakeAccountData struct {
	mu    sync.Mutex
	data  map[string]json.RawMessage
	count int
}

func (f *fakeAccountData) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

func (f *fakeAccountData) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/account_data/") {
			http.Error(w, "unknown path", http.StatusNotFound)
			return
		}
		// path tail is .../account_data/<type>
		key := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			body, ok := f.data[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"not set"}`))
				return
			}
			_, _ = w.Write(body)
		case http.MethodPut:
			buf := make([]byte, r.ContentLength)
			if _, err := r.Body.Read(buf); err != nil && err.Error() != "EOF" {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.data[key] = buf
			f.count++
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mkIgnoredEvent(t *testing.T, users []string) *event.Event {
	t.Helper()
	content := ignoredContentOf(ids{}, users)
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	evt := &event.Event{Type: event.Type{Type: ignoredType, Class: event.AccountDataEventType}}
	evt.Content.VeryRaw = raw
	return evt
}
