package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// starredType is the room account data holding a room's starred messages: it survives
// a cache rebuild, syncs across this account's machines, and (unlike a reaction) is
// invisible to the room. Namespaced, since Matrix standardizes nothing here.
const starredType = "org.kith.starred"

// starredContent is the whole set for one room (account data is a value, not a log).
type starredContent struct {
	Events []string `json:"events"`
}

// starredFrom reports one room's stars from a sync, and whether the type was present
// (absence is silence, not "no stars").
func starredFrom(events []*event.Event) (ids []domain.EventID, present bool) {
	for _, evt := range events {
		if evt == nil || evt.Type.Type != starredType {
			continue
		}
		// Our own type: decode raw. Unparseable still means present (an empty set).
		var content starredContent
		if err := json.Unmarshal(evt.Content.VeryRaw, &content); err != nil {
			return nil, true
		}
		ids = make([]domain.EventID, 0, len(content.Events))
		for _, raw := range content.Events {
			if raw != "" {
				ids = append(ids, domain.EventID(raw))
			}
		}
		present = true
	}
	return ids, present
}

// StarMessage adds or removes one message's star, writing the whole set back. The
// current set is read from the cache mirror (no round trip; last writer wins).
func (b *InProc) StarMessage(ctx context.Context, roomID domain.RoomID, eventID domain.EventID, on bool) error {
	if b.cache == nil {
		return fmt.Errorf("matrix: star %s: no cache to read the current set from", eventID)
	}
	have, err := b.cache.Starred(ctx, roomID)
	if err != nil {
		return fmt.Errorf("matrix: star %s: %w", eventID, err)
	}
	next := make([]string, 0, len(have)+1)
	found := false
	for _, id := range have {
		if id == eventID {
			found = true
			continue
		}
		next = append(next, string(id))
	}
	if on {
		if found {
			return nil // already starred
		}
		next = append(next, string(eventID))
	} else if !found {
		return nil // already unstarred
	}
	content := starredContent{Events: next}
	if err := b.client.SetRoomAccountData(ctx, id.RoomID(roomID), starredType, &content); err != nil {
		return fmt.Errorf("matrix: write stars for %s: %w", roomID, err)
	}
	// Mirror now so the mark appears immediately; the sync echo confirms or corrects.
	if err := b.cache.SetStarred(ctx, roomID, toEventIDs(next), time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("matrix: mirror stars for %s: %w", roomID, err)
	}
	return nil
}

// StarredIn is the room's starred set from the cache mirror.
func (b *InProc) StarredIn(ctx context.Context, roomID domain.RoomID) ([]domain.EventID, error) {
	return fromCache(b, "list starred in "+string(roomID), func(c *db.Cache) ([]domain.EventID, error) {
		return c.Starred(ctx, roomID)
	})
}

// toEventIDs converts raw IDs.
func toEventIDs(raw []string) []domain.EventID {
	out := make([]domain.EventID, 0, len(raw))
	for _, s := range raw {
		out = append(out, domain.EventID(s))
	}
	return out
}
