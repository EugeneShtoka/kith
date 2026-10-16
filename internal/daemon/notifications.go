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

// Notifications decides and delivers notifications; the daemon sees every message
// whether or not a TUI is attached. Config-derived state behind mu is replaced by Reload.
type Notifications struct {
	// members reads a person's current name in a room; nil knows none.
	members func(ctx context.Context, room domain.RoomID, user string) (string, error)
	// hides reports whether a message is from a hidden person or answers one; nil hides none.
	hides func(ctx context.Context, msg domain.Message) (bool, error)
	scope *scopeIndex
	sinks Sinks

	// syncs counts sync responses processed; see caughtUp.
	syncs atomic.Int64

	// limiter has its own lock and outlives any one config.
	limiter notify.Limiter

	mu sync.Mutex
	// standIns are unannounced bridge placeholders by message and arrival time; the edit
	// replacing one is announced instead (placeholderSpan).
	standIns map[domain.EventID]time.Time
	// keep and keepRules are [storage] messages_per_room and its rules.
	keep      int
	keepRules []domain.KeepRule
	limit     notify.Limit
	rules     []notify.Rule
	notifier  notify.Notifier
	clock     domain.Clock // how a notification writes its {date} and {time}
	// aliases are the person's names for people, firstNames whether a home shows first
	// names only, roomRules whether a direct chat's label is shaped too.
	aliases    map[string]string
	firstNames func(home string) bool
	roomRules  bool
	temps      notify.Temps
	autocopy   domain.AutoCopy
	clip       clipboard
	// tracked is the word list as rules; trackedNotify the global switch a rule's
	// `notify` may override.
	tracked       []domain.TrackedRule
	trackedNotify bool
	// selves are the IDs that are this person, asked per message.
	selves func() []string
	// people reads the directory; nil knows nobody.
	people func(context.Context) (domain.Directory, error)
	// filters has its own lock and outlives reloads: a moved room stays moved.
	filters *filterWatch
	// log hears what Deliver cannot return; nil is silent.
	log *slog.Logger
}

// UseSelves sets every ID that is this person on any network, including bridge
// puppets; a function because accounts may log in later. Their messages never
// notify and make a room a conversation for the filters. Call before Run.
func (n *Notifications) UseSelves(selves func() []string) {
	n.selves = selves
	if n.filters != nil {
		n.filters.mine, n.filters.selves = n.isMine, selves
	}
}

// UseMemberNames sets where a person's current name in a room is read; a name in a
// message may be stale. Call before Run.
func (n *Notifications) UseMemberNames(names func(ctx context.Context, room domain.RoomID, user string) (string, error)) {
	n.members = names
}

// UseHidden sets how a message from a hidden person (a muted tag's people), or a
// direct reply to one, is recognized. Call before Run.
func (n *Notifications) UseHidden(hides func(ctx context.Context, msg domain.Message) (bool, error)) {
	n.hides = hides
}

// hidden reports whether msg is hidden; a failed read hides nothing.
func (n *Notifications) hidden(ctx context.Context, msg domain.Message) bool {
	if n.hides == nil {
		return false
	}
	hidden, err := n.hides(ctx, msg)
	if err != nil {
		n.log.Warn("ask whether a message is hidden", "err", err)
		return false
	}
	return hidden
}

// memberName is user's name in room now, "" when unknown.
func (n *Notifications) memberName(ctx context.Context, room domain.RoomID, user string) string {
	if n.members == nil || user == "" {
		return ""
	}
	name, err := n.members(ctx, room, user)
	if err != nil {
		n.log.Warn("read a member's name", "err", err)
	}
	return name
}

// UseDirectory sets where people are named from (domain.Directory). Call before Run.
func (n *Notifications) UseDirectory(people func(context.Context) (domain.Directory, error)) {
	n.people = people
	if n.filters != nil {
		n.filters.directory = people
	}
}

// lazyDirectory reads the directory the first time a notification needs it.
type lazyDirectory struct {
	dir  domain.Directory
	read bool
}

