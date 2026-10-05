package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// spamStore is what promoting a room into Spam needs from the backend. Promotion
// lives in the daemon because the first-message rule fires on a message that
// arrives once, usually while no client is open; the verdict goes to room account
// data (matrix/spam.go) so it survives rebuilds and reaches other machines.
type spamStore interface {
	MarkSpam(ctx context.Context, verdict domain.SpamVerdict) error
	SpamRooms(ctx context.Context) ([]domain.SpamVerdict, error)
	CachedTimeline(ctx context.Context, roomID domain.RoomID) ([]domain.Message, error)
}

// spamWatch decides whether a message moves its room into Spam, and remembers what
// it decided. The set is held in memory, seeded from the cache.
type spamWatch struct {
	store spamStore
	// log hears reads and writes that failed; nil is silent. Set before use.
	log *slog.Logger
	// mine, when set, is whether a sender is this person (Notifications.UseSelves):
	// have you ever written here separates a conversation from a broadcast.
	mine func(sender string) bool

	mu     sync.Mutex
	rules  domain.SpamRules
	places domain.Spam
	// stored/released mirror the account data, re-read after spamReseed or a reload
	// (a release made on another machine arrives only by sync).
	stored   map[domain.RoomID]bool
	released map[domain.RoomID]bool
	// recorded is what this process promoted; kept apart from stored so a re-read
	// cannot drop a verdict whose write failed.
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

// reload replaces the rules and hand-written lists, and marks the memo stale.
func (w *spamWatch) reload(rules domain.SpamRules, places domain.Spam) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rules, w.places = rules, places
	w.seeded = false
}

// spam reports whether this message's room is in Spam, already or as of this
// message. Precedence: `[spam] except` > hand-written list > account-data release >
// recorded verdicts > rules.
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
	filter, caught := rules.Catches(msg.Body, msg.Sender)
	if !caught {
		return domain.SpamVerdict{}, false
	}
	// One history read answers every room question the rules ask.
	history, err := w.history(ctx, msg)
	if err != nil {
		// Unknown must never read as "new and unspoken-in" — what the strongest rules fire on.
		w.warn(ctx, "spam: read the room's history failed; not judging this message", err, "room", msg.RoomID, "event", msg.ID)
		return domain.SpamVerdict{}, false
	}
	place := domain.SpamCase{
		Filter: filter.Name, Direct: facts.Direct,
		First: history.first, Mine: history.mine,
	}
	if rules.Ratio > 0 {
		place.Caught, place.Total = w.window(history.msgs, rules)
	}
	rule, promoted := rules.Promotes(place)
	if !promoted {
		return domain.SpamVerdict{}, false
	}
	return domain.SpamVerdict{
		Room: msg.RoomID, Rule: rule, Filter: filter.Name, At: time.Now(),
	}, true
}

// roomHistory is what came before one message.
type roomHistory struct {
	msgs  []domain.Message
	first bool
	mine  bool
}

// history reads what a room has said. Only "has history" is memoized: a room that
// looks new may just not have been fetched yet.
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

// warn logs a failed step; nil err or no logger is silent.
func (w *spamWatch) warn(ctx context.Context, msg string, err error, attrs ...any) {
	if err == nil || w.log == nil {
		return
	}
	w.log.Log(ctx, levelFor(ctx), msg, append([]any{"err", err}, attrs...)...)
}

// window counts how much of a room's recent conversation the filters catch.
func (w *spamWatch) window(msgs []domain.Message, rules domain.SpamRules) (caught, total int) {
	since := time.Now().Add(-rules.Window)
	for i := range msgs {
		if msgs[i].IsUpdate() || msgs[i].Timestamp.Before(since) {
			continue
		}
		total++
		if _, hit := rules.Catches(msgs[i].Body, msgs[i].Sender); hit {
			caught++
		}
	}
	return caught, total
}

// record writes the verdict to account data and memory, and reports whether it is
// in force: not if the room was released elsewhere (remembered as released).
func (w *spamWatch) record(ctx context.Context, verdict domain.SpamVerdict) bool {
	err := w.store.MarkSpam(ctx, verdict)
	w.mu.Lock()
	defer w.mu.Unlock()
	if errors.Is(err, domain.ErrSpamReleased) {
		w.released[verdict.Room] = true
		return false
	}
	// Any other failure leaves the verdict in force for this process, but it will
	// not survive a restart or reach other devices: worth a warning.
	w.warn(ctx, "spam: record the verdict failed; it holds until restart only", err, "room", verdict.Room, "rule", verdict.Rule)
	w.recorded[verdict.Room] = true
	return true
}

// seed rereads caught and released rooms from the cache; a failure leaves it stale
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
	// A release outranks what this process recorded before it heard of it.
	for room := range released {
		delete(w.recorded, room)
	}
	w.seededAt, w.seeded = w.now(), true
}
