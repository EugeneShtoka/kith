package db

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// SetNumberNames replaces what source (one account: "whatsapp:<digits>",
// "telegram:<id>") knows numbers by with names.
func (c *Cache) SetNumberNames(ctx context.Context, source string, names []domain.NumberName) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM phone_names WHERE source = ?", source); err != nil {
			return fmt.Errorf("db: clear the numbers %s names: %w", source, err)
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO phone_names(source, phone, name, rank) VALUES(?, ?, ?, ?)
			ON CONFLICT(source, phone) DO UPDATE SET name = excluded.name, rank = excluded.rank
			WHERE excluded.rank < phone_names.rank`)
		if err != nil {
			return fmt.Errorf("db: prepare a number's name: %w", err)
		}
		defer func() { _ = stmt.Close() }()
		for _, n := range names {
			if n.Phone == "" || n.Name == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, source, n.Phone, n.Name, int(n.Rank)); err != nil {
				return fmt.Errorf("db: keep the name of %s: %w", n.Phone, err)
			}
		}
		return nil
	})
}

// PhoneBook is the best name for every number any account or bridge knows: the lowest
// rank, and among names of one rank the first source's, then the first name, so the
// same names always give the same book. A bridge's name for a person whose ID is
// their number (domain.PhoneOf) counts as RankBridged, its tag (" (WA)") taken off; a
// name that is itself a number names nobody.
func (c *Cache) PhoneBook(ctx context.Context) (domain.PhoneBook, error) {
	type known struct {
		domain.NumberName
		source string
	}
	saved, err := collect(ctx, c.db, "phone names", "SELECT source, phone, name, rank FROM phone_names",
		func(rows *sql.Rows) (known, error) {
			var k known
			var rank int
			err := rows.Scan(&k.source, &k.Phone, &k.Name, &rank)
			k.Rank = domain.NameRank(rank)
			return k, err
		})
	if err != nil {
		return nil, err
	}
	members, err := collect(ctx, c.db, "bridged names",
		`SELECT DISTINCT user_id, display_name FROM room_members
		  WHERE display_name <> '' AND (user_id LIKE '@whatsapp%' OR user_id LIKE 'whatsapp:%')`,
		func(rows *sql.Rows) (domain.Member, error) {
			var m domain.Member
			err := rows.Scan(&m.UserID, &m.DisplayName)
			return m, err
		})
	if err != nil {
		return nil, err
	}
	all := saved
	for _, m := range members {
		phone, name := domain.PhoneOf(m.UserID), domain.WithoutBridgeTag(m.DisplayName)
		if phone == "" || name == "" {
			continue
		}
		all = append(all, known{NumberName: domain.NumberName{Phone: phone, Name: name, Rank: domain.RankBridged}, source: m.UserID})
	}
	slices.SortFunc(all, func(a, b known) int {
		return cmp.Or(cmp.Compare(a.Phone, b.Phone), cmp.Compare(a.Rank, b.Rank),
			cmp.Compare(a.source, b.source), cmp.Compare(a.Name, b.Name))
	})
	book := domain.PhoneBook{}
	for _, k := range all {
		if _, named := book[k.Phone]; named {
			continue
		}
		if _, isNumber := domain.PhoneIn(k.Name); isNumber {
			continue
		}
		book[k.Phone] = k.Name
	}
	return book, nil
}
