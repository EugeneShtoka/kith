package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SetPeople replaces what source (one account: "whatsapp:<digits>", "telegram:<id>",
// a bridge login) names identifiers and which it says are one person. Of two names a
// source gives one identifier, the better ranked is kept.
func (c *Cache) SetPeople(ctx context.Context, source string, names []domain.PersonName, links []domain.PersonLink) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"person_names", "person_links"} {
			// #nosec G202 -- table is one of two constants.
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE source = ?", source); err != nil {
				return fmt.Errorf("db: clear what %s says of people: %w", source, err)
			}
		}
		if err := savePersonNames(ctx, tx, source, names); err != nil {
			return err
		}
		return savePersonLinks(ctx, tx, source, links)
	})
}

// savePersonNames adds what source calls identifiers.
func savePersonNames(ctx context.Context, tx *sql.Tx, source string, names []domain.PersonName) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO person_names(source, id, name, rank) VALUES(?, ?, ?, ?)
		ON CONFLICT(source, id) DO UPDATE SET name = excluded.name, rank = excluded.rank
		WHERE excluded.rank < person_names.rank`)
	if err != nil {
		return fmt.Errorf("db: prepare a person's name: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, n := range names {
		if n.ID == "" || n.Name == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, source, n.ID, n.Name, int(n.Rank)); err != nil {
			return fmt.Errorf("db: keep the name of %s: %w", n.ID, err)
		}
	}
	return nil
}

// savePersonLinks adds which identifiers source says are one person; a link to
// oneself, or to nothing, says nothing.
func savePersonLinks(ctx context.Context, tx *sql.Tx, source string, links []domain.PersonLink) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO person_links(source, id, other) VALUES(?, ?, ?)
		ON CONFLICT(source, id, other) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("db: prepare a link: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, l := range links {
		if l.ID == "" || l.Other == "" || l.ID == l.Other {
			continue
		}
		if _, err := stmt.ExecContext(ctx, source, l.ID, l.Other); err != nil {
			return fmt.Errorf("db: link %s and %s: %w", l.ID, l.Other, err)
		}
	}
	return nil
}

// Directory is who people are (domain.Directory), from every source's names and links,
// and the names bridges give their puppets: a puppet whose ID is a number
// (domain.PhoneOf) is named by the bridge as RankBridged, its tag (" (WA)") taken off.
func (c *Cache) Directory(ctx context.Context) (domain.Directory, error) {
	names, err := collect(ctx, c.db, "people's names", "SELECT source, id, name, rank FROM person_names",
		func(rows *sql.Rows) (domain.PersonName, error) {
			var n domain.PersonName
			err := rows.Scan(&n.Source, &n.ID, &n.Name, (*int)(&n.Rank))
			return n, err
		})
	if err != nil {
		return domain.Directory{}, err
	}
	links, err := collect(ctx, c.db, "links between people", "SELECT source, id, other FROM person_links",
		func(rows *sql.Rows) (domain.PersonLink, error) {
			var l domain.PersonLink
			scanErr := rows.Scan(&l.Source, &l.ID, &l.Other)
			return l, scanErr
		})
	if err != nil {
		return domain.Directory{}, err
	}
	puppets, err := collect(ctx, c.db, "bridged names",
		`SELECT DISTINCT user_id, display_name FROM room_members
		  WHERE display_name <> '' AND (user_id LIKE '@whatsapp%' OR user_id LIKE 'whatsapp:%')`,
		func(rows *sql.Rows) (domain.Member, error) {
			var m domain.Member
			scanErr := rows.Scan(&m.UserID, &m.DisplayName)
			return m, scanErr
		})
	if err != nil {
		return domain.Directory{}, err
	}
	for _, m := range puppets {
		phone, name := domain.PhoneOf(m.UserID), domain.WithoutBridgeTag(m.DisplayName)
		if phone == "" || name == "" {
			continue
		}
		names = append(names, domain.PersonName{Source: m.UserID, ID: domain.PhoneID(phone), Name: name, Rank: domain.RankBridged})
	}
	return domain.NewDirectory(names, links), nil
}

// PeopleSources is every source beginning with prefix that names or links people.
func (c *Cache) PeopleSources(ctx context.Context, prefix string) ([]string, error) {
	return collect(ctx, c.db, "people's sources", `
		SELECT source FROM person_names WHERE substr(source, 1, length(?1)) = ?1
		UNION SELECT source FROM person_links WHERE substr(source, 1, length(?1)) = ?1`,
		func(rows *sql.Rows) (string, error) {
			var s string
			err := rows.Scan(&s)
			return s, err
		}, prefix)
}

// ForgetPeople drops what every source beginning with prefix said of people, but
// those in keep: an account or bridge no longer read takes its names with it.
func (c *Cache) ForgetPeople(ctx context.Context, prefix string, keep []string) error {
	sources, err := c.PeopleSources(ctx, prefix)
	if err != nil {
		return err
	}
	for _, s := range sources {
		if slices.Contains(keep, s) {
			continue
		}
		if err := c.SetPeople(ctx, s, nil, nil); err != nil {
			return fmt.Errorf("db: forget what %s said of people: %w", s, err)
		}
	}
	return nil
}
