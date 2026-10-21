package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestIgnoredContentRoundTrip(t *testing.T) {
	t.Parallel()
	in := []string{"@bob:x", "@ann:x", "@carl:x"}
	content := ignoredContentOf(in)
	got := content.users()
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
	if got := content.users(); len(got) != 1 || got[0] != "@spam:x" {
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
	if err := b.ignoreUser(ctx, "@spam:x"); err != nil {
		t.Fatal(err)
	}
	if err := b.ignoreUser(ctx, "@spam2:x"); err != nil {
		t.Fatal(err)
	}
	got, err := b.ignored(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"@spam2:x", "@spam:x"}; !sameStringSet(got, want) {
		t.Errorf("Ignored = %v, want %v (one write per call, both kept)", got, want)
	}
	if uerr := b.unignoreUser(ctx, "@spam:x"); uerr != nil {
		t.Fatal(uerr)
	}
	got, err = b.ignored(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"@spam2:x"}; !sameStringSet(got, want) {
		t.Errorf("after unignore = %v, want %v", got, want)
	}
	// Unignoring someone not in the set is a no-op: no error, no server write.
	writesBefore := store.writes()
	if err := b.unignoreUser(ctx, "@never:x"); err != nil {
		t.Fatal(err)
	}
	if store.writes() != writesBefore {
		t.Errorf("unignoreUser on a non-member wrote to the server (%d → %d)", writesBefore, store.writes())
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

func mkIgnoredEvent(t *testing.T, ids []string) *event.Event {
	t.Helper()
	content := ignoredContentOf(ids)
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	evt := &event.Event{Type: event.Type{Type: ignoredType, Class: event.AccountDataEventType}}
	evt.Content.VeryRaw = raw
	return evt
}
