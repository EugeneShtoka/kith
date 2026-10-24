package db

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Every per-room writer works against a room the cache has never listed.
func TestPerRoomWritersRegisterTheRoomFirst(t *testing.T) {
	t.Parallel()

	const room = domain.RoomID(mx + "!never-listed:example.org")
	now := time.Now()

	for _, tc := range []struct {
		name  string
		write func(context.Context, *Cache) error
		check func(context.Context, *testing.T, *Cache)
	}{
		{
			name: "SetRoomVerdicts",
			write: func(ctx context.Context, c *Cache) error {
				return c.SetRoomVerdicts(ctx, room, []domain.TagVerdict{
					{Room: room, Tag: "Spam", Rule: domain.MoveAlways, Filter: "test", At: now},
				})
			},
			check: func(ctx context.Context, t *testing.T, c *Cache) {
				verdicts, err := c.TagVerdicts(ctx)
				if err != nil {
					t.Fatalf("TagVerdicts: %v", err)
				}
				if len(verdicts) != 1 || verdicts[0].Room != room {
					t.Errorf("TagVerdicts = %v, want the one verdict just written", verdicts)
				}
			},
		},
		{
			name: "SetStarred",
			write: func(ctx context.Context, c *Cache) error {
				return c.SetStarred(ctx, room, []domain.EventID{mx + "$e"}, now.UnixMilli())
			},
			check: func(ctx context.Context, t *testing.T, c *Cache) {
				ids, err := c.Starred(ctx, room)
				if err != nil {
					t.Fatalf("Starred: %v", err)
				}
				if len(ids) != 1 || ids[0] != mx+"$e" {
					t.Errorf("Starred = %v, want the one star just written", ids)
				}
			},
		},
		{
			name: "SaveDraft",
			write: func(ctx context.Context, c *Cache) error {
				return putDraft(ctx, c, domain.StoredDraft{
					RoomID: room, Body: "half a sentence", Updated: now,
				})
			},
			check: func(ctx context.Context, t *testing.T, c *Cache) {
				drafts, err := c.Drafts(ctx)
				if err != nil {
					t.Fatalf("Drafts: %v", err)
				}
				if len(drafts) != 1 || drafts[0].Body != "half a sentence" {
					t.Errorf("Drafts = %v, want the one draft just written", drafts)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			cache := openTemp(t)

			if err := tc.write(ctx, cache); err != nil {
				t.Fatalf("%s against an unlisted room: %v\n"+
					"The table REFERENCES rooms(id) — call registerRoom in the same "+
					"transaction, the way the other eleven per-room writers do.", tc.name, err)
			}
			tc.check(ctx, t, cache)
		})
	}
}
