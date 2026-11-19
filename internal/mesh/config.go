package mesh

import (
	"bytes"
	"context"
	"crypto"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/mesh/crdt"
)

// WriteConfig writes each shared config key of writes (its TOML path, its value) as one
// op signed by signer, leaving out those the mesh holds at that value alone; none with
// nothing to write. One op, so a change is taken whole or not at all.
func (s *Store) WriteConfig(ctx context.Context, signer crypto.Signer, writes map[string][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	origin, err := devicekey.IDOf(signer.Public())
	if err != nil {
		return fmt.Errorf("mesh: %w", err)
	}
	var out []crdt.Write
	for _, path := range slices.Sorted(maps.Keys(writes)) {
		key := crdt.ConfigKey(path)
		if held := s.state.Entries[key]; len(held) == 1 && bytes.Equal(held[0].Value, writes[path]) {
			continue
		}
		out = append(out, crdt.Write{Key: key, Value: writes[path]})
	}
	if len(out) == 0 {
		return nil
	}
	return s.writeLocked(ctx, signer, origin, out)
}

// Config is the shared config the mesh holds: each key's applied value by its TOML
// path (the newest written), and each key whose concurrent values differ, its values
// as the register holds them, the applied one first. Equal values are no conflict.
func (s *Store) Config() (map[string][]byte, []domain.ConfigConflict) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roster, _ := s.state.Roster() // a roster that does not read names no bridge
	values := map[string][]byte{}
	var conflicts []domain.ConfigConflict
	for path, entries := range s.state.Config() {
		values[path] = entries[0].Value
		if !slices.ContainsFunc(entries, func(e crdt.Entry) bool { return !bytes.Equal(e.Value, entries[0].Value) }) {
			continue
		}
		c := domain.ConfigConflict{Path: path}
		for _, e := range entries {
			bridge := domain.Device{ID: e.Dot.Origin.String()}
			if m, ok := roster[e.Dot.Origin]; ok {
				bridge = deviceOf(e.Dot.Origin, &m)
			}
			c.Values = append(c.Values, domain.ConfigVersion{Value: string(e.Value), Bridge: bridge, At: e.Clock.Time()})
		}
		conflicts = append(conflicts, c)
	}
	slices.SortFunc(conflicts, func(a, b domain.ConfigConflict) int { return strings.Compare(a.Path, b.Path) })
	return values, conflicts
}

// maxRenderings bounds the renderings kept between hand edits: an editor buffer opened
// before the oldest kept and saved after reads its untouched keys as edits.
// shortcut: a count, not an age; keep them by time if a long-open buffer ever undoes a change.
const maxRenderings = 256

// Rendered is each config.toml kithd wrote from the mesh since a hand edit was last
// read, oldest first, and whether the store has just joined a mesh, so this bridge
// takes the mesh's config before it renders.
func (s *Store) Rendered(ctx context.Context) (renderings []string, joined bool, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM rendered ORDER BY n`)
	if err != nil {
		return nil, false, fmt.Errorf("mesh: read the config's renderings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, false, fmt.Errorf("mesh: read the config's renderings: %w", err)
		}
		if body == "" {
			joined = true
			continue
		}
		renderings = append(renderings, body)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("mesh: read the config's renderings: %w", err)
	}
	return renderings, joined, nil
}

// KeepRendered keeps body as the latest config.toml kithd wrote; fresh drops those
// before it, as a hand edit was read (or the mesh's config taken) first.
func (s *Store) KeepRendered(ctx context.Context, body string, fresh bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mesh: keep the config's rendering: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	if fresh {
		if _, err := tx.ExecContext(ctx, `DELETE FROM rendered`); err != nil {
			return fmt.Errorf("mesh: keep the config's rendering: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO rendered (body) VALUES (?)`, body); err != nil {
		return fmt.Errorf("mesh: keep the config's rendering: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rendered WHERE n <= (SELECT max(n) FROM rendered) - ?`, maxRenderings); err != nil {
		return fmt.Errorf("mesh: keep the config's rendering: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mesh: keep the config's rendering: %w", err)
	}
	return nil
}
