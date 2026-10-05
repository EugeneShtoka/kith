package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// Notifications decides and delivers notifications — the daemon sees every message
// whether or not a TUI is attached. It holds no per-client state on purpose.
// Config-derived state behind mu is replaced wholesale by Reload.
type Notifications struct {
	scope *scopeIndex
	sinks Sinks

	// syncs counts sync responses processed; see caughtUp.
	syncs atomic.Int64

	// limiter has its own lock and outlives any one config.
	limiter notify.Limiter

	mu       sync.Mutex
	limit    notify.Limit
	rules    []notify.Rule
	notifier notify.Notifier
	temps    notify.Temps
	autocopy domain.AutoCopy
	clip     clipboard
	// tracked is the word list as rules; trackedNotify the global switch a rule's
	// own `notify` may override (see domain.TrackedNotifies).
	tracked       []domain.TrackedRule
	trackedNotify bool
	// selves are the IDs that are this person, asked per message (see UseSelves).
	selves func() []string
	// spam has its own lock and outlives reloads: a caught room stays caught.
	spam *spamWatch
	// log hears what Deliver cannot return (see UseLogger); nil is silent.
	log *slog.Logger
}

// UseSelves sets every ID that is this person, on every network: each network's own
// accounts and the IDs a bridge posts as for them (an account logging in later
// included, hence a function). Their messages are ours: they never notify, and they
// make a room a conversation for the spam rules. Without it nobody is. Call before
// Run.
func (n *Notifications) UseSelves(selves func() []string) {
	n.selves = selves
	if n.spam != nil {
		n.spam.mine = n.isMine
	}
}

// isMine reports whether a sender is this person.
func (n *Notifications) isMine(sender string) bool {
	return sender != "" && n.selves != nil && slices.Contains(n.selves(), sender)
}

// UseLogger sets where failed deliveries, copies and cache reads are logged. Call
// before Run; nil keeps the silent default.
func (n *Notifications) UseLogger(log *slog.Logger) {
	if log == nil {
		return
	}
	n.log = log
	n.scope.log = log
	if n.spam != nil {
		n.spam.log = log
	}
}

// levelFor is warn, or debug while ctx is ending: our own shutdown is not a failure.
func levelFor(ctx context.Context) slog.Level {
	if ctx.Err() != nil {
		return slog.LevelDebug
	}
	return slog.LevelWarn
}

// tracksAWordIn reports whether the body carries a tracked word that may interrupt.
func (n *Notifications) tracksAWordIn(msg domain.Message, room domain.RoomFacts) bool {
	n.mu.Lock()
	rules, fallback := n.tracked, n.trackedNotify
	n.mu.Unlock()
	if len(rules) == 0 {
		return false
	}
	words := domain.TrackedFor(rules, room, msg.Sender)
	if !words.Any() {
		return false
	}
	return domain.TrackedNotifies(rules, words.Find(msg.Body), fallback, room, msg.Sender)
}

// Sinks builds the delivery sinks for a configuration (setup.Notifier in the
// daemon). Reload rebuilds them, so the builder outlives any one set.
type Sinks func(config.Notifications) notify.Notifier

// NewNotifications builds the notifier from cfg; UseSelves says whose messages are
// ours. A config that will not parse is refused.
func NewNotifications(cfg config.Config, src scopeSource, sinks Sinks) (*Notifications, error) {
	n := &Notifications{
		scope: newScopeIndex(src, cfg.Display.Names, homeOrderOf(cfg)),
		sinks: sinks,
	}
	// A source without spam storage leaves the spam feature off.
	if store, ok := src.(spamStore); ok {
		n.spam = newSpamWatch(store)
	}
	if err := n.Reload(cfg); err != nil {
		return nil, err
	}
	return n, nil
}

