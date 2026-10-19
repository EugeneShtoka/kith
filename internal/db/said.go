package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// saidOrder is newestFirstIn with the room last: the page runs across rooms, so two
// rooms' messages must never tie.
const saidOrder = " m.ts_ms DESC, COALESCE(o.seq, 0) DESC, length(m.event_id) DESC, m.event_id DESC, m.room_id DESC"

// MessagesFrom is one page of what req.Senders said in req.Rooms, before req.Before,
// oldest first. Hidden senders are included: the person was asked for by name.
func (c *Cache) MessagesFrom(ctx context.Context, req domain.SaidRequest) ([]domain.Message, error) {
	if len(req.Senders) == 0 || req.Rooms.None() || req.Limit <= 0 {
		return nil, nil
	}
	senders, err := c.withTheirNumbers(ctx, req.Senders)
	if err != nil {
		return nil, err
	}
	// The page's keys first, by the sender index; then each room's messages whole.
	in, args := inIDs(nil, senders)
	// #nosec G202 -- inIDs emits only placeholders; every sender and room is bound.
	query := `SELECT m.room_id, m.event_id FROM messages m
		LEFT JOIN message_order o ON o.room_id = m.room_id AND o.event_id = m.event_id
		WHERE m.sender` + in
	if !req.Rooms.All {
		var rooms string
		rooms, args = inIDs(args, req.Rooms.IDs)
		query += " AND m.room_id" + rooms
	}
	if before := req.Before; before.ID != "" {
		query += ` AND (m.ts_ms, COALESCE(o.seq, 0), length(m.event_id), m.event_id, m.room_id) < (?, ?, length(?), ?, ?)`
		args = append(args, before.Timestamp.UnixMilli(), before.Seq, string(before.ID), string(before.ID), string(before.RoomID))
	}
	keys, err := collect(ctx, c.db, "messages from", query+" ORDER BY"+saidOrder+" LIMIT ?",
		func(rows *sql.Rows) (domain.Message, error) {
			var room, event string
			scanErr := rows.Scan(&room, &event)
			return domain.Message{RoomID: domain.RoomID(room), ID: domain.EventID(event)}, scanErr
		}, append(args, req.Limit)...)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	byRoom := map[domain.RoomID][]domain.EventID{}
	for i := range keys {
		byRoom[keys[i].RoomID] = append(byRoom[keys[i].RoomID], keys[i].ID)
	}
	type key struct {
		room  domain.RoomID
		event domain.EventID
	}
	found := make(map[key]domain.Message, len(keys))
	for room, events := range byRoom {
		in, args := inIDs([]any{string(room)}, events)
		// #nosec G202 -- inIDs emits only placeholders; every event ID is bound.
		msgs, err := collect(ctx, c.db, "messages from", messageSelect+` WHERE m.room_id = ? AND m.event_id`+in,
			scanMessage(room), args...)
		if err != nil {
			return nil, err
		}
		for i := range msgs {
			found[key{room, msgs[i].ID}] = msgs[i]
		}
	}
	// In the page's order, less any message gone between the two reads.
	out := make([]domain.Message, 0, len(keys))
	for i := range slices.Backward(keys) {
		if msg, ok := found[key{keys[i].RoomID, keys[i].ID}]; ok {
			out = append(out, msg)
		}
	}
	return out, nil
}

// withTheirNumbers adds to ids every cached sender whose phone number is one of theirs
// (a tel: ID among them): an account the directory never listed is still theirs by its
// number, as domain.Directory counts it when it hides them.
func (c *Cache) withTheirNumbers(ctx context.Context, ids []string) ([]string, error) {
	numbers := map[string]bool{}
	for _, id := range ids {
		if strings.HasPrefix(id, domain.PhoneID("")) {
			numbers[id] = true
		}
	}
	if len(numbers) == 0 {
		return ids, nil
	}
	senders, err := collect(ctx, c.db, "senders", "SELECT DISTINCT sender FROM messages WHERE sender <> ''",
		func(rows *sql.Rows) (string, error) {
			var s string
			err := rows.Scan(&s)
			return s, err
		})
	if err != nil {
		return nil, err
	}
	out := slices.Clone(ids)
	for _, s := range senders {
		if digits := domain.PhoneOf(s); digits != "" && numbers[domain.PhoneID(digits)] && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// MessageCounts is how many cached messages each person wrote, by their IDs and the
// accounts their numbers reach, as MessagesFrom reads them.
func (c *Cache) MessageCounts(ctx context.Context, people [][]string) ([]int, error) {
	out := make([]int, len(people))
	for i, ids := range people {
		if len(ids) == 0 {
			continue
		}
		senders, err := c.withTheirNumbers(ctx, ids)
		if err != nil {
			return nil, err
		}
		in, args := inIDs(nil, senders)
		// #nosec G202 -- inIDs emits only placeholders; every sender is bound.
		if err := c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE sender"+in, args...).Scan(&out[i]); err != nil {
			return nil, fmt.Errorf("db: count messages from %v: %w", ids, err)
		}
	}
	return out, nil
}
