package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"maunium.net/go/mautrix/event"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// starredType is the room account data for a room's starred messages: it survives a
// cache rebuild, syncs across devices, and is invisible to the room. Matrix has no standard type.
const starredType = "org.kith.starred"

// starredContent is the whole set for one room.
type starredContent struct {
	Events []string `json:"events"`
}

// starredFrom reports one room's stars from a sync, and whether they were present
// (absence is no change, not "no stars").
func (x ids) starredFrom(events []*event.Event) (starred []domain.EventID, present bool) {
	for _, evt := range events {
		if evt == nil || evt.Type.Type != starredType {
			continue
		}
		// Our own type: decode raw. Unparseable is absent: the mirror keeps what it has,
		// since an empty set written back from it would erase every star.
		var content starredContent
		if err := json.Unmarshal(evt.Content.VeryRaw, &content); err != nil {
			continue
		}
		starred = make([]domain.EventID, 0, len(content.Events))
		for _, raw := range content.Events {
			if raw != "" {
				starred = append(starred, x.eventOf(raw))
			}
		}
		present = true
	}
	return starred, present
}

// StarMessage adds or removes one message's star, writing back the whole set read from
// the cache mirror (last writer wins).
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
		next = append(next, matrixString(b.ids.nativeEvent(id)))
	}
	if on {
		if found {
			return nil // already starred
		}
		next = append(next, matrixString(b.ids.nativeEvent(eventID)))
	} else if !found {
		return nil // already unstarred
	}
	content := starredContent{Events: next}
	if err := b.client.SetRoomAccountData(ctx, b.ids.nativeRoom(roomID), starredType, &content); err != nil {
		return fmt.Errorf("matrix: write stars for %s: %w", roomID, err)
	}
	// Mirror now so the mark appears at once; the sync echo corrects it.
	if err := b.cache.SetStarred(ctx, roomID, b.ids.toEventIDs(next), time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("matrix: mirror stars for %s: %w", roomID, err)
	}
	return nil
}

func (x ids) toEventIDs(raw []string) []domain.EventID {
	out := make([]domain.EventID, 0, len(raw))
	for _, s := range raw {
		out = append(out, x.eventOf(s))
	}
	return out
}
