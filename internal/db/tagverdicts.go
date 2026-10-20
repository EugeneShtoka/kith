package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SetRoomVerdicts replaces a room's tag verdicts and releases (mirroring account data,
// which carries the room's whole set). The room row comes first: account data can
// arrive before the room list, and a dropped verdict would never be caught again.
func (c *Cache) SetRoomVerdicts(ctx context.Context, roomID domain.RoomID, verdicts []domain.TagVerdict) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if err := registerRoom(ctx, tx, roomID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tag_verdicts WHERE room_id = ?`, string(roomID)); err != nil {
			return fmt.Errorf("db: clear the tag verdicts of %s: %w", roomID, err)
		}
		for _, v := range verdicts {
			if !v.In() && !v.Released {
				continue
			}
			rule, filter := int(v.Rule), v.Filter
			if v.Released {
				rule, filter = 0, ""
			}
			_, err := tx.ExecContext(ctx, `
				INSERT INTO tag_verdicts (room_id, tag, rule, filter, at_ms, released) VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (room_id, tag) DO UPDATE SET
					rule = excluded.rule, filter = excluded.filter, at_ms = excluded.at_ms, released = excluded.released`,
				string(roomID), verdictKey(v.Tag), rule, filter, v.At.UnixMilli(), boolInt(v.Released))
			if err != nil {
				return fmt.Errorf("db: record the %s verdict of %s: %w", v.Tag, roomID, err)
			}
		}
		return nil
	})
}

// TagVerdicts is every room's verdicts and releases, newest first.
func (c *Cache) TagVerdicts(ctx context.Context) ([]domain.TagVerdict, error) {
	return c.verdicts(ctx, `SELECT room_id, tag, rule, filter, at_ms, released FROM tag_verdicts
		ORDER BY at_ms DESC, room_id, tag`)
}

// RoomVerdicts is one room's verdicts and releases.
func (c *Cache) RoomVerdicts(ctx context.Context, roomID domain.RoomID) ([]domain.TagVerdict, error) {
	return c.verdicts(ctx, `SELECT room_id, tag, rule, filter, at_ms, released FROM tag_verdicts
		WHERE room_id = ? ORDER BY tag`, string(roomID))
}

func (c *Cache) verdicts(ctx context.Context, query string, args ...any) ([]domain.TagVerdict, error) {
	return collect(ctx, c.db, "tag_verdicts", query, func(rows *sql.Rows) (domain.TagVerdict, error) {
		var room, tag, filter string
		var rule, released int
		var at int64
		if err := rows.Scan(&room, &tag, &rule, &filter, &at, &released); err != nil {
			return domain.TagVerdict{}, err
		}
		return domain.TagVerdict{
			Room: domain.RoomID(room), Tag: tag, Rule: domain.MoveRule(rule), Filter: filter,
			At: time.UnixMilli(at), Released: released != 0,
		}, nil
	}, args...)
}

// verdictKey is how a tag is keyed in account data and the mirror: lower-cased.
func verdictKey(tag string) string { return strings.ToLower(strings.TrimSpace(tag)) }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// MarkTag applies one verdict to its room's set in the cache, for a network with
// nowhere else to keep it (domain.WithVerdict).
func (c *Cache) MarkTag(ctx context.Context, v domain.TagVerdict) error {
	set, err := c.RoomVerdicts(ctx, v.Room)
	if err != nil {
		return err
	}
	if set, err = domain.WithVerdict(set, v); err != nil {
		return fmt.Errorf("db: %w", err)
	}
	return c.SetRoomVerdicts(ctx, v.Room, set)
}

// RetagVerdicts moves tag from's verdicts to tag to in the cache, or drops them when to
// is empty: of every room and person but those on the networks in except, which keep
// their own (Matrix's are a mirror of the homeserver's).
func (c *Cache) RetagVerdicts(ctx context.Context, from, to string, except []domain.Protocol) error {
	all, err := c.TagVerdicts(ctx)
	if err != nil {
		return err
	}
	for _, room := range VerdictRooms(all, from) {
		if slices.Contains(except, domain.NetworkOf(string(room))) {
			continue
		}
		set, rerr := c.RoomVerdicts(ctx, room)
		if rerr != nil {
			return rerr
		}
		retagged, _ := domain.Retagged(set, from, to)
		if serr := c.SetRoomVerdicts(ctx, room, retagged); serr != nil {
			return serr
		}
	}
	people, err := c.PersonVerdicts(ctx)
	if err != nil {
		return err
	}
	var networks []domain.Protocol
	for _, v := range people {
		network := domain.NetworkOf(v.Person)
		if domain.SameTag(v.Tag, from) && !slices.Contains(except, network) && !slices.Contains(networks, network) {
			networks = append(networks, network)
		}
	}
	for _, network := range networks {
		if err := c.RetagPersonVerdicts(ctx, network, from, to); err != nil {
			return err
		}
	}
	return nil
}

// VerdictRooms is the rooms among verdicts with one on tag.
func VerdictRooms(verdicts []domain.TagVerdict, tag string) []domain.RoomID {
	var out []domain.RoomID
	for _, v := range verdicts {
		if domain.SameTag(v.Tag, tag) && !slices.Contains(out, v.Room) {
			out = append(out, v.Room)
		}
	}
	return out
}
