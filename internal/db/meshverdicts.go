package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// KeptPersonVerdicts is the marks and releases kept here alone: those of every network
// with nowhere else to keep them.
func (c *Cache) KeptPersonVerdicts(ctx context.Context) ([]domain.PersonVerdict, error) {
	return accountVerdicts(ctx, c.db, "")
}

// KeepVerdicts keeps the verdicts another bridge made, as the mesh carries them, of the
// networks with nowhere else to keep them: each that wins over the cache's own
// (domain.TagVerdict.Beats), checked in the transaction that writes it, so a verdict
// made here meanwhile is not written over by one it wins over. A cleared one removes
// the cache's. A room's is kept only for a room the cache holds. changed reports
// whether any was kept.
func (c *Cache) KeepVerdicts(ctx context.Context, tags []domain.TagVerdict, people []domain.PersonVerdict) (changed bool, err error) {
	marked := false
	err = c.inTx(ctx, func(tx *sql.Tx) error {
		for _, v := range tags {
			kept, kerr := keepTagVerdict(ctx, tx, v)
			if kerr != nil {
				return kerr
			}
			changed = changed || kept
		}
		for _, v := range people {
			kept, kerr := keepPersonVerdict(ctx, tx, v)
			if kerr != nil {
				return kerr
			}
			marked = marked || kept
		}
		return nil
	})
	if err != nil || !marked {
		return changed, err
	}
	// A mark can hide someone, and a release show them again.
	return true, c.rebuildHidden(ctx)
}

// keepTagVerdict keeps v unless the cache does not hold its room or holds a verdict
// that is the same or wins over it.
func keepTagVerdict(ctx context.Context, tx *sql.Tx, v domain.TagVerdict) (bool, error) {
	var held bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM rooms WHERE id = ?)`, string(v.Room)).Scan(&held); err != nil {
		return false, fmt.Errorf("db: look for %s: %w", v.Room, err)
	}
	if !held {
		return false, nil
	}
	var rule, released int
	var filter string
	var at int64
	found, err := optional(tx.QueryRowContext(ctx, `SELECT rule, filter, at_ms, released FROM tag_verdicts WHERE room_id = ? AND tag = ?`,
		string(v.Room), verdictKey(v.Tag)).Scan(&rule, &filter, &at, &released))
	if err != nil {
		return false, fmt.Errorf("db: read the %s verdict of %s: %w", v.Tag, v.Room, err)
	}
	cur := domain.TagVerdict{Rule: domain.MoveRule(rule), Filter: filter, At: time.UnixMilli(at), Released: released != 0}
	switch {
	case found && (cur.Same(v) || !v.Beats(cur)), !found && !v.In() && !v.Released:
		return false, nil
	case !v.In() && !v.Released:
		if _, err := tx.ExecContext(ctx, `DELETE FROM tag_verdicts WHERE room_id = ? AND tag = ?`, string(v.Room), verdictKey(v.Tag)); err != nil {
			return false, fmt.Errorf("db: clear the %s verdict of %s: %w", v.Tag, v.Room, err)
		}
		return true, nil
	}
	return true, writeTagVerdict(ctx, tx, v)
}

// keepPersonVerdict keeps v unless the cache holds a verdict kept here alone that is
// the same or wins over it.
func keepPersonVerdict(ctx context.Context, tx *sql.Tx, v domain.PersonVerdict) (bool, error) {
	var rule, released int
	var filter string
	var at int64
	found, err := optional(tx.QueryRowContext(ctx, `SELECT rule, filter, at_ms, released FROM person_verdicts WHERE account = '' AND person = ? AND tag = ?`,
		v.Person, verdictKey(v.Tag)).Scan(&rule, &filter, &at, &released))
	if err != nil {
		return false, fmt.Errorf("db: read the %s verdict of %s: %w", v.Tag, v.Person, err)
	}
	cur := domain.PersonVerdict{Rule: domain.MoveRule(rule), Filter: filter, At: time.UnixMilli(at), Released: released != 0}
	if found && (cur.Same(v) || !v.Beats(cur)) || !found && !v.In() && !v.Released {
		return false, nil
	}
	return true, writePersonVerdict(ctx, tx, "", v)
}
