package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// spamStore is what promoting a room into Spam needs. Promotion lives in the daemon
// because the first-message rule fires once, usually with no client open; verdicts
// go to room account data so they survive rebuilds and reach other machines.
type spamStore interface {
	MarkSpam(ctx context.Context, verdict domain.SpamVerdict) error
	SpamRooms(ctx context.Context) ([]domain.SpamVerdict, error)
	CachedTimeline(ctx context.Context, roomID domain.RoomID) ([]domain.Message, error)
}

// spamWatch decides whether a message moves its room into Spam and remembers the
// verdict, in memory, seeded from the cache.
type spamWatch struct {
	store spamStore
	// log hears failed reads and writes; nil is silent. Set before use.
	log *slog.Logger
	// mine reports whether a sender is this person: having written in a room separates
	// a conversation from a broadcast.
	mine func(sender string) bool

	mu     sync.Mutex
	rules  domain.SpamRules
	places domain.Spam
	// stored/released mirror the account data, re-read after spamReseed or a reload
	// (a release on another machine arrives only by sync).
	stored   map[domain.RoomID]bool
	released map[domain.RoomID]bool
	// recorded is what this process promoted, kept apart from stored so a re-read cannot
	// drop a verdict whose write failed.
	recorded map[domain.RoomID]bool
	seededAt time.Time
	seeded   bool
	now      func() time.Time
	// established memoizes rooms known to have history (a room never loses it).
	established map[domain.RoomID]bool
}

func newSpamWatch(store spamStore) *spamWatch {
	return &spamWatch{
		store:       store,
		stored:      map[domain.RoomID]bool{},
		released:    map[domain.RoomID]bool{},
		recorded:    map[domain.RoomID]bool{},
		established: map[domain.RoomID]bool{},
		now:         time.Now,
	}
}

// spamReseed is how long the memo of caught and released rooms is trusted.
const spamReseed = time.Minute

// reload replaces the rules and hand-written lists and marks the memo stale.
func (w *spamWatch) reload(rules domain.SpamRules, places domain.Spam) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rules, w.places = rules, places
	w.seeded = false
}

// spam reports whether the message's room is in Spam, already or as of this message.
// Precedence: `[spam] except` > hand-written list > release > recorded > rules.
func (w *spamWatch) spam(ctx context.Context, msg domain.Message, facts domain.RoomFacts) bool {
	w.mu.Lock()
	rules, places := w.rules, w.places
	fresh := w.seeded && w.now().Sub(w.seededAt) < spamReseed
	w.mu.Unlock()

	if places.Excused(facts) {
		return false
	}
	if places.Names(facts) {
		return true
	}
	if !fresh {
		w.seed(ctx)
	}
	w.mu.Lock()
	released := w.released[msg.RoomID]
	already := w.stored[msg.RoomID] || w.recorded[msg.RoomID]
	w.mu.Unlock()
	if released {
		return false
	}
	if already {
		return true
	}
	if !rules.Any() {
		return false
	}
	verdict, promoted := w.judge(ctx, msg, facts, rules)
	if !promoted {
		return false
	}
	return w.record(ctx, verdict)
}

// judge runs the rules over one message, cheapest first.
func (w *spamWatch) judge(
	ctx context.Context, msg domain.Message, facts domain.RoomFacts, rules domain.SpamRules,
) (domain.SpamVerdict, bool) {
	caught := rules.Catching(msg.Body, msg.Sender, facts)
	if len(caught) == 0 {
		return domain.SpamVerdict{}, false
	}
	// One history read answers every room question the rules ask.
	history, err := w.history(ctx, msg)
	if err != nil {
		// Unknown must never read as "new and unspoken-in", which the strongest rules fire on.
		w.warn(ctx, "spam: read the room's history failed; not judging this message", err, "room", msg.RoomID, "event", msg.ID)
		return domain.SpamVerdict{}, false
	}
	place := domain.SpamCase{
		Filters: caught, Direct: facts.Direct,
		First: history.first, Mine: history.mine,
	}
	if place.RatioWanted() {
		place.Caught, place.Total = w.window(history.msgs, rules, facts)
	}
	rule, filter, promoted := rules.Promotes(place)
	if !promoted {
		return domain.SpamVerdict{}, false
	}
	return domain.SpamVerdict{
		Room: msg.RoomID, Rule: rule, Filter: filter.Name, At: time.Now(),
	}, true
}

type roomHistory struct {
	msgs  []domain.Message
	first bool
	mine  bool
}

