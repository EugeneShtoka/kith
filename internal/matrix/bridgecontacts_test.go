package matrix

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// fakeBridges is a homeserver's host with bridges' provisioning APIs under it, each
// answering whoami and contacts for this person only, as their own token.
type fakeBridges struct {
	mu sync.Mutex
	// logins is each bridge's logins: id to state.
	logins map[string]map[string]string
	// contacts is each bridge login's list.
	contacts map[string][]map[string]any
	// down is the bridges that fail.
	down map[string]bool
	// listed is each bridge login whose contacts were asked for.
	listed []string
}

func (f *fakeBridges) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer token" || r.URL.Query().Get("user_id") != "@me:x" {
		http.Error(w, `{"errcode":"M_UNKNOWN_TOKEN"}`, http.StatusUnauthorized)
		return
	}
	bridge, call, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/_matrix/provision/"), "/")
	if f.down[bridge] {
		http.Error(w, `{"errcode":"M_UNKNOWN"}`, http.StatusBadGateway)
		return
	}
	switch call {
	case "v3/whoami":
		var logins []map[string]any
		for id, state := range f.logins[bridge] {
			logins = append(logins, map[string]any{"id": id, "state": map[string]any{"state_event": state}})
		}
		reply(w, map[string]any{"logins": logins})
	case "v3/contacts":
		login := r.URL.Query().Get("login_id")
		f.listed = append(f.listed, bridge+"/"+login)
		if f.logins[bridge][login] != "CONNECTED" {
			http.Error(w, `{"errcode":"M_FORBIDDEN","error":"You must be logged in to list contacts"}`, http.StatusForbidden)
			return
		}
		reply(w, map[string]any{"contacts": f.contacts[bridge+"/"+login]})
	default:
		http.NotFound(w, r)
	}
}

// reply writes v as the bridge's JSON answer.
func reply(w http.ResponseWriter, v map[string]any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}

func contact(name string, ids ...string) map[string]any {
	return map[string]any{"name": name, "identifiers": ids}
}

// Each bridge's contact list names numbers, one source per connected login: a name
// that is a number and a contact with no number name nothing. A login logged out, and
// a bridge that cannot be read, keep what they gave last; a bridge no longer
// configured takes its names away; one off the homeserver's host is never asked,
// since the request carries the login.
func TestABridgesContactListNamesNumbers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &fakeBridges{
		logins: map[string]map[string]string{"wa-a": {"one": "CONNECTED", "two": "CONNECTED"}, "wa-b": {"b1": "CONNECTED"}},
		contacts: map[string][]map[string]any{
			"wa-a/one": {
				contact("Dana", "tel:+15550100001"),
				contact("+15550100002", "tel:+15550100002"),
				contact("No Number", "email:x@example.org"),
				contact("Not A Tel", "15550100005"),
			},
			"wa-a/two": {contact("Fay", "tel:+15550100004")},
			"wa-b/b1":  {contact("Eli", "tel:+15550100003")},
		},
		down: map[string]bool{},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	var offHost atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offHost.Add(1) // another host (its own port), which must never hear the login
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(elsewhere.Close)
	b := backendWith(t, srv, nil)
	b.client.UserID = "@me:x"
	stale := 0
	b.onRoomsStale = func() { stale++ }
	a, bb, off := srv.URL+"/_matrix/provision/wa-a", srv.URL+"/_matrix/provision/wa-b", elsewhere.URL+"/_matrix/provision/wa-a"
	book := func() domain.PhoneBook {
		got, err := b.cache.PhoneBook(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	b.readBridgeContacts(ctx, []string{a, bb, off})
	if want := (domain.PhoneBook{"15550100001": "Dana", "15550100003": "Eli", "15550100004": "Fay"}); !maps.Equal(book(), want) {
		t.Fatalf("book = %v, want %v", book(), want)
	}
	if stale == 0 {
		t.Error("reading the contacts refreshed no room list, so no client hears of the names")
	}

	fake.mu.Lock()
	fake.logins["wa-a"]["two"] = "BAD_CREDENTIALS"
	fake.down["wa-b"] = true
	fake.listed = nil
	fake.mu.Unlock()
	b.readBridgeContacts(ctx, []string{a, bb, off})
	fake.mu.Lock()
	if slices.Contains(fake.listed, "wa-a/two") {
		t.Error("the contacts of a login logged out were asked for")
	}
	fake.mu.Unlock()
	if got := book(); got["15550100004"] != "Fay" || got["15550100003"] != "Eli" {
		t.Errorf("a logged-out login and a bridge down lost their names: %v", got)
	}

	b.readBridgeContacts(ctx, []string{a})
	if got := book(); got["15550100003"] != "" || got["15550100001"] != "Dana" {
		t.Errorf("after wa-b is no longer configured: %v", got)
	}
	if n := offHost.Load(); n != 0 {
		t.Errorf("a bridge off the homeserver's host was sent the login %d times", n)
	}
}
