package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// PersonVerdicts is every person's marks and releases, newest first. A person's
// verdict on a tag applies on every account, so where several accounts hold one, the
// newest is it (a release, of two at the same time).
func (c *Cache) PersonVerdicts(ctx context.Context) ([]domain.PersonVerdict, error) {
	held, err := collect(ctx, c.db, "person_verdicts", `SELECT person, tag, rule, filter, at_ms, released
		FROM person_verdicts ORDER BY at_ms DESC, released DESC, person, tag, account`, scanPersonVerdict)
	if err != nil {
		return nil, err
	}
	type key struct{ person, tag string }
	seen := map[key]bool{}
	out := held[:0]
	for _, v := range held {
		if k := (key{v.Person, v.Tag}); !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	return out, nil
}

// accountVerdicts is the marks and releases account holds ("" those kept here alone).
func accountVerdicts(ctx context.Context, q querier, account string) ([]domain.PersonVerdict, error) {
	return collect(ctx, q, "person_verdicts", `SELECT person, tag, rule, filter, at_ms, released
		FROM person_verdicts WHERE account = ? ORDER BY at_ms DESC, person, tag`, scanPersonVerdict, account)
}

func scanPersonVerdict(rows *sql.Rows) (domain.PersonVerdict, error) {
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
}

// SetPersonVerdicts replaces the marks and releases account holds with set (mirroring
// its account data, which carries the whole set), then rebuilds who is hidden. Other
// accounts' are untouched, and so are other networks' people in set.
func (c *Cache) SetPersonVerdicts(ctx context.Context, account domain.RoomOwner, set []domain.PersonVerdict) error {
	network := domain.NetworkOf(string(account))
	err := c.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM person_verdicts WHERE account = ?`, string(account)); err != nil {
			return fmt.Errorf("db: clear the person verdicts of %s: %w", account, err)
		}
		for _, v := range set {
			if domain.NetworkOf(v.Person) != network {
				continue
			}
			if err := writePersonVerdict(ctx, tx, string(account), v); err != nil {
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
		found, err := optional(tx.QueryRowContext(ctx, `SELECT released FROM person_verdicts WHERE account = '' AND person = ? AND tag = ?`,
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
		return writePersonVerdict(ctx, tx, "", v)
	})
	if err != nil {
		return err
	}
	return c.rebuildHidden(ctx)
}

// writePersonVerdict replaces one person's entry for one tag in what account holds: a
// mark or a release is kept, anything else removes it.
func writePersonVerdict(ctx context.Context, tx *sql.Tx, account string, v domain.PersonVerdict) error {
	if !v.In() && !v.Released {
		_, err := tx.ExecContext(ctx, `DELETE FROM person_verdicts WHERE account = ? AND person = ? AND tag = ?`,
			account, v.Person, verdictKey(v.Tag))
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
		INSERT INTO person_verdicts (account, person, tag, rule, filter, at_ms, released) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (account, person, tag) DO UPDATE SET
			rule = excluded.rule, filter = excluded.filter, at_ms = excluded.at_ms, released = excluded.released`,
		account, v.Person, verdictKey(v.Tag), rule, filter, v.At.UnixMilli(), boolInt(v.Released))
	if err != nil {
		return fmt.Errorf("db: record the %s verdict of %s: %w", v.Tag, v.Person, err)
	}
	return nil
}

// RetagPersonVerdicts moves tag from's verdicts on network's people kept here to tag
// to, or drops them when to is empty.
func (c *Cache) RetagPersonVerdicts(ctx context.Context, network domain.Protocol, from, to string) error {
	changed := false
	err := c.inTx(ctx, func(tx *sql.Tx) error {
		held, err := accountVerdicts(ctx, tx, "")
		if err != nil {
			return err
		}
		held = slices.DeleteFunc(held, func(v domain.PersonVerdict) bool { return domain.NetworkOf(v.Person) != network })
		var retagged []domain.PersonVerdict
		if retagged, changed = domain.RetaggedPeople(held, from, to); !changed {
			return nil
		}
		for _, v := range held {
			if _, err := tx.ExecContext(ctx, `DELETE FROM person_verdicts WHERE account = '' AND person = ? AND tag = ?`, v.Person, v.Tag); err != nil {
				return fmt.Errorf("db: clear the verdicts of %s: %w", v.Person, err)
			}
		}
		for _, v := range retagged {
			if err := writePersonVerdict(ctx, tx, "", v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || !changed {
		return err
	}
	return c.rebuildHidden(ctx)
}