// history reads what a room has said. Only "has history" is memoized: a room that
// looks new may just not be fetched yet.
func (w *spamWatch) history(ctx context.Context, msg domain.Message) (roomHistory, error) {
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

func (w *spamWatch) warn(ctx context.Context, msg string, err error, attrs ...any) {
	if err == nil || w.log == nil {
		return
	}
	w.log.Log(ctx, levelFor(ctx), msg, append([]any{"err", err}, attrs...)...)
}

// window counts how much of a room's recent conversation the ratio filters catch.
func (w *spamWatch) window(msgs []domain.Message, rules domain.SpamRules, room domain.RoomFacts) (caught, total int) {
	since := time.Now().Add(-rules.Window)
	for i := range msgs {
		if msgs[i].IsUpdate() || msgs[i].Timestamp.Before(since) {
			continue
		}
		total++
		if rules.CountsForRatio(msgs[i].Body, msgs[i].Sender, room) {
			caught++
		}
	}
	return caught, total
}

// record writes the verdict to account data and memory, reporting whether it is in
// force (not if the room was released elsewhere).
func (w *spamWatch) record(ctx context.Context, verdict domain.SpamVerdict) bool {
	err := w.store.MarkSpam(ctx, verdict)
	w.mu.Lock()
	defer w.mu.Unlock()
	if errors.Is(err, domain.ErrSpamReleased) {
		w.released[verdict.Room] = true
		return false
	}
	// Any other failure keeps the verdict for this process only: it will not survive a
	// restart or reach other devices.
	w.warn(ctx, "spam: record the verdict failed; it holds until restart only", err, "room", verdict.Room, "rule", verdict.Rule)
	w.recorded[verdict.Room] = true
	return true
}

// rescan judges each room's cached messages since since by the rules that hold for an
// old message: not first_message, since the cache may not reach a room's start. The
// first message that moves a room is its sample. With apply it records the verdicts,
// for only the rooms in only when that is not empty.
func (w *spamWatch) rescan(
	ctx context.Context, rooms []domain.RoomFacts, since time.Time, apply bool, only []domain.RoomID,
) ([]domain.SpamCandidate, error) {
	w.seed(ctx)
	w.mu.Lock()
	rules, places := w.rules, w.places
	w.mu.Unlock()
	var out []domain.SpamCandidate
	for i := range rooms {
		if err := ctx.Err(); err != nil {
			return out, fmt.Errorf("daemon: rescan stopped: %w", err)
		}
		facts := rooms[i]
		id := domain.RoomID(facts.ID)
		if len(only) > 0 && !slices.Contains(only, id) {
			continue
		}
		if !rules.Any() || places.Excused(facts) || places.Names(facts) || w.settled(id) {
			continue
		}
		found, ok, err := w.judgeHistory(ctx, facts, rules, since)
		if err != nil {
			return out, err
		}
		if !ok || (apply && !w.record(ctx, found.Verdict)) {
			continue
		}
		out = append(out, found)
	}
	return out, nil
}

// settled reports whether a room is already in Spam by a verdict, or released.
func (w *spamWatch) settled(room domain.RoomID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.released[room] || w.stored[room] || w.recorded[room]
}

// judgeHistory finds the oldest message since since that moves room, oldest first.
func (w *spamWatch) judgeHistory(
	ctx context.Context, room domain.RoomFacts, rules domain.SpamRules, since time.Time,
) (domain.SpamCandidate, bool, error) {
	msgs, err := w.store.CachedTimeline(ctx, domain.RoomID(room.ID))
	if err != nil {
		return domain.SpamCandidate{}, false, fmt.Errorf("daemon: read %s to rescan it: %w", room.ID, err)
	}
	mine := slices.ContainsFunc(msgs, func(m domain.Message) bool {
		return !m.IsUpdate() && w.mine != nil && w.mine(m.Sender)
	})
	slices.SortStableFunc(msgs, func(a, b domain.Message) int { return a.Timestamp.Compare(b.Timestamp) })
	counted := false
	place := domain.SpamCase{Direct: room.Direct, Mine: mine}
	for i := range msgs {
		m := msgs[i]
		if m.IsUpdate() || m.Timestamp.Before(since) || (w.mine != nil && w.mine(m.Sender)) {
			continue
		}
		place.Filters = rules.Catching(m.Body, m.Sender, room)
		if len(place.Filters) == 0 {
			continue
		}
		if place.RatioWanted() && !counted {
			place.Caught, place.Total = w.window(msgs, rules, room)
			counted = true
		}
		rule, filter, promoted := rules.Promotes(place)
		if promoted {
			verdict := domain.SpamVerdict{Room: domain.RoomID(room.ID), Rule: rule, Filter: filter.Name, At: time.Now()}
			return domain.SpamCandidate{Verdict: verdict, Sample: m}, true, nil
		}
	}
	return domain.SpamCandidate{}, false, nil
}

// seed rereads caught and released rooms from the cache; on failure it stays stale
// so the next message retries.
func (w *spamWatch) seed(ctx context.Context) {
	verdicts, err := w.store.SpamRooms(ctx)
	if err != nil {
		w.warn(ctx, "spam: reread the caught rooms failed; using the stale set", err)
		return
	}
	stored := make(map[domain.RoomID]bool, len(verdicts))
	released := map[domain.RoomID]bool{}
	for i := range verdicts {
		switch {
		case verdicts[i].Released:
			released[verdicts[i].Room] = true
		case verdicts[i].Spam():
			stored[verdicts[i].Room] = true
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stored, w.released = stored, released
	// A release outranks what this process recorded before hearing of it.
	for room := range released {
		delete(w.recorded, room)
	}
	w.seededAt, w.seeded = w.now(), true
}
