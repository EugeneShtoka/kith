package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Scrolling off the top of an upgraded room continues in its predecessor via the token.
func TestScrollbackWalksOffTheTopIntoTheRoomThisOneReplaced(t *testing.T) {
	t.Parallel()

	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			room := roomOf(r.URL.Path)
			asked = append(asked, room)
			if room == "!old:x" {
				_, _ = w.Write([]byte(`{"start":"s","end":"e2","chunk":[{
					"type":"m.room.message","event_id":"$before:x","sender":"@bob:x",
					"origin_server_ts":1700000000000,
					"content":{"msgtype":"m.text","body":"said before the upgrade"}}]}`))
				return
			}
			// An empty chunk: the start of the new room.
			_, _ = w.Write([]byte(`{"start":"s","end":"e","chunk":[]}`))
		case strings.HasSuffix(r.URL.Path, "/joined_members"):
			_, _ = w.Write([]byte(`{"joined": {"@bob:x": {"display_name": "Bob"}}}`))
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	b, ctx := backendOn(t, srv), context.Background()
	if err := b.cache.SaveRoomUpgrade(ctx, "!new:x", db.RoomUpgrade{Predecessor: "!old:x"}); err != nil {
		t.Fatalf("SaveRoomUpgrade: %v", err)
	}

	page, err := b.Timeline(ctx, "!new:x", "", 50)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if page.Next == "" {
		t.Fatal("history stopped at the upgrade boundary; want a token continuing into the old room")
	}

	// The old room's messages stay keyed to the room that holds them.
	page, err = b.Timeline(ctx, "!new:x", page.Next, 50)
	if err != nil {
		t.Fatalf("Timeline(continued): %v", err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Body != "said before the upgrade" {
		t.Fatalf("continued page = %+v; want the pre-upgrade message", page.Messages)
	}
	if got := page.Messages[0].RoomID; got != "!old:x" {
		t.Errorf("pre-upgrade message RoomID = %q, want !old:x — rewriting it would lose its redactions", got)
	}
	if len(asked) != 2 || asked[0] != "!new:x" || asked[1] != "!old:x" {
		t.Errorf("rooms fetched = %v, want [!new:x !old:x]", asked)
	}
}

func TestARoomThatWasNeverUpgradedStillEnds(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"start":"s","end":"e","chunk":[]}`))
	}))
	defer srv.Close()

	page, err := backendOn(t, srv).Timeline(context.Background(), "!plain:x", "", 50)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if page.Next != "" {
		t.Errorf("Next = %q at the start of an ordinary room's history, want empty", page.Next)
	}
}

// An unreadable predecessor ends the history quietly.
func TestAPredecessorWeCannotReadEndsTheHistoryQuietly(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errcode":"M_FORBIDDEN"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	b, ctx := backendOn(t, srv), context.Background()
	if err := b.cache.SaveRoomUpgrade(ctx, "!new:x", db.RoomUpgrade{Predecessor: "!gone:x"}); err != nil {
		t.Fatalf("SaveRoomUpgrade: %v", err)
	}

	page, err := b.Timeline(ctx, "!new:x", chainPrefix+"1:!gone:x", 50)
	if err != nil {
		t.Fatalf("Timeline into an unreadable predecessor = %v; want the walk to end quietly", err)
	}
	if len(page.Messages) != 0 || page.Next != "" {
		t.Errorf("page = %+v; want an empty final page", page)
	}

	// The same failure at depth zero is a real error.
	if _, err := b.Timeline(ctx, "!new:x", "", 50); err == nil {
		t.Error("a forbidden fetch of the requested room returned no error")
	}
}

// The depth bound terminates a predecessor loop.
func TestAChainThatLoopsStopsWalking(t *testing.T) {
	t.Parallel()

	b, ctx := backendWithCache(t, "@me:x"), context.Background()
	if err := b.cache.SaveRoomUpgrade(ctx, "!a:x", db.RoomUpgrade{Predecessor: "!b:x"}); err != nil {
		t.Fatalf("SaveRoomUpgrade: %v", err)
	}

	if next := b.chainNext(ctx, "!a:x", 0); next == "" {
		t.Fatal("a real predecessor produced no continuation token")
	}
	if next := b.chainNext(ctx, "!a:x", chainWalkLimit); next != "" {
		t.Errorf("at the walk limit the token was %q, want empty", next)
	}
}

// Only our own well-formed continuation token redirects a fetch.
func TestOnlyOurOwnContinuationTokenRedirectsAFetch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		from      string
		wantRoom  domain.RoomID
		wantFrom  string
		wantDepth int
	}{
		{"a server token is left alone", "t42-1_0_0", "!here:x", "t42-1_0_0", 0},
		{"no token is the first page", "", "!here:x", "", 0},
		{"ours redirects to the predecessor", chainPrefix + "3:!old:x", "!old:x", "", 3},
		{"a truncated one does not", chainPrefix + "3", "!here:x", "", 0},
		{"a non-numeric depth does not", chainPrefix + "x:!old:x", "!here:x", "", 0},
		{"an empty room does not", chainPrefix + "1:", "!here:x", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			room, from, depth := chainStep("!here:x", tc.from)
			if room != tc.wantRoom || from != tc.wantFrom || depth != tc.wantDepth {
				t.Errorf("chainStep(%q) = %q, %q, %d; want %q, %q, %d",
					tc.from, room, from, depth, tc.wantRoom, tc.wantFrom, tc.wantDepth)
			}
		})
	}
}

// roomOf pulls the room ID out of a /rooms/{id}/messages path.
func roomOf(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "rooms" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
