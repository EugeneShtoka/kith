package db

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A draft whose mentions no longer parse is kept — the text is the user's — but the
// loss is logged, since it would send without notifying anyone it named.
func TestCorruptDraftMentionsAreLoggedNotLost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	var out bytes.Buffer
	cache.UseLogger(slog.New(slog.NewTextHandler(&out, nil)))
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
	if err := putDraft(ctx, cache, domain.StoredDraft{RoomID: "!a:x", Body: "private words"}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := cache.db.ExecContext(ctx, `UPDATE drafts SET mentions = '{not json' WHERE room_id = '!a:x'`); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	got, err := cache.Drafts(ctx)
	if err != nil || len(got) != 1 || got[0].Body != "private words" || got[0].Mentions != nil {
		t.Fatalf("Drafts() = %+v, %v; want the draft kept without mentions", got, err)
	}
	log := out.String()
	if !strings.Contains(log, "draft mentions unreadable") || !strings.Contains(log, "room=!a:x") {
		t.Errorf("log = %q, want the dropped mentions reported", log)
	}
	if strings.Contains(log, "private words") {
		t.Errorf("the draft's text reached the log: %q", log)
	}
}
