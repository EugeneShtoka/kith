package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A filter's sender moves mark the person who sent a caught message, judged over
// everything they wrote in every room. The rails are the room moves': a mark is never
// made over a release (on any of the person's linked IDs), and never on anyone you have
// written to.

// personStore is what marking a person needs; a source without it leaves the sender
// moves off.
type personStore interface {
	MarkPerson(ctx context.Context, verdict domain.PersonVerdict) error
	PersonVerdicts(ctx context.Context) ([]domain.PersonVerdict, error)
	SenderMessages(ctx context.Context, sender string, since time.Time, limit int) ([]domain.Message, error)
	WrittenTo(ctx context.Context, me []string) ([]string, error)
}

// senderHistoryCap bounds the messages one judgment reads of a sender's window.
const senderHistoryCap = 2000

// watchSender runs the sender moves of the filters that caught msg, marking its
// sender into each tag one fires for; it reports whether it marked them anywhere.
func (w *filterWatch) watchSender(ctx context.Context, msg domain.Message, caught []domain.Filter) bool {
	if w.people == nil {
		return false
	}
	var history *senderHistory
	var window time.Duration
	for i := range caught {
		if caught[i].Moves.MarksSenders() {
			window = max(window, caught[i].Window)
		}
	}
	marked := false
	for i := range caught {
		f := &caught[i]
		if !f.Moves.MarksSenders() {
			continue
		}
		settled, err := w.settledPerson(ctx, msg.Sender, f.Tag)
		if err != nil {
			w.warn(ctx, "filters: read the person marks failed; not judging the sender", err, "sender", msg.Sender)
			return marked
		}
		if settled {
			continue
		}
		if history == nil {
			h, herr := w.senderHistory(ctx, msg, window)
			if herr != nil {
				// Unknown must never read as "their first message", which marks.
				w.warn(ctx, "filters: read the sender's messages failed; not judging them", herr, "sender", msg.Sender)
				return marked
			}
			history = &h
		}
		rule, fires := f.Marking(history.judge(ctx, *f, msg, w.facts))
		if !fires {
			continue
		}
		spoken, err := w.writtenTo(ctx, msg.Sender)
		if err != nil || spoken {
			w.warn(ctx, "filters: read whom you wrote to failed; not marking the sender", err, "sender", msg.Sender)
			continue
		}
		if w.recordPerson(ctx, domain.PersonVerdict{Person: msg.Sender, Tag: f.Tag, Rule: rule, Filter: f.Name, At: w.now()}) {
			marked = true
		}
	}
	return marked
}

// senderHistory is what a sender has written: whether anything before msg, and their
// messages over the longest window asked.
type senderHistory struct {
	earlier bool
	window  []domain.Message
	since   time.Time
}

func (w *filterWatch) senderHistory(ctx context.Context, msg domain.Message, window time.Duration) (senderHistory, error) {
	first, err := w.people.SenderMessages(ctx, msg.Sender, time.Time{}, 2)
	if err != nil {
		return senderHistory{}, fmt.Errorf("daemon: read %s's first messages: %w", msg.Sender, err)
	}
	out := senderHistory{since: w.now().Add(-window)}
	for i := range first {
		if first[i].ID != msg.ID {
			out.earlier = true
		}
	}
	if out.window, err = w.people.SenderMessages(ctx, msg.Sender, out.since, senderHistoryCap); err != nil {
		return senderHistory{}, fmt.Errorf("daemon: read %s's messages: %w", msg.Sender, err)
	}
	return out, nil
}

// judge is what f's sender moves know about the sender of msg. The window is the
// longest read; a filter with a shorter one counts only its own span.
func (h senderHistory) judge(
	ctx context.Context, f domain.Filter, msg domain.Message, facts func(context.Context, domain.RoomID) domain.RoomFacts,
) domain.SenderCase {
	c := domain.SenderCase{First: !h.earlier}
	since := msg.Timestamp.Add(-f.Window)
	if msg.Timestamp.IsZero() {
		since = h.since
	}
	same := strings.ToLower(strings.TrimSpace(msg.Body))
	rooms := map[domain.RoomID]bool{msg.RoomID: true}
	known := map[domain.RoomID]domain.RoomFacts{}
	factsOf := func(room domain.RoomID) domain.RoomFacts {
		got, ok := known[room]
		if !ok && facts != nil {
			got = facts(ctx, room)
			known[room] = got
		}
		return got
	}
	for i := range h.window {
		m := &h.window[i]
		if m.Timestamp.Before(since) {
			continue
		}
		c.Total++
		if !f.Catches(m.Body, m.Sender, factsOf(m.RoomID)) {
			continue
		}
		c.Caught++
		if strings.ToLower(strings.TrimSpace(m.Body)) == same {
			rooms[m.RoomID] = true
		}
	}
	c.Rooms = len(rooms)
	return c
}

