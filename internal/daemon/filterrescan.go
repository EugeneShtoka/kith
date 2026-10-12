package daemon

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// rescan judges each room's cached messages since since by the moves that hold for an
// old message: not first_message, since the cache may not reach a room's start. The
// first message that moves a room into a tag is that move's sample. With apply it
// records the moves, for only the rooms in only when that is not empty.
func (w *filterWatch) rescan(
	ctx context.Context, rooms []domain.RoomFacts, since time.Time, apply bool, only []domain.RoomID,
) ([]domain.MoveCandidate, error) {
	w.seed(ctx)
	w.mu.Lock()
	filters, tags := w.filters, w.tags
	w.mu.Unlock()
	if len(filters) == 0 {
		return nil, nil
	}
	var out []domain.MoveCandidate
	for i := range rooms {
		if err := ctx.Err(); err != nil {
			return out, fmt.Errorf("daemon: rescan stopped: %w", err)
		}
		facts := rooms[i]
		if len(only) > 0 && !slices.Contains(only, domain.RoomID(facts.ID)) {
			continue
		}
		found, err := w.judgeHistory(ctx, facts, filters, tags, since)
		if err != nil {
			return out, err
		}
		for j := range found {
			if !apply || w.record(ctx, found[j].Verdict) {
				out = append(out, found[j])
			}
		}
	}
	return out, nil
}

// judgeHistory finds, per tag, the oldest message since since that moves room there.
func (w *filterWatch) judgeHistory(
	ctx context.Context, room domain.RoomFacts, filters domain.Filters, tags domain.TagSet, since time.Time,
) ([]domain.MoveCandidate, error) {
	id := domain.RoomID(room.ID)
	open := slices.DeleteFunc(slices.Clone(filters), func(f domain.Filter) bool {
		return w.settled(id, f.Tag) || tags.Excludes(f.Tag, room)
	})
	if len(open) == 0 {
		return nil, nil
	}
	msgs, err := w.store.CachedTimeline(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("daemon: read %s to rescan it: %w", room.ID, err)
	}
	mine := slices.ContainsFunc(msgs, func(m domain.Message) bool {
		return !m.IsUpdate() && w.mine != nil && w.mine(m.Sender)
	})
	slices.SortStableFunc(msgs, func(a, b domain.Message) int { return a.Timestamp.Compare(b.Timestamp) })
	moved := map[string]bool{}
	var out []domain.MoveCandidate
	for i := range msgs {
		m := msgs[i]
		if m.IsUpdate() || m.Timestamp.Before(since) || (w.mine != nil && w.mine(m.Sender)) {
			continue
		}
		for j := range open {
			f := &open[j]
			if moved[tagKey(f.Tag)] || !f.Catches(m.Body, m.Sender, room) {
				continue
			}
			c := domain.MoveCase{Direct: room.Direct, Mine: mine}
			if f.Moves.Ratio {
				c.Caught, c.Total = window(msgs, *f, room, w.now())
			}
			if rule, ok := f.Moving(c); ok {
				moved[tagKey(f.Tag)] = true
				verdict := domain.TagVerdict{Room: id, Tag: f.Tag, Rule: rule, Filter: f.Name, At: w.now()}
				out = append(out, domain.MoveCandidate{Verdict: verdict, Sample: m})
			}
		}
	}
	return out, nil
}
