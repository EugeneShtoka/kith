package matrix

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// syncBuffer is a bytes.Buffer safe for the logger's concurrent writers.
type syncBuffer struct {
	mu  chan struct{}
	buf bytes.Buffer
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{mu: make(chan struct{}, 1)} }

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	return s.buf.String()
}

// A refused receipt's reason reaches both the result and the log; the access token
// reaches neither.
func TestMarkRoomsReadFailureCarriesTheReasonAndLogsIt(t *testing.T) {
	t.Parallel()

	// Built at run time so no token-shaped literal sits in the source.
	token := "syt_" + strings.Repeat("S", 12) + "_" + strings.Repeat("z", 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errcode":"M_BAD_JSON","error":"Content must be a JSON object"}`))
	}))
	t.Cleanup(srv.Close)

	cache := testCache(t)
	if err := cache.SaveMessages(context.Background(), mx+"!dnd:x",
		[]domain.Message{{ID: mx + "$last", Timestamp: time.Unix(10, 0)}}); err != nil {
		t.Fatalf("SaveMessages: %v", err)
	}
	client, err := mautrix.NewClient(srv.URL, id.UserID("@me:x"), token)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	buf := newSyncBuffer()
	b := newBackend(cache)
	b.UseLogger(logging.New(buf, logging.Options{Level: slog.LevelDebug}))
	b.client = client
	b.attachLogger(client)

	got, err := b.MarkRoomsRead(context.Background(), []domain.RoomID{mx + "!dnd:x"}, false)
	if err != nil {
		t.Fatalf("MarkRoomsRead() error = %v", err)
	}
	if got.Failed != 1 || got.Marked != 0 {
		t.Errorf("MarkRoomsRead() = %+v, want one failure", got)
	}
	if !strings.Contains(got.FirstError, "M_BAD_JSON") {
		t.Errorf("FirstError = %q, want the homeserver's M_BAD_JSON", got.FirstError)
	}
	out := buf.String()
	for _, want := range []string{"level=WARN", "room=" + mx + "!dnd:x", "event=" + mx + "$last", "M_BAD_JSON"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, token) || strings.Contains(out, strings.Repeat("S", 12)) {
		t.Errorf("the access token reached the log:\n%s", out)
	}
}

// Without a logger the backend is silent, and attachLogger leaves mautrix's Nop.
func TestLoggerDefaultsAreSilent(t *testing.T) {
	t.Parallel()
	b := newBackend(nil)
	b.warnIf(context.Background(), context.Canceled, "nothing")
	b.UseLogger(nil)
	if b.logger != nil {
		t.Error("UseLogger(nil) installed a logger")
	}
	b.attachLogger(nil)
}

// A failing /sync is visible: the first failure and each doubling warn, the retries in
// between are debug, and recovery says how long the streak was.
func TestFailingSyncerLogsOutagesWithoutFlooding(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	b := newBackend(nil)
	b.UseLogger(logging.New(&buf, logging.Options{Level: slog.LevelInfo}))
	f := &failingSyncer{DefaultSyncer: mautrix.NewDefaultSyncer(), b: b}
	for range 5 {
		wait, err := f.OnFailedSync(nil, context.DeadlineExceeded)
		if err != nil || wait <= 0 {
			t.Fatalf("OnFailedSync = %v, %v; want a retry", wait, err)
		}
	}
	if got := strings.Count(buf.String(), `msg="sync failed"`); got != 3 { // 1, 2, 4
		t.Errorf("%d sync-failed warnings for 5 failures, want 3:\n%s", got, buf.String())
	}
	if err := f.ProcessResponse(context.Background(), &mautrix.RespSync{}, "s1"); err != nil {
		t.Fatalf("ProcessResponse: %v", err)
	}
	if !strings.Contains(buf.String(), "failures=5") {
		t.Errorf("recovery not logged:\n%s", buf.String())
	}
}
