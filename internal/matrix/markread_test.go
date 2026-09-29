package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// receiptServer records the receipts posted to it and refuses the rooms in reject.
type receiptServer struct {
	*httptest.Server
	mu     sync.Mutex
	posted []string // "roomID → eventID", in the order they arrived
}

func newReceiptServer(t *testing.T, reject map[string]bool) *receiptServer {
	t.Helper()
	rs := &receiptServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// .../rooms/{roomID}/receipt/m.read/{eventID}
		parts := strings.Split(r.URL.Path, "/")
		at := -1
		for i, p := range parts {
			if p == "receipt" {
				at = i
				break
			}
		}
		if at < 2 || at+2 >= len(parts) {
			http.Error(w, `{"errcode":"M_UNRECOGNIZED"}`, http.StatusNotFound)
			return
		}
		room, evt := parts[at-1], parts[at+2]
		w.Header().Set("Content-Type", "application/json")
		if !receiptBodyOK(w, r) {
			return
		}
		if reject[room] {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"no"}`))
			return
		}
		rs.mu.Lock()
		rs.posted = append(rs.posted, room+" → "+evt)
		rs.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *receiptServer) receipts() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.posted...)
}

// backendWith returns an InProc talking to srv, backed by a fresh cache holding
// msgs per room.
func backendWith(t *testing.T, srv *httptest.Server, msgs map[domain.RoomID][]domain.Message) *InProc {
	t.Helper()
	cache := testCache(t)
	for roomID, list := range msgs {
		if serr := cache.SaveMessages(context.Background(), roomID, list); serr != nil {
			t.Fatalf("SaveMessages: %v", serr)
		}
	}
	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	b := New(cache)
	b.client = client
	return b
}

// Each room's newest cached event is receipted; uncached rooms are skipped and a
// refusal is counted without costing the others.
func TestMarkRoomsReadReceiptsTheNewestCachedEvent(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	srv := newReceiptServer(t, map[string]bool{"!refused:x": true})
	b := backendWith(t, srv.Server, map[domain.RoomID][]domain.Message{
		"!a:x": {
			{ID: "$a-old", Timestamp: base},
			{ID: "$a-new", Timestamp: base.Add(time.Hour)},
		},
		"!b:x":       {{ID: "$b", Timestamp: base}},
		"!refused:x": {{ID: "$r", Timestamp: base}},
	})

	// !cold:x has nothing cached; the others are asked for in a deliberate jumble.
	got, err := b.MarkRoomsRead(context.Background(),
		[]domain.RoomID{"!a:x", "!cold:x", "!refused:x", "!b:x"}, false)
	if err != nil {
		t.Fatalf("MarkRoomsRead() error = %v", err)
	}
	if !strings.Contains(got.FirstError, "M_FORBIDDEN") {
		t.Errorf("FirstError = %q, want the homeserver's refusal", got.FirstError)
	}
	got.FirstError = ""
	want := domain.ReadResult{Marked: 2, Skipped: 1, Failed: 1}
	if got != want {
		t.Errorf("MarkRoomsRead() = %+v, want %+v", got, want)
	}
	receipts := srv.receipts()
	sort.Strings(receipts)
	if wantReceipts := []string{"!a:x → $a-new", "!b:x → $b"}; !reflect.DeepEqual(receipts, wantReceipts) {
		t.Errorf("receipts = %v, want %v", receipts, wantReceipts)
	}
}

// A canceled context stops the walk and reports an error.
func TestMarkRoomsReadStopsWhenCanceled(t *testing.T) {
	t.Parallel()

	srv := newReceiptServer(t, nil)
	b := backendWith(t, srv.Server, map[domain.RoomID][]domain.Message{
		"!a:x": {{ID: "$a", Timestamp: time.Unix(1, 0)}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := b.MarkRoomsRead(ctx, []domain.RoomID{"!a:x"}, false); err == nil {
		t.Error("MarkRoomsRead() on a canceled context: error = nil, want the cancellation")
	}
	if n := len(srv.receipts()); n != 0 {
		t.Errorf("%d receipts posted after cancellation, want none", n)
	}
}

// Without a cache this fails rather than reporting every room skipped.
func TestMarkRoomsReadWithoutACacheFails(t *testing.T) {
	t.Parallel()

	if _, err := New(nil).MarkRoomsRead(context.Background(), []domain.RoomID{"!a:x"}, false); err == nil {
		t.Error("MarkRoomsRead() with no cache: error = nil, want a failure")
	}
}

// Regression: marking a room read must clear its threads locally too (write the
// thread_read floor), not wait for the receipt to echo back through sync.
func TestMarkRoomsReadClearsThreadsLocally(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	srv := newReceiptServer(t, nil)
	b := backendWith(t, srv.Server, map[domain.RoomID][]domain.Message{
		"!a:x": {
			{ID: "$root", Sender: "@them:x", Timestamp: base},
			{ID: "$reply", Sender: "@them:x", ThreadRoot: "$root", Timestamp: base.Add(time.Minute)},
			{ID: "$newest", Sender: "@them:x", Timestamp: base.Add(2 * time.Minute)},
		},
	})
	ctx := context.Background()

	before, err := b.cache.CountThreadUnread(ctx, []string{"@me:x"}, "!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("expected one unread thread to start with, got %d", len(before))
	}

	if _, markErr := b.MarkRoomsRead(ctx, []domain.RoomID{"!a:x"}, false); markErr != nil {
		t.Fatalf("MarkRoomsRead: %v", markErr)
	}

	after, err := b.cache.CountThreadUnread(ctx, []string{"@me:x"}, "!a:x")
	if err != nil {
		t.Fatalf("CountThreadUnread after: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("%d thread(s) still unread after marking the room read: %+v", len(after), after)
	}
}