// Reload replaces the config-derived state — rules, sinks, room aliases — or returns
// the error that stopped it, changing nothing. Temporary (DND) rules are runtime
// state and are left untouched.
func (n *Notifications) Reload(cfg config.Config) error {
	rules, err := setup.NotificationRules(cfg.Notifications)
	if err != nil {
		return err
	}
	limit, err := setup.NotificationLimit(cfg.Notifications)
	if err != nil {
		return err
	}
	autocopy, err := setup.AutoCopy(cfg)
	if err != nil {
		return err
	}
	spam, err := setup.SpamRules(cfg.Spam)
	if err != nil {
		return err
	}
	notifier := n.sinks(cfg.Notifications)

	// Not a reset: a room caught yesterday stays caught through a reload.
	if n.spam != nil {
		n.spam.reload(spam, setup.SpamPlaces(cfg.Spam))
	}

	n.scope.SetAliases(cfg.Display.Names)
	n.scope.SetHomeOrder(homeOrderOf(cfg))
	// Validated already (Reload refuses a config that will not parse).
	tags, _, _ := setup.Tags(cfg)
	n.scope.SetTags(tags)
	archives, _ := setup.Archives(cfg, tags)
	n.scope.SetArchives(archives)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.rules, n.notifier, n.limit = rules, notifier, limit
	n.autocopy, n.clip = autocopy, clipboard{command: cfg.Clipboard.Command}
	n.tracked, n.trackedNotify = setup.TrackedRules(cfg.Display.Tracked), cfg.Display.Tracked.Notify
	return nil
}

// SetDND puts one temporary rule in force, replacing one naming the same thing, and
// returns the resulting set.
func (n *Notifications) SetDND(r notify.Rule) notify.Temps {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.temps = n.temps.Add(r).Live(time.Now())
	return n.temps
}

// ClearDND lifts the rule naming this place and person, and returns what is left.
func (n *Notifications) ClearDND(match, sender string) notify.Temps {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.temps = n.temps.Remove(match, sender).Live(time.Now())
	return n.temps
}

// ClearAllDND lifts every temporary rule.
func (n *Notifications) ClearAllDND() notify.Temps {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.temps = nil
	return nil
}

// DND reports the temporary rules in force, lapsed ones dropped.
func (n *Notifications) DND() notify.Temps {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.temps = n.temps.Live(time.Now())
	return n.temps
}

// Synced records a sync response arriving. mautrix runs it before that response's
// events, so messages handled while the count is one are the catch-up batch.
func (n *Notifications) Synced(time.Time) { n.syncs.Add(1) }

// caughtUp reports whether the daemon is past its catch-up batch; nothing is
// announced retroactively.
func (n *Notifications) caughtUp() bool { return n.syncs.Load() > 1 }

// InvalidateScope drops the room/space index, e.g. after a refresh has landed.
func (n *Notifications) InvalidateScope() { n.scope.Invalidate() }

// Deliver acts on one incoming message: it captures a verification code if
// configured, then raises a notification if one is earned, and reports it.
//
// It runs synchronously on the stream pump, which serve joins on shutdown, so every
// sink must be bounded (command hooks are not waited on, D-Bus has a deadline, the
// clipboard has copyTimeout) — a wedged notification daemon once held SIGTERM.
func (n *Notifications) Deliver(ctx context.Context, msg domain.Message) (notify.Notification, bool) {
	now := time.Now()
	scope, _ := n.scope.Scope(ctx, msg.RoomID, msg.Sender, msg.ThreadRoot)
	mine := n.isMine(msg.Sender)

	n.captureCode(ctx, msg, mine)

	// Spam is a place, not a policy: a room in it never notifies. Asked after code
	// capture, and it promotes the room as a side effect.
	if n.spam != nil && !mine && n.spam.spam(ctx, msg, n.scope.Facts(ctx, msg.RoomID)) {
		return notify.Notification{}, false
	}

	alert, ok := n.decide(ctx, msg, scope, mine, now)
	if !ok {
		return notify.Notification{}, false
	}
	alert, ok = n.rated(alert, string(msg.RoomID), now)
	if !ok {
		return notify.Notification{}, false
	}
	n.notify(alert)
	return alert, true
}

