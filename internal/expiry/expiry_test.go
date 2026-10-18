package expiry_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/expiry"
)

// due is a cache with what has run out, and asks it saw.
type due struct {
	gone  []domain.Message
	err   error
	asked []bool
}

func (d *due) ExpireDue(_ context.Context, _ domain.RoomOwner, _ time.Time, keep bool) ([]domain.Message, error) {
	d.asked = append(d.asked, keep)
	return d.gone, d.err
}

// What expired reaches clients as a deletion: what it said and its file only under
// keep; each room touched is changed once; a cache that fails is logged.
func TestWhatExpiredIsToldAsADeletion(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	photo := &domain.Media{Type: domain.MediaImage, Name: "photo"}
	cache := &due{gone: []domain.Message{
		{ID: "a/1", RoomID: "a", Body: "one", Media: photo, Redacted: true, RedactedAt: at},
		{ID: "a/2", RoomID: "a", Body: "two", Redacted: true, RedactedAt: at},
		{ID: "b/1", RoomID: "b", Body: "three", Redacted: true, RedactedAt: at},
	}, err: errors.New("disk full")}
	for _, keep := range []bool{false, true} {
		var told []domain.Message
		changed := map[domain.RoomID]int{}
		var log bytes.Buffer
		expiry.Sweeper{
			Cache: cache, Rooms: "signal:", Keep: func() bool { return keep },
			Gone: func(m domain.Message) { told = append(told, m) }, Changed: func(r domain.RoomID) { changed[r]++ },
			Log: slog.New(slog.NewTextHandler(&log, nil)),
		}.Sweep(context.Background(), at)
		if len(told) != 3 {
			t.Fatalf("keep=%v: told %d", keep, len(told))
		}
		for _, m := range told {
			if !m.Redacted || !m.RedactedAt.Equal(at) || (m.Body != "") != keep {
				t.Errorf("keep=%v: told %+v", keep, m)
			}
		}
		if (told[0].Media != nil) != keep {
			t.Errorf("keep=%v: the photo told as %+v", keep, told[0].Media)
		}
		if changed["a"] != 1 || changed["b"] != 1 {
			t.Errorf("keep=%v: rooms changed %v, want each once", keep, changed)
		}
		if !strings.Contains(log.String(), "disk full") {
			t.Errorf("keep=%v: the cache's failure was not logged: %q", keep, log.String())
		}
	}
	if len(cache.asked) != 2 || cache.asked[0] || !cache.asked[1] {
		t.Errorf("asked the cache with keep %v", cache.asked)
	}
}

// Run sweeps at once, then until its context ends.
func TestRunSweepsUntilStopped(t *testing.T) {
	t.Parallel()
	cache := &due{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		expiry.Sweeper{Cache: cache, Keep: func() bool { return false }, Log: slog.Default()}.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}
