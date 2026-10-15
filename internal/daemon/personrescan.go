package daemon

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// rescanPeople judges the senders of cached messages since since by the sender moves
// that hold for an old message: not sender_first, since the cache may not reach their
// first message. Each is judged at each caught message, as it stood then; the first
// that marks them into a tag is that mark's sample. With apply it records the marks,
// for only the people in only when that is not empty.
func (w *filterWatch) rescanPeople(
	ctx context.Context, rooms []domain.RoomFacts, since time.Time, apply bool, only []string,
) ([]domain.PersonCandidate, error) {
	if w.people == nil {
		return nil, nil
	}
	w.seed(ctx)
	w.mu.Lock()
	filters := w.filters
	w.mu.Unlock()
	var judging domain.Filters
	var longest time.Duration
	for i := range filters {
		f := filters[i]
		f.Moves.SenderFirst = false
		if f.Moves.MarksSenders() {
			judging = append(judging, f)
			longest = max(longest, f.Window)
		}
	}
	if len(judging) == 0 {
		return nil, nil
	}
	senders, err := w.caughtSenders(ctx, rooms, judging, since)
	if err != nil {
		return nil, err
	}
	var out []domain.PersonCandidate
	for _, sender := range senders {
		if err := ctx.Err(); err != nil {
			return out, fmt.Errorf("daemon: rescan stopped: %w", err)
		}
		if len(only) > 0 && !slices.Contains(only, sender) {
			continue
		}
		found, err := w.judgeSender(ctx, sender, judging, since, longest)
		if err != nil {
			return out, err
		}
		for i := range found {
			if !apply || w.recordPerson(ctx, found[i].Verdict) {
				out = append(out, found[i])
			}
		}
	}
	return out, nil
}

// caughtSenders is everyone not you with a message since since that a filter catches,
// in order of ID.
func (w *filterWatch) caughtSenders(ctx context.Context, rooms []domain.RoomFacts, filters domain.Filters, since time.Time) ([]string, error) {
	seen := map[string]bool{}
	for i := range rooms {
		msgs, err := w.store.CachedTimeline(ctx, domain.RoomID(rooms[i].ID))
		if err != nil {
			return nil, fmt.Errorf("daemon: read %s to rescan it: %w", rooms[i].ID, err)
		}
		for j := range msgs {
			m := &msgs[j]
			if seen[m.Sender] || m.IsUpdate() || m.Timestamp.Before(since) || (w.mine != nil && w.mine(m.Sender)) {
				continue
			}
			if len(filters.Catching(m.Body, m.Sender, rooms[i])) > 0 {
				seen[m.Sender] = true
			}
		}
	}
	return slices.Sorted(func(yield func(string) bool) {
		for s := range seen {
			if !yield(s) {
				return
			}
		}
	}), nil
}

// judgeSender finds, per tag, the first caught message since since that marks sender
// there, skipping tags they are settled in and anyone you wrote to.
func (w *filterWatch) judgeSender(
	ctx context.Context, sender string, filters domain.Filters, since time.Time, longest time.Duration,
) ([]domain.PersonCandidate, error) {
	from := time.Time{}
	if !since.IsZero() {
		from = since.Add(-longest)
	}
	msgs, err := w.people.SenderMessages(ctx, sender, from, senderHistoryCap)
	if err != nil {
		return nil, fmt.Errorf("daemon: read %s's messages to rescan them: %w", sender, err)
	}
	history := senderHistory{earlier: true, window: msgs, since: from}
	marked := map[string]bool{}
	var out []domain.PersonCandidate
	for i := range filters {
		f := &filters[i]
		if marked[tagKey(f.Tag)] {
			continue
		}
		settled, err := w.settledPerson(ctx, sender, f.Tag)
		if err != nil {
			return out, err
		}
		if settled {
			continue
		}
		found, ok, err := w.firstMarking(ctx, sender, f, history, since)
		if err != nil {
			return out, err
		}
		if ok {
			marked[tagKey(f.Tag)] = true
			out = append(out, found)
		}
	}
	return out, nil
}

// firstMarking is the first caught message of the sender's since since that marks them
// by f, as things stood then; none when it is nobody's, or you wrote to them.
func (w *filterWatch) firstMarking(
	ctx context.Context, sender string, f *domain.Filter, history senderHistory, since time.Time,
) (domain.PersonCandidate, bool, error) {
	for j := range history.window {
		m := history.window[j]
		if m.Timestamp.Before(since) {
			continue
		}
		var facts domain.RoomFacts
		if w.facts != nil {
			facts = w.facts(ctx, m.RoomID)
		}
		if !f.Catches(m.Body, m.Sender, facts) {
			continue
		}
		rule, fires := f.Marking(history.judge(ctx, *f, m, w.facts))
		if !fires {
			continue
		}
		spoken, err := w.writtenTo(ctx, sender)
		if err != nil || spoken {
			return domain.PersonCandidate{}, false, err
		}
		return domain.PersonCandidate{
			Verdict: domain.PersonVerdict{Person: sender, Tag: f.Tag, Rule: rule, Filter: f.Name, At: w.now()},
			Sample:  m,
		}, true, nil
	}
	return domain.PersonCandidate{}, false, nil
}
