package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A join during a name-list fetch is not lost: the stale list is not installed.
func TestAJoinDuringANameFetchIsNotLost(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/joined_members") {
			close(entered)
			<-release
			// The server's answer as of before the join.
			_, _ = w.Write([]byte(`{"joined": {"@bob:x": {"display_name": "Bob"}}}`))
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatal(err)
	}
	b := newBackend(testCache(t))
	b.client = client
	room := domain.RoomID(mx + "!r:x")

	done := make(chan struct{})
	go func() { b.roomMemberNames(context.Background(), room); close(done) }()
	<-entered

	// The sync goroutine sees Carol join meanwhile.
	carol := "@carol:x"
	evt := &event.Event{
		Type: event.StateMember, RoomID: testIDs.nativeRoom(room), StateKey: &carol,
		Content: event.Content{Parsed: &event.MemberEventContent{Membership: event.MembershipJoin, Displayname: "Carol"}},
	}
	evt.Mautrix.EventSource = event.SourceJoin | event.SourceTimeline
	b.onMember(context.Background(), evt)

	close(release)
	<-done

	if names, ok := b.names.get(room); ok && names[carol] != "Carol" {
		t.Errorf("memo after a join during the fetch = %v: Carol is missing, and would stay so", names)
	}
}
