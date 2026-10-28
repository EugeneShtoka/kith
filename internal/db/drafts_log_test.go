package db

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A draft with unparsable mentions is kept and the loss logged.
func TestCorruptDraftMentionsAreLoggedNotLost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cache := openTemp(t)
	var out bytes.Buffer
	cache.UseLogger(slog.New(slog.NewTextHandler(&out, nil)))
	if err := cache.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "Alpha"}}); err != nil {
		t.Fatalf("SaveRooms: %v", err)
	}
	if err := putDraft(ctx, cache, domain.StoredDraft{RoomID: mx + "!a:x", Body: "private words"}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := cache.db.ExecContext(ctx, `UPDATE drafts SET mentions = '{not json' WHERE room_id = 'matrix:0123456789abcdef/!a:x'`); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	got, err := cache.Drafts(ctx)
	if err != nil || len(got) != 1 || got[0].Body != "private words" || got[0].Mentions != nil {
		t.Fatalf("Drafts() = %+v, %v; want the draft kept without mentions", got, err)
	}
	log := out.String()
	if !strings.Contains(log, "room="+mx+"!a:x") {
		t.Errorf("log = %q, want the dropped mentions reported", log)
	}
	if strings.Contains(log, "private words") {
		t.Errorf("the draft's text reached the log: %q", log)
	}
}
