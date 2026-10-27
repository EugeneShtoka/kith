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

// Hidden people are the senders the muted tags' people lists name, or verdicts put in a
// muted tag (person_verdicts), matched across
// every ID the directory and the identity groups link to them (domain.HiddenPeople).
// Search leaves them out, and direct replies to them. SQL cannot ask the matcher, so
// the cache keeps the list of senders it holds that are hidden: rebuilt when the marks
// or the directory change, extended as senders are written.
//
// A profile hides more people on top (its people.exclude, UseProfileHidden): each has a
// matcher and a list of its own, read for a request running in it (domain.ProfileIn).

type hiddenState struct {
	// rebuilding holds rebuilds to one at a time, so the last to start lands last.
	rebuilding sync.Mutex

	mu     sync.Mutex
	marked []string
	// muted is the tags whose verdicts hide people.
	muted  []string
	groups [][]string
	// shared is who everyone hides.
	shared senderSet
	// excluded is each profile's own exclusions by name; profiles their senders.
	excluded map[string]domain.ExcludedPeople
	profiles map[string]*senderSet
	// changed hears the rooms a rebuild changed (OnHiddenChange), changedHere those a
	// profile's own hiding changed (OnProfileHiddenChange).
	changed, changedHere func(context.Context, []domain.RoomID)
}

// senderSet is one matcher and what it found among the senders the cache holds.
type senderSet struct {
	people domain.HiddenPeople
	// seen is every sender written or found, true for the hidden; ids are those.
	seen map[string]bool
	ids  []string
}

// classify records each new sender, hidden or not.
func (s *senderSet) classify(senders []string) {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	for _, sender := range senders {
		if _, done := s.seen[sender]; done || sender == "" {
			continue
		}
		hidden := s.people.Has(sender)
		s.seen[sender] = hidden
		if hidden {
			s.ids = append(s.ids, sender)
		}
	}
}

// OnHiddenChange has fn called after a rebuild changes which senders are hidden, with
// the rooms they wrote in, so counts kept elsewhere can be taken again. Senders first
// written later are not a change: whoever writes them counts their room anew.
func (c *Cache) OnHiddenChange(fn func(context.Context, []domain.RoomID)) {
	c.hidden.mu.Lock()
	defer c.hidden.mu.Unlock()
	c.hidden.changed = fn
}

// OnProfileHiddenChange has fn called after a rebuild changes whom a profile hides
// beyond everyone's hidden people, with the rooms they wrote in: their counts in that
// profile change, nothing else does.
func (c *Cache) OnProfileHiddenChange(fn func(context.Context, []domain.RoomID)) {
	c.hidden.mu.Lock()
	defer c.hidden.mu.Unlock()
	c.hidden.changedHere = fn
}

// UseHidden sets the people the muted tags list, the muted tags (whose verdicts hide
// people too) and the identity groups that link IDs, and rebuilds the list of hidden
// senders. Nobody is hidden until it is called.
func (c *Cache) UseHidden(ctx context.Context, marked, muted []string, groups [][]string) error {
	c.hidden.mu.Lock()
	c.hidden.marked, c.hidden.muted, c.hidden.groups = slices.Clone(marked), slices.Clone(muted), slices.Clone(groups)
	c.hidden.mu.Unlock()
	return c.rebuildHidden(ctx)
}

// UseProfileHidden sets whom each profile hides on top of everyone's hidden people, by
// profile name, and rebuilds; a profile left out hides no more than everyone does. A
// request running in a profile (domain.WithProfile) reads its list.
func (c *Cache) UseProfileHidden(ctx context.Context, excluded map[string]domain.ExcludedPeople) error {
	c.hidden.mu.Lock()
	c.hidden.excluded = maps.Clone(excluded)
	c.hidden.mu.Unlock()
	return c.rebuildHidden(ctx)
}

// rebuildHidden builds the matchers over the current directory and classifies every
// sender the cache holds, and every sender written meanwhile.
func (c *Cache) rebuildHidden(ctx context.Context) error {
	c.hidden.rebuilding.Lock()
	defer c.hidden.rebuilding.Unlock()
	c.hidden.mu.Lock()
	marked, muted, groups, excluded := c.hidden.marked, c.hidden.muted, c.hidden.groups, c.hidden.excluded
	c.hidden.mu.Unlock()
	if len(muted) > 0 {
		verdicts, err := c.PersonVerdicts(ctx)
		if err != nil {
			return err
		}
		marked = slices.Concat(marked, domain.HiddenByVerdict(verdicts, muted))
	}
	people, profiles, senders, err := c.hiddenMatchers(ctx, marked, groups, excluded)
	if err != nil {
		return err
	}
	moved, movedHere, changed, changedHere := c.swapHidden(people, profiles, senders)
	if err := c.tellMoved(ctx, moved, changed); err != nil {
		return err
	}
	return c.tellMoved(ctx, movedHere, changedHere)
}

