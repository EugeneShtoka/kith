package matrix

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A sender who is no member of the room they wrote in is named by their name in another
// cached room, else their profile's, which is asked once.
func TestASenderOutsideTheRoomIsNamedElsewhere(t *testing.T) {
	t.Parallel()
	var profileAsks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/joined_members"):
			_, _ = io.WriteString(w, `{"joined":{}}`)
		case strings.Contains(r.URL.Path, "/profile/") && strings.HasSuffix(r.URL.Path, "/displayname"):
			profileAsks.Add(1)
			_, _ = io.WriteString(w, `{"displayname":"Slackbot"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errcode":"M_NOT_FOUND"}`)
		}
	}))
	t.Cleanup(srv.Close)
	b := backendWith(t, srv, nil)
	ctx := context.Background()

	if err := b.cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!other:x"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.cache.SaveMembers(ctx, "!other:x", []domain.Member{{UserID: "@dana:x", DisplayName: "Dana"}}); err != nil {
		t.Fatal(err)
	}
	if got := b.senderName(ctx, "!dm:x", "@dana:x"); got != "Dana" || profileAsks.Load() != 0 {
		t.Errorf("a sender cached elsewhere: %q after %d profile asks, want Dana and none", got, profileAsks.Load())
	}
	for range 3 {
		if got := b.senderName(ctx, "!dm:x", "@slackbot:x"); got != "Slackbot" {
			t.Fatalf("a sender known nowhere = %q, want the profile's name", got)
		}
	}
	if n := profileAsks.Load(); n != 1 {
		t.Errorf("the profile was asked %d times, want once", n)
	}
}
