package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// filterStore is what moving a room into a tag needs. Filters run in the daemon because
// a first message arrives once, usually with no client open; moves go to room account
// data so they survive rebuilds and reach other machines.
type filterStore interface {
	MarkTag(ctx context.Context, verdict domain.TagVerdict) error
	TagVerdicts(ctx context.Context) ([]domain.TagVerdict, error)
	CachedTimeline(ctx context.Context, roomID domain.RoomID) ([]domain.Message, error)
}

// filterWatch runs the filters over each message and remembers what they moved, in
// memory, seeded from the cache.
type filterWatch struct {
	store filterStore
	// log hears failed reads and writes; nil is silent. Set before use.
	log *slog.Logger
	// mine reports whether a sender is this person: having written in a room separates
	// a conversation from a broadcast.
	mine func(sender string) bool

	mu      sync.Mutex
	filters domain.Filters
	tags    domain.TagSet
	// stored mirrors the account data (moves and releases, by room and lower-cased tag),
	// re-read after filterReseed or a reload: a release on another machine arrives only
	// by sync.
	stored map[domain.RoomID]map[string]domain.TagVerdict
	// recorded is what this process moved, kept apart from stored so a re-read cannot drop
	// a move whose write failed.
	recorded map[domain.RoomID]map[string]bool
	seededAt time.Time
	seeded   bool
	now      func() time.Time
	// established memoizes rooms known to have history (a room never loses it).
	established map[domain.RoomID]bool

	// people is where person marks go (personwatch.go); nil leaves the sender moves off.
	// facts, directory and selves are each room's facts, the people directory and every
	// ID that is you; groups the identity groups linking IDs, set by reload.
	people    personStore
	facts     func(context.Context, domain.RoomID) domain.RoomFacts
	directory func(context.Context) (domain.Directory, error)
	selves    func() []string
	groups    [][]string
	// peopleStored mirrors the person marks and releases (by ID and tag), peopleRecorded
	// the marks this process made, as stored and recorded do for rooms.
	peopleStored   map[string]map[string]domain.PersonVerdict
	peopleRecorded map[string]map[string]bool
}

func newFilterWatch(store filterStore) *filterWatch {
	return &filterWatch{
		store:       store,
		stored:      map[domain.RoomID]map[string]domain.TagVerdict{},
		recorded:    map[domain.RoomID]map[string]bool{},
		established: map[domain.RoomID]bool{},
		now:         time.Now,

		peopleStored:   map[string]map[string]domain.PersonVerdict{},
		peopleRecorded: map[string]map[string]bool{},
	}
}

// filterReseed is how long the memo of moves and releases is trusted.
const filterReseed = time.Minute

// reload replaces the filters, tags and identity groups and marks the memo stale.
func (w *filterWatch) reload(filters domain.Filters, tags domain.TagSet, groups [][]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.filters, w.tags, w.groups = filters, tags, groups
	w.seeded = false
}

// tagKey is a tag as the memo keys it.
func tagKey(tag string) string { return strings.ToLower(strings.TrimSpace(tag)) }