// hiddenMatchers builds everyone's matcher and each profile's over the current
// directory, with every sender the cache holds; nothing to read when nobody is hidden.
func (c *Cache) hiddenMatchers(ctx context.Context, marked []string, groups [][]string, excluded map[string]domain.ExcludedPeople) (
	people domain.HiddenPeople, profiles map[string]*senderSet, senders []string, err error,
) {
	profiles = map[string]*senderSet{}
	excluding := slices.ContainsFunc(slices.Collect(maps.Values(excluded)), func(e domain.ExcludedPeople) bool { return !e.Empty() })
	if len(marked) == 0 && !excluding {
		return people, profiles, nil, nil
	}
	dir, err := c.Directory(ctx)
	if err != nil {
		return people, nil, nil, err
	}
	mine := c.mine()
	people = domain.NewHiddenPeople(marked, groups, dir, mine)
	for name, ex := range excluded {
		if !ex.Empty() {
			profiles[name] = &senderSet{people: domain.NewExcludedPeople(ex, groups, dir, mine)}
		}
	}
	senders, err = collect(ctx, c.db, "senders", "SELECT DISTINCT sender FROM messages WHERE sender <> ''",
		func(rows *sql.Rows) (string, error) {
			var s string
			scanErr := rows.Scan(&s)
			return s, scanErr
		})
	if err != nil {
		return people, nil, nil, fmt.Errorf("db: classify senders: %w", err)
	}
	return people, profiles, senders, nil
}

// swapHidden puts the new matchers in force, classifying senders and every sender
// noted meanwhile, and says whose hiding changed: everyone's (moved) and a profile's
// own (movedHere), with who hears each.
func (c *Cache) swapHidden(people domain.HiddenPeople, profiles map[string]*senderSet, senders []string) (
	moved, movedHere []string, changed, changedHere func(context.Context, []domain.RoomID),
) {
	c.hidden.mu.Lock()
	defer c.hidden.mu.Unlock()
	// Senders noted while the scan ran are in seen, under the old matcher.
	all := slices.Concat(senders, slices.Collect(maps.Keys(c.hidden.shared.seen)))
	before := c.hidden.shared.ids
	c.hidden.shared = senderSet{people: people}
	c.hidden.shared.classify(all)
	moved = symmetricDifference(before, c.hidden.shared.ids)
	// A profile's own: those it hid and no longer exists to, and what each now hides
	// differently.
	for name := range c.hidden.profiles {
		if profiles[name] == nil {
			movedHere = append(movedHere, c.hidden.profiles[name].ids...)
		}
	}
	for name, set := range profiles {
		set.classify(all)
		var before []string
		if old := c.hidden.profiles[name]; old != nil {
			before = old.ids
		}
		movedHere = append(movedHere, symmetricDifference(before, set.ids)...)
	}
	c.hidden.profiles = profiles
	return moved, movedHere, c.hidden.changed, c.hidden.changedHere
}

// tellMoved calls changed with the rooms the senders moved wrote in, if any.
func (c *Cache) tellMoved(ctx context.Context, moved []string, changed func(context.Context, []domain.RoomID)) error {
	slices.Sort(moved)
	moved = slices.Compact(moved)
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
	c.hidden.shared.classify(senders)
	for _, set := range c.hidden.profiles {
		set.classify(senders)
	}
}

// profileSet is the senders ctx's profile hides beyond everyone's; nil when none. Call
// with hidden.mu held.
func (c *Cache) profileSet(ctx context.Context) *senderSet {
	return c.hidden.profiles[domain.ProfileIn(ctx)]
}

// hiddenSenders is the senders the cache holds that are hidden in ctx's profile:
// everyone's and the profile's own, never one of yours.
func (c *Cache) hiddenSenders(ctx context.Context) []string {
	mine := c.mine()
	c.hidden.mu.Lock()
	defer c.hidden.mu.Unlock()
	ids := c.hidden.shared.ids
	if set := c.profileSet(ctx); set != nil {
		ids = slices.Concat(ids, set.ids)
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(mine, id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// hides reports whether sender is hidden in ctx's profile, asking the matchers (a
// sender not yet written counts too).
func (c *Cache) hides(ctx context.Context, sender string) bool {
	if sender == "" || slices.Contains(c.mine(), sender) {
		return false
	}
	c.hidden.mu.Lock()
	defer c.hidden.mu.Unlock()
	if c.hidden.shared.people.Has(sender) {
		return true
	}
	set := c.profileSet(ctx)
	return set != nil && set.people.Has(sender)
}

// ProfileHides reports whether the profile named hides anyone beyond everyone's hidden
// people.
func (c *Cache) ProfileHides(name string) bool {
	c.hidden.mu.Lock()
	defer c.hidden.mu.Unlock()
	set := c.hidden.profiles[name]
	return set != nil && !set.people.Empty()
}

// Hides reports whether msg is hidden in ctx's profile: its sender is, or it answers a
// message whose sender is.
func (c *Cache) Hides(ctx context.Context, msg domain.Message) (bool, error) {
	if c.hides(ctx, msg.Sender) {
		return true, nil
	}
	if msg.ReplyTo == "" {
		return false, nil
	}
	c.hidden.mu.Lock()
	empty := c.hidden.shared.people.Empty()
	if set := c.profileSet(ctx); set != nil {
		empty = empty && set.people.Empty()
	}
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
	return c.hides(ctx, sender), nil
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