func (n *Notifications) directory(ctx context.Context, once *lazyDirectory) domain.Directory {
	if !once.read && n.people != nil {
		once.read = true
		dir, err := n.people(ctx)
		if err != nil {
			n.log.Warn("read the directory", "err", err)
		}
		once.dir = dir
	}
	return once.dir
}

// byNumber is label, or the directory's name for it when label is only a number.
func (n *Notifications) byNumber(ctx context.Context, once *lazyDirectory, label string) string {
	if _, isNumber := domain.PhoneIn(label); !isNumber {
		return label
	}
	if name, ok := n.directory(ctx, once).Named(label); ok {
		return name
	}
	return label
}

// namer names people as the timeline does: alias, network name, directory name,
// first names only where home's rule says so.
func (n *Notifications) namer(ctx context.Context, once *lazyDirectory, home string) func(user string, known ...string) string {
	n.mu.Lock()
	aliases, first := n.aliases, n.firstNamesLocked(home)
	n.mu.Unlock()
	return func(user string, known ...string) string {
		name := setup.PeopleOf(aliases, n.directory(ctx, once)).Name(user, known...)
		if first {
			name = domain.FirstName(name)
		}
		return name
	}
}

// firstNamesIn reports whether home's names are first names only.
func (n *Notifications) firstNamesIn(home string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.firstNamesLocked(home)
}

func (n *Notifications) firstNamesLocked(home string) bool {
	return n.firstNames != nil && n.firstNames(home)
}

func (n *Notifications) isMine(sender string) bool {
	return sender != "" && n.selves != nil && slices.Contains(n.selves(), sender)
}

