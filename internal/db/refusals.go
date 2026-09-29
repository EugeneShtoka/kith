package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Reaction refusals are learned from reactions a bridge took back; there is no
// capability API to ask.

// SaveReactionRefusal records that protocol refused emoji, last seen at.
func (c *Cache) SaveReactionRefusal(ctx context.Context, protocol, emoji string, at time.Time) error {
	if protocol == "" || emoji == "" {
		return nil
	}
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO reaction_refusals(protocol, emoji, at_ms) VALUES(?, ?, ?)
		ON CONFLICT(protocol, emoji) DO UPDATE SET at_ms = excluded.at_ms`,
		protocol, emoji, at.UnixMilli()); err != nil {
		return fmt.Errorf("db: record reaction refusal %s/%s: %w", protocol, emoji, err)
	}
	return nil
}

// ReactionRefusals is everything learned so far.
func (c *Cache) ReactionRefusals(ctx context.Context) ([]domain.ReactionRefusal, error) {
	return collect(ctx, c.db, "reaction refusals",
		"SELECT protocol, emoji, at_ms FROM reaction_refusals ORDER BY protocol, emoji",
		func(rows *sql.Rows) (domain.ReactionRefusal, error) {
			var protocol, emoji string
			var at int64
			err := rows.Scan(&protocol, &emoji, &at)
			return domain.ReactionRefusal{
				Protocol: protocol,
				Emoji:    emoji,
				At:       time.UnixMilli(at),
			}, err
		})
}