// caught is the tags filters moved room into, released ones left out. It reads memory
// only, so a facts lookup on every cache write stays cheap.
func (w *filterWatch) caught(room domain.RoomID) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for key, v := range w.stored[room] {
		if v.In() && !w.recorded[room][key] {
			out = append(out, key)
		}
	}
	for key := range w.recorded[room] {
		if v, ok := w.stored[room][key]; !ok || !v.Released {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

// settled reports whether room is in tag by a move already, or released from it.
func (w *filterWatch) settled(room domain.RoomID, tag string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, stored := w.stored[room][tagKey(tag)]
	return stored || w.recorded[room][tagKey(tag)]
}

// watch runs the filters over one message, moving its room into each tag a filter's
// room moves fire for, and marking its sender for each its sender moves fire for. A
// tag's excluded list and a release outrank every filter. Our own messages are not
// watched (Deliver). It reports whether the sender was marked.
func (w *filterWatch) watch(ctx context.Context, msg domain.Message, facts domain.RoomFacts) bool {
	w.mu.Lock()
	filters, tags := w.filters, w.tags
	fresh := w.seeded && w.now().Sub(w.seededAt) < filterReseed
	w.mu.Unlock()
	// Every message refreshes a stale memo: a room moved earlier stays quiet even when
	// this message catches nothing.
	if !fresh {
		w.seed(ctx)
	}
	caught := filters.Catching(msg.Body, msg.Sender, facts)
	if len(caught) == 0 {
		return false
	}
	w.watchRoom(ctx, msg, facts, caught, tags)
	return w.watchSender(ctx, msg, caught)
}

// watchRoom runs the room moves of the filters that caught msg.
func (w *filterWatch) watchRoom(ctx context.Context, msg domain.Message, facts domain.RoomFacts, caught []domain.Filter, tags domain.TagSet) {
	var history *roomHistory
	for i := range caught {
		f := &caught[i]
		if !f.Moves.MovesRooms() || w.settled(msg.RoomID, f.Tag) || tags.Excludes(f.Tag, facts) {
			continue
		}
		if history == nil {
			h, err := w.history(ctx, msg)
			if err != nil {
				// Unknown must never read as "new and unspoken-in", which the strongest moves fire on.
				w.warn(ctx, "filters: read the room's history failed; not judging this message", err, "room", msg.RoomID, "event", msg.ID)
				return
			}
			history = &h
		}
		c := domain.MoveCase{Direct: facts.Direct, First: history.first, Mine: history.mine}
		if f.Moves.Ratio {
			c.Caught, c.Total = window(history.msgs, *f, facts, w.now())
		}
		if rule, moved := f.Moving(c); moved {
			w.record(ctx, domain.TagVerdict{Room: msg.RoomID, Tag: f.Tag, Rule: rule, Filter: f.Name, At: w.now()})
		}
	}
}

type roomHistory struct {
	msgs  []domain.Message
	first bool
	mine  bool
}

// history reads what a room has said. Only "has history" is memoized: a room that
// looks new may just not be fetched yet.
func (w *filterWatch) history(ctx context.Context, msg domain.Message) (roomHistory, error) {
	msgs, err := w.store.CachedTimeline(ctx, msg.RoomID)
	if err != nil {
		return roomHistory{}, fmt.Errorf("daemon: read %s to judge it: %w", msg.RoomID, err)
	}
	out := roomHistory{msgs: msgs, first: true}
	for i := range msgs {
		if msgs[i].IsUpdate() {
			continue
		}
		if w.mine != nil && w.mine(msgs[i].Sender) {
			out.mine = true
		}
		if msgs[i].ID != msg.ID {
			out.first = false
		}
	}
	w.mu.Lock()
	known := w.established[msg.RoomID]
	if !out.first {
		w.established[msg.RoomID] = true
	}
	w.mu.Unlock()
	if known {
		out.first = false
	}
	return out, nil
}

func (w *filterWatch) warn(ctx context.Context, msg string, err error, attrs ...any) {
	if err == nil || w.log == nil {
		return
	}
	w.log.Log(ctx, levelFor(ctx), msg, append([]any{"err", err}, attrs...)...)
}

// window counts how much of a room's conversation over f's window f catches.
func window(msgs []domain.Message, f domain.Filter, room domain.RoomFacts, now time.Time) (caught, total int) {
	since := now.Add(-f.Window)
	for i := range msgs {
		if msgs[i].IsUpdate() || msgs[i].Timestamp.Before(since) {
			continue
		}
		total++
		if f.Catches(msgs[i].Body, msgs[i].Sender, room) {
			caught++
		}
	}
	return caught, total
}

// record writes the move to account data and memory, reporting whether it is in force
// (not if the room was released from that tag elsewhere).
func (w *filterWatch) record(ctx context.Context, verdict domain.TagVerdict) bool {
	err := w.store.MarkTag(ctx, verdict)
	w.mu.Lock()
	defer w.mu.Unlock()
	key := tagKey(verdict.Tag)
	if errors.Is(err, domain.ErrReleased) {
		if w.stored[verdict.Room] == nil {
			w.stored[verdict.Room] = map[string]domain.TagVerdict{}
		}
		w.stored[verdict.Room][key] = domain.TagVerdict{Room: verdict.Room, Tag: key, Released: true}
		return false
	}
	// Any other failure keeps the move for this process only: it will not survive a
	// restart or reach other devices.
	w.warn(ctx, "filters: record the move failed; it holds until restart only", err, "room", verdict.Room, "tag", verdict.Tag)
	if w.recorded[verdict.Room] == nil {
		w.recorded[verdict.Room] = map[string]bool{}
	}
	w.recorded[verdict.Room][key] = true
	return true
}

// seed rereads moves and releases from the cache; on failure it stays stale so the
// next message retries.
func (w *filterWatch) seed(ctx context.Context) {
	verdicts, err := w.store.TagVerdicts(ctx)
	if err != nil {
		w.warn(ctx, "filters: reread the moves failed; using the stale set", err)
		return
	}
	w.seedPeople(ctx)
	stored := make(map[domain.RoomID]map[string]domain.TagVerdict, len(verdicts))
	for _, v := range verdicts {
		if !v.In() && !v.Released {
			continue
		}
		if stored[v.Room] == nil {
			stored[v.Room] = map[string]domain.TagVerdict{}
		}
		stored[v.Room][tagKey(v.Tag)] = v
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stored, w.seeded, w.seededAt = stored, true, w.now()
}
