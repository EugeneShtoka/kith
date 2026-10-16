package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// PersonVerdicts is every person's marks and releases, newest first.
func (c *Cache) PersonVerdicts(ctx context.Context) ([]domain.PersonVerdict, error) {
	return c.personVerdicts(ctx, c.db)
}

func (c *Cache) personVerdicts(ctx context.Context, q querier) ([]domain.PersonVerdict, error) {
	return collect(ctx, q, "person_verdicts", `SELECT person, tag, rule, filter, at_ms, released
		FROM person_verdicts ORDER BY at_ms DESC, person, tag`,
		func(rows *sql.Rows) (domain.PersonVerdict, error) {
			var person, tag, filter string
			var rule, released int
			var at int64
			if err := rows.Scan(&person, &tag, &rule, &filter, &at, &released); err != nil {
				return domain.PersonVerdict{}, err
			}
			return domain.PersonVerdict{
				Person: person, Tag: tag, Rule: domain.MoveRule(rule), Filter: filter,
				At: time.UnixMilli(at), Released: released != 0,
			}, nil
		})
}

// SetPersonVerdicts replaces the marks and releases of every person on network with set
// (mirroring account data, which carries a network's whole set), then rebuilds who is
// hidden.
func (c *Cache) SetPersonVerdicts(ctx context.Context, network domain.Protocol, set []domain.PersonVerdict) error {
	err := c.inTx(ctx, func(tx *sql.Tx) error {
		held, err := c.personVerdicts(ctx, tx)
		if err != nil {
			return err
		}
		for _, v := range held {
			if domain.NetworkOf(v.Person) != network {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM person_verdicts WHERE person = ? AND tag = ?`, v.Person, v.Tag); err != nil {
				return fmt.Errorf("db: clear the verdicts of %s: %w", v.Person, err)
			}
		}
		for _, v := range set {
			if domain.NetworkOf(v.Person) != network {
				continue
			}
			if err := writePersonVerdict(ctx, tx, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// A mark can hide someone, and a release show them again.
	return c.rebuildHidden(ctx)
}

// MarkPerson applies one mark or release to the cache, for a network with nowhere else
// to keep it (domain.WithPersonVerdict), in one transaction so marks made together keep
// each other.
func (c *Cache) MarkPerson(ctx context.Context, v domain.PersonVerdict) error {
	err := c.inTx(ctx, func(tx *sql.Tx) error {
		var released int
		found, err := optional(tx.QueryRowContext(ctx, `SELECT released FROM person_verdicts WHERE person = ? AND tag = ?`,
			v.Person, verdictKey(v.Tag)).Scan(&released))
		if err != nil {
			return fmt.Errorf("db: read the %s verdict of %s: %w", v.Tag, v.Person, err)
		}
		var held []domain.PersonVerdict
		if found {
			held = []domain.PersonVerdict{{Person: v.Person, Tag: v.Tag, Rule: domain.MoveAlways, Released: released != 0}}
		}
		if _, err := domain.WithPersonVerdict(held, v); err != nil {
			return fmt.Errorf("db: %w", err)
		}
		return writePersonVerdict(ctx, tx, v)
	})
	if err != nil {
		return err
	}
	return c.rebuildHidden(ctx)
}

// writePersonVerdict replaces one person's entry for one tag: a mark or a release is
// kept, anything else removes it.
func writePersonVerdict(ctx context.Context, tx *sql.Tx, v domain.PersonVerdict) error {
	if !v.In() && !v.Released {
		_, err := tx.ExecContext(ctx, `DELETE FROM person_verdicts WHERE person = ? AND tag = ?`, v.Person, verdictKey(v.Tag))
		if err != nil {
			return fmt.Errorf("db: clear the %s verdict of %s: %w", v.Tag, v.Person, err)
		}
		return nil
	}
	rule, filter := int(v.Rule), v.Filter
	if v.Released {
		rule, filter = 0, ""
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO person_verdicts (person, tag, rule, filter, at_ms, released) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (person, tag) DO UPDATE SET
			rule = excluded.rule, filter = excluded.filter, at_ms = excluded.at_ms, released = excluded.released`,
		v.Person, verdictKey(v.Tag), rule, filter, v.At.UnixMilli(), boolInt(v.Released))
	if err != nil {
		return fmt.Errorf("db: record the %s verdict of %s: %w", v.Tag, v.Person, err)
	}
	return nil
}

// RetagPersonVerdicts moves tag from's verdicts on network's people to tag to, or drops
// them when to is empty.
func (c *Cache) RetagPersonVerdicts(ctx context.Context, network domain.Protocol, from, to string) error {
	all, err := c.PersonVerdicts(ctx)
	if err != nil {
		return err
	}
	retagged, changed := domain.RetaggedPeople(all, from, to)
	if !changed {
		return nil
	}
	return c.SetPersonVerdicts(ctx, network, retagged)
}