// rated applies the per-room burst limit. Past the burst messages are counted, not
// dropped, and summarized in one popup that Replaces itself per room.
func (n *Notifications) rated(alert notify.Notification, room string, now time.Time) (notify.Notification, bool) {
	n.mu.Lock()
	limit := n.limit
	n.mu.Unlock()

	switch decision, held := n.limiter.Allow(room, limit, now); decision {
	case notify.Deliver:
		return alert, true
	case notify.Summarize:
		// About the room now, not one message: no sender or body, and no sound.
		return notify.Notification{
			Room:     alert.Room,
			Space:    alert.Space,
			Protocol: alert.Protocol,
			Sent:     alert.Sent,
			Body:     newMessages(held),
			Silent:   true,
			Replaces: room,
		}, true
	case notify.Hold:
		return notify.Notification{}, false
	}
	return alert, true
}

// newMessages is what a summary says.
func newMessages(held int) string {
	if held == 1 {
		return "1 new message"
	}
	return fmt.Sprintf("%d new messages", held)
}

// notify hands an alert to the sinks that are configured right now.
func (n *Notifications) notify(alert notify.Notification) {
	n.mu.Lock()
	notifier := n.notifier
	n.mu.Unlock()
	notifier.Notify(alert)
}

// decide is Deliver's trigger, taking scope and clock as arguments for testing.
func (n *Notifications) decide(ctx context.Context, msg domain.Message, scope notify.Scope, mine bool, now time.Time) (notify.Notification, bool) {
	// Edits and redaction markers never interrupt (a marker has no sender or body).
	if msg.IsUpdate() {
		return notify.Notification{}, false
	}
	if !n.caughtUp() {
		return notify.Notification{}, false
	}
	n.mu.Lock()
	rules := notify.Rules(n.rules, n.temps)
	n.mu.Unlock()

	facts := n.scope.Facts(ctx, msg.RoomID)
	event := notify.Event{
		Mine:      mine,
		Mentioned: msg.Mentioned,
		Direct:    n.scope.Direct(ctx, msg.RoomID),
		Tracked:   n.tracksAWordIn(msg, facts),
	}
	outcome := notify.Resolve(rules, scope, now).Decide(event)
	if !outcome.Notify {
		return notify.Notification{}, false
	}
	sender := msg.SenderName
	if sender == "" {
		sender = msg.Sender
	}
	// A room can be in several spaces and tags; {space} is the first by priority.
	space := n.scope.Home(facts)
	return notify.Notification{
		Sender: sender,
		MXID:   msg.Sender,
		Room:   facts.Name,
		Space:  space,
		// NotifyBody: what the sender covered up stays covered.
		Body:     truncateRunes(msg.NotifyBody(), notifyBodyRunes),
		Protocol: domain.ProtocolOf(msg.Sender).String(),
		Sent:     msg.Timestamp,
		Sound:    outcome.Sound,
		Silent:   outcome.Silent,
	}, true
}

// notifyBodyRunes caps how much of a message reaches a desktop popup.
const notifyBodyRunes = 140

// truncateRunes shortens s to at most n runes, appending an ellipsis when cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Run delivers a notification for every message on the fan-out, in order, until ctx
// is canceled or the stream ends. It blocks.
func (n *Notifications) Run(ctx context.Context, s *Streams) {
	id, msgs := s.SubscribeMessages()
	defer s.ReleaseMessages(id)
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgs:
			if !ok {
				return
			}
			n.Deliver(ctx, msg)
		}
	}
}

// homeOrderOf is the config's part of the home order (setup.PlacesOf's); the spaces'
// part is added as the index reads them.
func homeOrderOf(cfg config.Config) domain.HomeOrder { return setup.PlacesOf(cfg).Order }
