package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// fakeReceipts is a homeserver that refuses receipts until told otherwise, and records
// the receipts and marked-unread writes it takes.
type fakeReceipts struct {
	refuse  atomic.Bool
	mu      sync.Mutex
	taken   []string
	unmarks int
}

func (f *fakeReceipts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(r.URL.Path, "/messages"):
		_, _ = w.Write([]byte(`{"chunk":[]}`))
	case f.refuse.Load():
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"down"}`))
	case strings.Contains(r.URL.Path, "/receipt/"):
		f.mu.Lock()
		f.taken = append(f.taken, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	case strings.Contains(r.URL.Path, "/account_data/"):
		f.mu.Lock()
		f.unmarks++
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	default:
		_, _ = w.Write([]byte(`{}`))
	}
}

// readingBackend is a backend over a cache holding two messages in room, behind f.
func readingBackend(t *testing.T, f *fakeReceipts, room domain.RoomID) *InProc {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cache := testCache(t)
	if err := cache.SaveMessages(context.Background(), room, []domain.Message{
		{ID: mx + "$one", RoomID: room, Sender: "matrix:@dana:x", Body: "one", Timestamp: time.Unix(10, 0)},
		{ID: mx + "$two", RoomID: room, Sender: "matrix:@dana:x", Body: "two", Timestamp: time.Unix(20, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), "token")
	if err != nil {
		t.Fatal(err)
	}
	b := newBackend(cache)
	b.client = client
	return b
}

// Reading is local first: the homeserver refusing the receipt leaves the room read here
// and its marked-unread flag cleared here, the receipt owed; once the homeserver takes
// it again, the resend settles it and clears the mark there too.
func TestARoomReadInKithStaysReadWhenTheHomeserverRefuses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := &fakeReceipts{}
	f.refuse.Store(true)
	room := domain.RoomID(mx + "!r:x")
	b := readingBackend(t, f, room)
	b.unread.update(room, func(cur *domain.Unread, _ *int64) bool { cur.RoomID, cur.Marked = room, true; return true })

	if err := b.MarkRead(ctx, room, mx+"$two", false); err != nil {
		t.Fatalf("a refused receipt is an error: %v", err)
	}
	if u := b.unread.get(room); u.Marked {
		t.Error("the room is still marked unread here")
	}
	n, _, counted, err := b.cache.CountUnread(ctx, b.me(), room)
	if err != nil || !counted || n != 0 {
		t.Errorf("after reading, the homeserver refusing: %d unread (counted %v, %v)", n, counted, err)
	}
	owed := b.reads.owed()
	if r := owed[room]; r.event != mx+"$two" || !r.mainOnly || !r.unmark {
		t.Fatalf("the receipt is not owed: %+v", owed)
	}

	f.refuse.Store(false)
	b.sendOwed(ctx)
	if left := b.reads.owed(); len(left) != 0 {
		t.Errorf("a receipt the homeserver took is still owed: %+v", left)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.taken) != 1 || f.taken[0] != "$two" || f.unmarks != 1 {
		t.Errorf("the homeserver took receipts %v and %d unmarks", f.taken, f.unmarks)
	}
}

// Reading rooms without opening them is the same, and still reports the refusal: a
// bare count hides a systematic one.
func TestRoomsReadTogetherStayReadAndSaySo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := &fakeReceipts{}
	f.refuse.Store(true)
	room := domain.RoomID(mx + "!r:x")
	b := readingBackend(t, f, room)
	res, err := b.MarkRoomsRead(ctx, []domain.RoomID{room}, false)
	if err != nil || res.Failed != 1 || !strings.Contains(res.FirstError, "M_UNKNOWN") {
		t.Errorf("MarkRoomsRead = %+v, %v", res, err)
	}
	if n, _, counted, _ := b.cache.CountUnread(ctx, b.me(), room); !counted || n != 0 {
		t.Errorf("after reading together, the homeserver refusing: %d unread", n)
	}
	if r := b.reads.owed()[room]; r.event != mx+"$two" || r.mainOnly {
		t.Errorf("owed %+v, want an unthreaded receipt for $two", r)
	}
}

// A sync that succeeds sends the receipts owed, one resend at a time.
func TestASyncSendsTheReceiptsOwed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := &fakeReceipts{}
	room := domain.RoomID(mx + "!r:x")
	b := readingBackend(t, f, room)
	b.reads.add(room, owedRead{event: mx + "$two", mainOnly: true})
	// While a resend runs, another does not start.
	b.reads.sending.Store(true)
	b.sendOwed(ctx)
	if len(b.reads.owed()) != 1 {
		t.Fatal("a second resend ran beside the first")
	}
	b.reads.sending.Store(false)
	b.onSync(ctx, &mautrix.RespSync{}, "s1")
	deadline := time.Now().Add(5 * time.Second)
	for len(b.reads.owed()) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if left := b.reads.owed(); len(left) != 0 {
		t.Errorf("after a sync, still owed: %+v", left)
	}
}

// Owed receipts: a later read replaces an earlier and keeps a mark to clear; settling
// an event no longer owed leaves the later one.
func TestOwedReceipts(t *testing.T) {
	t.Parallel()
	var o owedReads
	o.add(mx+"!a:x", owedRead{event: mx + "$1", unmark: true})
	o.add(mx+"!a:x", owedRead{event: mx + "$2"})
	o.settle(mx+"!a:x", mx+"$1")
	if r := o.owed()[mx+"!a:x"]; r.event != mx+"$2" || !r.unmark {
		t.Errorf("owed %+v, want $2 still clearing the mark", r)
	}
	o.settle(mx+"!a:x", mx+"$2")
	if len(o.owed()) != 0 {
		t.Errorf("after settling: %+v", o.owed())
	}
}
