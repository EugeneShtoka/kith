package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Hidden people are the senders the muted tags' people lists name, matched across
// every ID the directory and the identity groups link to them (domain.HiddenPeople).
// Search leaves them out, and direct replies to them. SQL cannot ask the matcher, so
// the cache keeps the list of senders it holds that are hidden: rebuilt when the marks
// or the directory change, extended as senders are written.

type hiddenState struct {
	// rebuilding holds rebuilds to one at a time, so the last to start lands last.
	rebuilding sync.Mutex

	mu     sync.Mutex
	marked []string
	groups [][]string
	people domain.HiddenPeople
	// seen is every sender written or found, true for the hidden; ids are those.
	seen map[string]bool
	ids  []string
	// changed hears the rooms a rebuild changed (OnHiddenChange).
	changed func(context.Context, []domain.RoomID)
}

// OnHiddenChange has fn called after a rebuild changes which senders are hidden, with
// the rooms they wrote in, so counts kept elsewhere can be taken again. Senders first
// written later are not a change: whoever writes them counts their room anew.
func (c *Cache) OnHiddenChange(fn func(context.Context, []domain.RoomID)) {
	c.hidden.mu.Lock()
	defer c.hidden.mu.Unlock()
	c.hidden.changed = fn
}

// UseHidden sets the people the muted tags hide and the identity groups that link
// IDs, and rebuilds the list of hidden senders. Nobody is hidden until it is called.
func (c *Cache) UseHidden(ctx context.Context, marked []string, groups [][]string) error {
	c.hidden.mu.Lock()
	c.hidden.marked, c.hidden.groups = slices.Clone(marked), slices.Clone(groups)
	c.hidden.mu.Unlock()
	return c.rebuildHidden(ctx)
}

// rebuildHidden builds the matcher over the current directory and classifies every
// sender the cache holds, and every sender written meanwhile.
func (c *Cache) rebuildHidden(ctx context.Context) error {
	c.hidden.rebuilding.Lock()
	defer c.hidden.rebuilding.Unlock()
	c.hidden.mu.Lock()
	marked, groups := c.hidden.marked, c.hidden.groups
	c.hidden.mu.Unlock()
	var people domain.HiddenPeople
	var senders []string
	if len(marked) > 0 {
		dir, err := c.Directory(ctx)
		if err != nil {
			return err
		}
		people = domain.NewHiddenPeople(marked, groups, dir, c.mine())
		senders, err = collect(ctx, c.db, "senders", "SELECT DISTINCT sender FROM messages WHERE sender <> ''",
			func(rows *sql.Rows) (string, error) {
				var s string
				scanErr := rows.Scan(&s)
				return s, scanErr
			})
		if err != nil {
			return fmt.Errorf("db: classify senders: %w", err)
		}
	}
	c.hidden.mu.Lock()
	// Senders noted while the scan ran are in seen, under the old matcher.
	all := slices.Concat(senders, slices.Collect(maps.Keys(c.hidden.seen)))
	before := c.hidden.ids
	c.hidden.people, c.hidden.seen, c.hidden.ids = people, map[string]bool{}, nil
	c.classify(all)
	moved := symmetricDifference(before, c.hidden.ids)
	changed := c.hidden.changed
	c.hidden.mu.Unlock()
	if len(moved) == 0 || changed == nil {
		return nil
	}
	// Replies to them are in the rooms they wrote in.
	where, args := inIDs(nil, moved)
	// #nosec G202 -- inIDs emits only placeholders; every sender is bound.
	rooms, err := collect(ctx, c.db, "rooms of newly hidden senders", "SELECT DISTINCT room_id FROM messages WHERE sender"+where,
		func(rows *sql.Rows) (domain.RoomID, error) {
			var r string
			scanErr := rows.Scan(&r)
			return domain.RoomID(r), scanErr
		}, args...)
	if err != nil {
		return err
	}
	if len(rooms) > 0 {
		changed(ctx, rooms)
	}
	return nil
}

