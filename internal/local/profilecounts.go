package local

import (
	"context"
	"fmt"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A network counts a room's unread once, leaving out the people everyone hides. A
// profile that hides more people (its people.exclude) takes their messages out of that
// count as each request reads it: the cache counts the room both ways, and the
// difference comes off the network's numbers, in the main timeline and per thread.

// ProfileUnread is unread as ctx's profile counts it: less what the people only it
// hides wrote unread, messages and mentions. A count the cache cannot take (the read
// position unknown, so the network's own numbers stand) is left as it is.
func (s *Service) ProfileUnread(ctx context.Context, unread []domain.Unread) ([]domain.Unread, error) {
	if s.cache == nil || len(unread) == 0 || !s.cache.ProfileHides(domain.ProfileIn(ctx)) {
		return unread, nil
	}
	shared, me := domain.WithProfile(ctx, ""), s.me()
	room := domain.RoomID("")
	if len(unread) == 1 {
		room = unread[0].RoomID
	}
	everyone, err := unreadCounts(shared, s.cache, me, room)
	if err != nil {
		return nil, err
	}
	here, err := unreadCounts(ctx, s.cache, me, room)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Unread, len(unread))
	for i, u := range unread {
		out[i] = less(u, everyone, here)
	}
	return out, nil
}

// counts is unread the cache counted, per room: the main timeline's (when it could)
// and each thread's.
type counts struct {
	main    map[domain.RoomID]domain.Unread
	threads map[domain.RoomID][]domain.ThreadUnread
}

// unreadCounts counts one room, or every room for "", in ctx's profile.
func unreadCounts(ctx context.Context, cache *db.Cache, me []string, room domain.RoomID) (counts, error) {
	var out counts
	var err error
	if room == "" {
		if out.main, err = cache.CountUnreadAll(ctx, me); err != nil {
			return counts{}, fmt.Errorf("local: count unread for the profile: %w", err)
		}
		if out.threads, err = cache.CountThreadUnreadAll(ctx, me); err != nil {
			return counts{}, fmt.Errorf("local: count unread threads for the profile: %w", err)
		}
		return out, nil
	}
	messages, mentions, counted, err := cache.CountUnread(ctx, me, room)
	if err != nil {
		return counts{}, fmt.Errorf("local: count unread for the profile: %w", err)
	}
	out.main = map[domain.RoomID]domain.Unread{}
	if counted {
		out.main[room] = domain.Unread{RoomID: room, Messages: messages, Mentions: mentions, Counted: true}
	}
	threads, err := cache.CountThreadUnread(ctx, me, room)
	if err != nil {
		return counts{}, fmt.Errorf("local: count unread threads for the profile: %w", err)
	}
	out.threads = map[domain.RoomID][]domain.ThreadUnread{room: threads}
	return out, nil
}

// less is u less the difference between everyone's count and the profile's: the main
// timeline's when u is counted locally, and each of u's threads' (which u's totals hold).
func less(u domain.Unread, everyone, here counts) domain.Unread {
	if g, ok := everyone.main[u.RoomID]; ok && u.Counted {
		p := here.main[u.RoomID]
		u.Messages = max(u.Messages-max(g.Messages-p.Messages, 0), 0)
		u.Mentions = max(u.Mentions-max(g.Mentions-p.Mentions, 0), 0)
	}
	if len(u.Threads) == 0 {
		return u
	}
	threads := make([]domain.ThreadUnread, 0, len(u.Threads))
	for _, t := range u.Threads {
		gu, gm := threadCount(everyone.threads[u.RoomID], t.Root)
		pu, pm := threadCount(here.threads[u.RoomID], t.Root)
		gone, goneMentions := max(min(gu-pu, t.Unread), 0), max(min(gm-pm, t.Mentions), 0)
		t.Unread -= gone
		t.Mentions -= goneMentions
		u.Messages = max(u.Messages-gone, 0)
		u.Mentions = max(u.Mentions-goneMentions, 0)
		if t.Unread > 0 {
			threads = append(threads, t)
		}
	}
	u.Threads = threads
	return u
}

// threadCount is root's counts in threads; nothing when absent.
func threadCount(threads []domain.ThreadUnread, root domain.EventID) (unread, mentions int) {
	for _, t := range threads {
		if t.Root == root {
			return t.Unread, t.Mentions
		}
	}
	return 0, 0
}