// UseLogger sets where failed deliveries, copies and cache reads are logged; nil is
// silent. Call before Run.
func (n *Notifications) UseLogger(log *slog.Logger) {
	if log == nil {
		return
	}
	n.log = log
	n.scope.log = log
	if n.filters != nil {
		n.filters.log = log
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

// Sinks builds the delivery sinks for a configuration; Reload rebuilds them.
type Sinks func(config.Notifications) notify.Notifier

// NewNotifications builds the notifier from cfg, refusing a config that will not parse.
func NewNotifications(cfg config.Config, src scopeSource, sinks Sinks) (*Notifications, error) {
	n := &Notifications{
		scope: newScopeIndex(src, cfg.Display.Names, homeOrderOf(cfg)),
		sinks: sinks,
	}
	// A source with nowhere to keep moves leaves the filters off.
	if store, ok := src.(filterStore); ok {
		n.filters = newFilterWatch(store)
		n.scope.caught = n.filters.caught
		n.filters.facts = n.scope.Facts
		if people, ok := src.(personStore); ok {
			n.filters.people = people
		}
	}
	if err := n.Reload(cfg); err != nil {
		return nil, err
	}
	return n, nil
}

// Reload replaces the config-derived state, or changes nothing and returns the error.
// Temporary (DND) rules are runtime state and survive.
func (n *Notifications) Reload(cfg config.Config) error {
	rules, err := setup.NotificationRules(cfg.Notifications)
	if err != nil {
		return err
	}
	keep, keepRules, err := setup.KeepRules(cfg.Storage)
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
	filters, err := setup.Filters(cfg)
	if err != nil {
		return err
	}
	clock, err := setup.Clock(cfg.Display)
	if err != nil {
		return err
	}
	notifier := n.sinks(cfg.Notifications)

	n.scope.SetAliases(cfg.Display.Names)
	n.scope.SetHomeOrder(homeOrderOf(cfg))
	// Already validated by Reload.
	tags, _, _ := setup.Tags(cfg)
	n.scope.SetTags(tags)
	// A moved room stays moved through a reload.
	if n.filters != nil {
		_, _, groups, _ := setup.HiddenInputs(cfg)
		n.filters.reload(filters, tags, groups)
	}
	archives, _ := setup.Archives(cfg, tags)
	n.scope.SetArchives(archives)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.rules, n.notifier, n.limit, n.clock = rules, notifier, limit, clock
	display := cfg.Display
	n.aliases = setup.Aliases(display.Identities)
	n.firstNames = func(home string) bool { return setup.FirstNamesIn(display, home) }
	n.roomRules = cfg.Display.ApplyRoomNameRules()
	n.keep, n.keepRules = keep, keepRules
	n.autocopy, n.clip = autocopy, clipboard{command: cfg.Clipboard.Command}
	n.tracked, n.trackedNotify = setup.TrackedRules(cfg.Display.Tracked), cfg.Display.Tracked.Notify
	return nil
}

// RescanFilters judges cached messages since since, as if they arrived now, and returns
// the rooms that would move into a tag. With apply it moves them, only those in rooms
// when that is not empty, and returns the ones it moved.
func (n *Notifications) RescanFilters(ctx context.Context, since time.Time, apply bool, rooms []domain.RoomID) ([]domain.MoveCandidate, error) {
	if n.filters == nil {
		return nil, nil
	}
	all, err := n.scope.All(ctx)
	if err != nil {
		return nil, err
	}
	return n.filters.rescan(ctx, all, since, apply, rooms)
}

// MoveWithHidden moves each of rooms that is a direct message, never written in, with
// someone a muted tag hides into that tag. kithd calls it with the rooms of people just
// hidden.
func (n *Notifications) MoveWithHidden(ctx context.Context, rooms []domain.RoomID) {
	if n.filters == nil {
		return
	}
	for _, room := range rooms {
		n.filters.moveWithPerson(ctx, room)
	}
}

// RescanPeople judges the senders of cached messages since since, as if they arrived
// then, and returns the people the filters' sender moves would mark. With apply it
// marks them, only those in people when that is not empty, and returns the ones it
// marked.
func (n *Notifications) RescanPeople(ctx context.Context, since time.Time, apply bool, people []string) ([]domain.PersonCandidate, error) {
	if n.filters == nil {
		return nil, nil
	}
	all, err := n.scope.All(ctx)
	if err != nil {
		return nil, err
	}
	return n.filters.rescanPeople(ctx, all, since, apply, people)
}

// SetDND puts a temporary rule in force, replacing one naming the same thing.
func (n *Notifications) SetDND(r notify.Rule) notify.Temps {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.temps = n.temps.Add(r).Live(time.Now())
	return n.temps
}

// ClearDND lifts the rule naming this place and person.
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

// MessagesKept is how many messages the cache keeps of a room, negative for all.
// Called on every cache write, so it never rebuilds the index (which reads the
// cache): a room not yet indexed is judged by its ID and network alone.
func (n *Notifications) MessagesKept(roomID domain.RoomID) int {
	n.mu.Lock()
	keep, rules := n.keep, n.keepRules
	n.mu.Unlock()
	if len(rules) == 0 {
		return keep
	}
	return domain.MessagesKept(keep, rules, n.scope.Known(roomID))
}

// Deliver captures a verification code if configured, then raises a notification
// if one is earned. It runs on the stream pump, which shutdown joins, so every sink
// must be bounded; a wedged notification daemon once held SIGTERM.
func (n *Notifications) Deliver(ctx context.Context, msg domain.Message) (notify.Notification, bool) {
	now := time.Now()
	mine := n.isMine(msg.Sender)

	n.captureCode(ctx, msg, mine)
	var news bool
	if msg, news = n.instead(msg, now); !news {
		return notify.Notification{}, false
	}

	// A hidden person's message, or a reply to one, never notifies, nor moves the room
	// by a filter: their room is not theirs. Asked after code capture.
	if !mine && n.hidden(ctx, msg) {
		// Their direct message, though, moves where they are.
		if n.filters != nil {
			n.filters.moveWithPerson(ctx, msg.RoomID)
		}
		return notify.Notification{}, false
	}
	// The filters run first, so the scope sees the tags they moved the room into.
	// A sender they just marked is hidden from this message on.
	if n.filters != nil && !mine && n.filters.watch(ctx, msg, n.scope.Facts(ctx, msg.RoomID)) && n.hidden(ctx, msg) {
		return notify.Notification{}, false
	}
	// A room a tag with notifies = false holds never notifies. Asked after code capture.
	if n.scope.Mutes(n.scope.Facts(ctx, msg.RoomID)) {
		return notify.Notification{}, false
	}
	scope, _ := n.scope.Scope(ctx, msg.RoomID, msg.Sender, msg.ThreadRoot)

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

// placeholderSpan is how long a bridge placeholder waits for its replacing edit.
const placeholderSpan = 10 * time.Minute

// instead is what a message announces: nothing for a bridge placeholder (kept a
// while), and for the edit replacing one, the edited message as new.
func (n *Notifications) instead(msg domain.Message, now time.Time) (domain.Message, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, at := range n.standIns {
		if now.Sub(at) > placeholderSpan {
			delete(n.standIns, id)
		}
	}
	if msg.Placeholder && !msg.IsUpdate() {
		if n.standIns == nil {
			n.standIns = map[domain.EventID]time.Time{}
		}
		n.standIns[msg.ID] = now
		return msg, false
	}
	if _, held := n.standIns[msg.ID]; held && msg.Edited && !msg.Redacted {
		delete(n.standIns, msg.ID)
		msg.Edited, msg.EditedAt, msg.RevisionID = false, time.Time{}, ""
	}
	return msg, true
}

// rated applies the per-room burst limit: past the burst, messages are counted and
// summarized in one popup that replaces itself per room.
func (n *Notifications) rated(alert notify.Notification, room string, now time.Time) (notify.Notification, bool) {
	n.mu.Lock()
	limit := n.limit
	n.mu.Unlock()

	switch decision, held := n.limiter.Allow(room, limit, now); decision {
	case notify.Deliver:
		return alert, true
	case notify.Summarize:
		// About the room, not one message: no sender, body or sound.
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

func newMessages(held int) string {
	if held == 1 {
		return "1 new message"
	}
	return fmt.Sprintf("%d new messages", held)
}

// notify hands an alert to the currently configured sinks.
func (n *Notifications) notify(alert notify.Notification) {
	n.mu.Lock()
	notifier, clock := n.notifier, n.clock
	n.mu.Unlock()
	if !alert.Sent.IsZero() {
		alert.Date, alert.Time = clock.ShortDate(alert.Sent), clock.Time(alert.Sent)
	}
	notifier.Notify(alert)
}

// decide is Deliver's trigger, with scope and clock as arguments.
func (n *Notifications) decide(ctx context.Context, msg domain.Message, scope notify.Scope, mine bool, now time.Time) (notify.Notification, bool) {
	// Edits and redaction markers never interrupt.
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
	// A room can be in several spaces and tags; {space} is the first by priority.
	home := n.scope.FirstHome(facts)
	space := domain.HomeLabel(home)
	var once lazyDirectory // read only when someone is to be named
	name := n.namer(ctx, &once, home)
	room := n.byNumber(ctx, &once, facts.Name)
	if n.roomRules && facts.Direct && n.firstNamesIn(home) {
		room = domain.FirstName(room)
	}
	// NotifyBody keeps what the sender covered up covered.
	body, _ := domain.ResolveMentions(msg.NotifyBody(), msg.Mentions, func(mn domain.Mention, words string) string {
		return name(mn.UserID, n.memberName(ctx, msg.RoomID, mn.UserID), words)
	})
	return notify.Notification{
		Sender:   name(msg.Sender, n.memberName(ctx, msg.RoomID, msg.Sender), msg.SenderName),
		MXID:     msg.Sender,
		Room:     room,
		Space:    space,
		Body:     truncateRunes(body, notifyBodyRunes),
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

// Run delivers a notification for every message on the fan-out until ctx ends or the
// stream closes. It blocks.
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

// homeOrderOf is the config's part of the home order; spaces are added as the index
// reads them.
func homeOrderOf(cfg config.Config) domain.HomeOrder { return setup.PlacesOf(cfg).Order }