// settledPerson reports whether sender is marked into tag already, or released from it,
// by this ID or any ID linked to it.
func (w *filterWatch) settledPerson(ctx context.Context, sender, tag string) (bool, error) {
	key := tagKey(tag)
	w.mu.Lock()
	if _, ok := w.peopleStored[sender][key]; ok || w.peopleRecorded[sender][key] {
		w.mu.Unlock()
		return true, nil
	}
	var linked []string
	for person, tags := range w.peopleStored {
		if _, ok := tags[key]; ok {
			linked = append(linked, person)
		}
	}
	for person, tags := range w.peopleRecorded {
		if tags[key] {
			linked = append(linked, person)
		}
	}
	groups := w.groups
	w.mu.Unlock()
	if len(linked) == 0 {
		return false, nil
	}
	dir, err := w.directoryNow(ctx)
	if err != nil {
		return false, err
	}
	return domain.NewHiddenPeople(linked, groups, dir, nil).Has(sender), nil
}

// writtenTo reports whether you have written to sender, by any ID linked to them.
func (w *filterWatch) writtenTo(ctx context.Context, sender string) (bool, error) {
	var me []string
	if w.selves != nil {
		me = w.selves()
	}
	spoken, err := w.people.WrittenTo(ctx, me)
	if err != nil {
		return false, fmt.Errorf("daemon: read whom you wrote to: %w", err)
	}
	if len(spoken) == 0 {
		return false, nil
	}
	dir, err := w.directoryNow(ctx)
	if err != nil {
		return false, err
	}
	w.mu.Lock()
	groups := w.groups
	w.mu.Unlock()
	return domain.NewHiddenPeople(spoken, groups, dir, nil).Has(sender), nil
}

func (w *filterWatch) directoryNow(ctx context.Context) (domain.Directory, error) {
	if w.directory == nil {
		return domain.Directory{}, nil
	}
	dir, err := w.directory(ctx)
	if err != nil {
		return domain.Directory{}, fmt.Errorf("daemon: read the people directory: %w", err)
	}
	return dir, nil
}

// recordPerson writes the mark and remembers it, reporting whether it is in force (not
// if the person was released from that tag elsewhere).
func (w *filterWatch) recordPerson(ctx context.Context, verdict domain.PersonVerdict) bool {
	err := w.people.MarkPerson(ctx, verdict)
	w.mu.Lock()
	defer w.mu.Unlock()
	key := tagKey(verdict.Tag)
	if errors.Is(err, domain.ErrPersonReleased) {
		if w.peopleStored[verdict.Person] == nil {
			w.peopleStored[verdict.Person] = map[string]domain.PersonVerdict{}
		}
		w.peopleStored[verdict.Person][key] = domain.PersonVerdict{Person: verdict.Person, Tag: key, Released: true}
		return false
	}
	// Any other failure keeps the mark for this process only.
	w.warn(ctx, "filters: record the mark failed; it holds until restart only", err, "person", verdict.Person, "tag", verdict.Tag)
	if w.peopleRecorded[verdict.Person] == nil {
		w.peopleRecorded[verdict.Person] = map[string]bool{}
	}
	w.peopleRecorded[verdict.Person][key] = true
	return true
}

// seedPeople rereads the person marks and releases; on failure the old set stays.
func (w *filterWatch) seedPeople(ctx context.Context) {
	if w.people == nil {
		return
	}
	verdicts, err := w.people.PersonVerdicts(ctx)
	if err != nil {
		w.warn(ctx, "filters: reread the person marks failed; using the stale set", err)
		return
	}
	stored := map[string]map[string]domain.PersonVerdict{}
	for _, v := range verdicts {
		if !v.In() && !v.Released {
			continue
		}
		if stored[v.Person] == nil {
			stored[v.Person] = map[string]domain.PersonVerdict{}
		}
		stored[v.Person][tagKey(v.Tag)] = v
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.peopleStored = stored
}
