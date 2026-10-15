package daemon

import (
	"context"
	"slices"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A direct message with someone a muted tag hides moves into that tag, as the direct
// move does: never a DM you wrote in, a room released from the tag, or one its excluded
// names. Like every move it stays when the person is let go; taking the room out is
// yours.

// moveWithPerson moves room, when it is such a direct message, into each muted tag
// holding its other person, listed in the tag or marked into it.
func (w *filterWatch) moveWithPerson(ctx context.Context, room domain.RoomID) {
	if w.facts == nil {
		return
	}
	facts := w.facts(ctx, room)
	if !facts.Direct {
		return
	}
	w.seedIfStale(ctx)
	w.mu.Lock()
	tags, groups := w.tags, w.groups
	w.mu.Unlock()
	var open []domain.Tag
	for i := range tags.Len() {
		if t := tags.At(i); t.Mute && !w.settled(room, t.Name) && !tags.Excludes(t.Name, facts) {
			open = append(open, t)
		}
	}
	if len(open) == 0 {
		return
	}
	msgs, err := w.store.CachedTimeline(ctx, room)
	if err != nil {
		w.warn(ctx, "filters: read a direct message to judge its person failed", err, "room", room)
		return
	}
	var peers []string
	for i := range msgs {
		m := &msgs[i]
		if m.IsUpdate() {
			continue
		}
		if w.mine != nil && w.mine(m.Sender) {
			return // a conversation: you wrote in it
		}
		peers = append(peers, m.Sender)
	}
	if len(peers) == 0 {
		return
	}
	dir, err := w.directoryNow(ctx)
	if err != nil {
		w.warn(ctx, "filters: read the people directory failed; not moving the direct message", err, "room", room)
		return
	}
	for i := range open {
		for who, filter := range w.holding(open[i]) {
			person := domain.NewHiddenPeople([]string{who}, groups, dir, nil)
			if !slices.ContainsFunc(peers, person.Has) {
				continue
			}
			w.record(ctx, domain.TagVerdict{Room: room, Tag: open[i].Name, Rule: domain.MoveWithPerson, Filter: filter, At: w.now()})
			break
		}
	}
}

// holding is everyone tag holds, listed (no filter) or marked (by the filter named).
func (w *filterWatch) holding(tag domain.Tag) map[string]string {
	out := map[string]string{}
	for _, id := range tag.People {
		out[id] = ""
	}
	key := tagKey(tag.Name)
	w.mu.Lock()
	defer w.mu.Unlock()
	for person, tags := range w.peopleStored {
		if v, ok := tags[key]; ok && v.In() {
			if _, listed := out[person]; !listed {
				out[person] = v.Filter
			}
		}
	}
	for person, tags := range w.peopleRecorded {
		if _, listed := out[person]; !listed && tags[key] {
			out[person] = "a filter"
		}
	}
	return out
}
