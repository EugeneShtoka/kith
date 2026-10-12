package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// tagsType is the room account data type for the tags filters moved a room into.
// Account data, not cache: a filter fires once, so a move lost on a rebuild would
// never be made again.
const tagsType = "org.kith.tags"

// tagsContent is one room's moves and releases, by lower-cased tag name.
type tagsContent struct {
	Verdicts map[string]tagEntry `json:"verdicts"`
}

type tagEntry struct {
	Rule     int    `json:"rule,omitempty"`
	Filter   string `json:"filter,omitempty"`
	At       int64  `json:"at_ms"`
	Released bool   `json:"released,omitempty"`
}

// verdicts is the content as the domain reads it.
func (c tagsContent) verdicts(roomID domain.RoomID) []domain.TagVerdict {
	out := make([]domain.TagVerdict, 0, len(c.Verdicts))
	for tag, e := range c.Verdicts {
		v := domain.TagVerdict{Room: roomID, Tag: tag, At: time.UnixMilli(e.At), Released: e.Released}
		if !e.Released {
			v.Rule, v.Filter = domain.MoveRule(e.Rule), e.Filter
		}
		out = append(out, v)
	}
	return out
}

// contentOf is a room's verdict set as account data carries it.
func contentOf(set []domain.TagVerdict) tagsContent {
	out := tagsContent{Verdicts: make(map[string]tagEntry, len(set))}
	for _, v := range set {
		e := tagEntry{At: v.At.UnixMilli(), Released: v.Released}
		if !v.Released {
			e.Rule, e.Filter = int(v.Rule), v.Filter
		}
		out.Verdicts[strings.ToLower(strings.TrimSpace(v.Tag))] = e
	}
	return out
}

// tagsFrom reports one room's tag account data in a sync, and whether it was present
// (absence is no change, not a release).
func tagsFrom(events []*event.Event) (content tagsContent, present bool) {
	for _, evt := range events {
		if evt == nil || evt.Type.Type != tagsType {
			continue
		}
		var parsed tagsContent
		if err := json.Unmarshal(evt.Content.VeryRaw, &parsed); err != nil {
			// Unparseable still counts as present; empty reads as no move.
			return tagsContent{}, true
		}
		content, present = parsed, true
	}
	return content, present
}

// MarkTag applies a move or a release to the room's set on the homeserver, then the
// mirror. The server copy is read first: a release made on another machine may not
// have synced yet, and a filter's move never overwrites one (domain.ErrReleased).
func (b *InProc) MarkTag(ctx context.Context, verdict domain.TagVerdict) error {
	// One room's set is read, changed and written back; two changes in this process must
	// not interleave and drop one. Another machine's write can still race (account data
	// has no compare-and-set): the later write wins.
	b.tagsMu.Lock()
	defer b.tagsMu.Unlock()
	if verdict.At.IsZero() {
		verdict.At = time.Now()
	}
	set, err := b.serverVerdicts(ctx, verdict.Room)
	if err != nil {
		return err
	}
	set, err = domain.WithVerdict(set, verdict)
	if err != nil {
		b.mirrorVerdicts(ctx, verdict.Room, set) // the release, if the mirror missed it
		return fmt.Errorf("matrix: %w", err)
	}
	return b.writeVerdicts(ctx, verdict.Room, set)
}

// RetagVerdicts moves tag from's verdicts to tag to on every room the mirror says holds
// one, or drops them when to is empty.
func (b *InProc) RetagVerdicts(ctx context.Context, from, to string) error {
	if b.cache == nil {
		return nil
	}
	b.tagsMu.Lock()
	defer b.tagsMu.Unlock()
	all, err := b.cache.TagVerdicts(ctx)
	if err != nil {
		return fmt.Errorf("matrix: read tag verdicts: %w", err)
	}
	var errs []error
	for _, room := range db.VerdictRooms(all, from) {
		set, err := b.serverVerdicts(ctx, room)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if retagged, changed := domain.Retagged(set, from, to); changed {
			errs = append(errs, b.writeVerdicts(ctx, room, retagged))
		}
	}
	return errors.Join(errs...)
}

// serverVerdicts is a room's verdict set as the homeserver has it; none yet is empty.
func (b *InProc) serverVerdicts(ctx context.Context, roomID domain.RoomID) ([]domain.TagVerdict, error) {
	var current tagsContent
	err := b.client.GetRoomAccountData(ctx, id.RoomID(roomID), tagsType, &current)
	switch {
	case errors.Is(err, mautrix.MNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("matrix: read the tag verdicts of %s: %w", roomID, err)
	}
	return current.verdicts(roomID), nil
}

// writeVerdicts stores a room's whole set on the homeserver, then the mirror.
func (b *InProc) writeVerdicts(ctx context.Context, roomID domain.RoomID, set []domain.TagVerdict) error {
	content := contentOf(set)
	if err := b.client.SetRoomAccountData(ctx, id.RoomID(roomID), tagsType, &content); err != nil {
		return fmt.Errorf("matrix: record the tag verdicts of %s: %w", roomID, err)
	}
	return toCache(b, "mirror the tag verdicts of "+string(roomID), func(c *db.Cache) error {
		return c.SetRoomVerdicts(ctx, roomID, content.verdicts(roomID))
	})
}

// mirrorVerdicts applies a room's set to the cache; best-effort, as the server copy is
// the record.
func (b *InProc) mirrorVerdicts(ctx context.Context, roomID domain.RoomID, set []domain.TagVerdict) {
	if b.cache == nil {
		return
	}
	b.warnIf(ctx, b.cache.SetRoomVerdicts(ctx, roomID, set), "mirror tag verdicts", "room", roomID)
}