// symmetricDifference is the IDs in exactly one of a and b.
func symmetricDifference(a, b []string) []string {
	var out []string
	for _, id := range a {
		if !slices.Contains(b, id) {
			out = append(out, id)
		}
	}
	for _, id := range b {
		if !slices.Contains(a, id) {
			out = append(out, id)
		}
	}
	return out
}

// noteSenders classifies the senders of msgs not seen before. It reads no table, so
// it runs outside a write's transaction.
func (c *Cache) noteSenders(msgs []domain.Message) {
	senders := make([]string, 0, len(msgs))
	for i := range msgs {
		senders = append(senders, msgs[i].Sender)
	}
	c.hidden.mu.Lock()
	defer c.hidden.mu.Unlock()
	c.classify(senders)
}

// classify records each new sender, hidden or not. Call with hidden.mu held.
func (c *Cache) classify(senders []string) {
	if c.hidden.seen == nil {
		c.hidden.seen = map[string]bool{}
	}
	for _, s := range senders {
		if _, done := c.hidden.seen[s]; done || s == "" {
			continue
		}
		hidden := c.hidden.people.Has(s)
		c.hidden.seen[s] = hidden
		if hidden {
			c.hidden.ids = append(c.hidden.ids, s)
		}
	}
}

// hiddenSenders is the senders the cache holds that are hidden, never one of yours.
func (c *Cache) hiddenSenders() []string {
	mine := c.mine()
	c.hidden.mu.Lock()
	defer c.hidden.mu.Unlock()
	out := make([]string, 0, len(c.hidden.ids))
	for _, id := range c.hidden.ids {
		if !slices.Contains(mine, id) {
			out = append(out, id)
		}
	}
	return out
}

// hides reports whether sender is hidden, asking the matcher (a sender not yet
// written counts too).
func (c *Cache) hides(sender string) bool {
	if sender == "" || slices.Contains(c.mine(), sender) {
		return false
	}
	c.hidden.mu.Lock()
	defer c.hidden.mu.Unlock()
	return c.hidden.people.Has(sender)
}

// Hides reports whether msg is hidden: its sender is, or it answers a message whose
// sender is.
func (c *Cache) Hides(ctx context.Context, msg domain.Message) (bool, error) {
	if c.hides(msg.Sender) {
		return true, nil
	}
	if msg.ReplyTo == "" {
		return false, nil
	}
	c.hidden.mu.Lock()
	empty := c.hidden.people.Empty()
	c.hidden.mu.Unlock()
	if empty {
		return false, nil
	}
	var sender string
	err := c.db.QueryRowContext(ctx, "SELECT sender FROM messages WHERE room_id = ? AND event_id = ?",
		string(msg.RoomID), string(msg.ReplyTo)).Scan(&sender)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db: read whom a reply answers: %w", err)
	}
	return c.hides(sender), nil
}

// notHidden is the AND condition leaving out hidden senders' messages and direct
// replies to them, appending its values to args; "" when nobody is hidden.
func notHidden(hidden []string, args []any) (string, []any) {
	if len(hidden) == 0 {
		return "", args
	}
	var own, answered string
	own, args = inIDs(args, hidden)
	answered, args = inIDs(args, hidden)
	// #nosec G202 -- inIDs emits only placeholders; every sender is bound.
	return " AND m.sender NOT" + own + ` AND NOT EXISTS (SELECT 1 FROM messages p
		WHERE m.reply_to <> '' AND p.room_id = m.room_id AND p.event_id = m.reply_to AND p.sender` + answered + ")", args
}

// WithoutHidden is msgs less the hidden ones (see Hides).
func (c *Cache) WithoutHidden(ctx context.Context, msgs []domain.Message) ([]domain.Message, error) {
	out := make([]domain.Message, 0, len(msgs))
	for i := range msgs {
		hidden, err := c.Hides(ctx, msgs[i])
		if err != nil {
			return nil, err
		}
		if !hidden {
			out = append(out, msgs[i])
		}
	}
	return out, nil
}
