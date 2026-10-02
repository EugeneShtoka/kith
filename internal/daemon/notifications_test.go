package daemon_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// The account the daemon is serving, and the rooms it knows about.
const (
	me      = "@me:example.org"
	alice   = "@alice:example.org"
	bob     = "@bob:example.org"
	dmRoom  = "!dm:example.org"
	chatRm  = "!standup:example.org"
	dmName  = "Alice"
	chatNam = "Standup"
	space   = "Work"
	// The same two places written as *rule entries*.
	chatNamEntry = "room:Standup"
	spaceEntry   = "space:Work"
)

// scopeBackend is the cache the daemon reads a room's name, spaces and DM-ness out
// of. Only Rooms and Spaces matter here; everything else is a Nop.
type scopeBackend struct {
	apitest.Nop

	mu     sync.Mutex
	rooms  []domain.Room
	spaces []domain.Space
	reads  int
	// inThreads are the threads this account has spoken in, by root.
	inThreads map[domain.EventID]bool
}

func (s *scopeBackend) Rooms(context.Context) ([]domain.Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return s.rooms, nil
}

func (s *scopeBackend) Spaces(context.Context) ([]domain.Space, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spaces, nil
}

func (s *scopeBackend) ThreadParticipant(_ context.Context, _ domain.RoomID, root domain.EventID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inThreads[root]
}

func (s *scopeBackend) roomReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// world is the backend every test here resolves scopes against.
func world() *scopeBackend {
	return &scopeBackend{
		rooms: []domain.Room{
			{ID: dmRoom, Name: dmName, IsDirect: true},
			{ID: chatRm, Name: chatNam},
		},
		spaces: []domain.Space{{ID: "!work:example.org", Name: space, Children: []domain.RoomID{chatRm}}},
	}
}

// recorder is a notify.Notifier that keeps what it was handed, so a test can assert
// on delivery rather than on the decision alone.
type recorder struct {
	mu   sync.Mutex
	sent []notify.Notification
}

func (r *recorder) Notify(n notify.Notification) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, n)
}

func (r *recorder) all() []notify.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Notification(nil), r.sent...)
}

// notifsOn is a [notifications] section that notifies at the given level with the
// sinks disabled — the tests substitute a recorder for delivery.
func notifsOn(level string) config.Config {
	return config.Config{Notifications: config.Notifications{Enabled: true, Rules: []config.Rule{{Show: level}}}}
}

// notifier builds the daemon's notifier over world(), with a recorder in place of
// the desktop sinks.
func notifier(t *testing.T, cfg config.Config) (*daemon.Notifications, *recorder, *scopeBackend) {
	t.Helper()

	src, rec := world(), &recorder{}
	n, err := daemon.NewNotifications(cfg, src, me, func(config.Notifications) notify.Notifier { return rec })
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	// Two sync responses: the daemon is past its catch-up batch, so a message arriving now
	// arrived now.
	n.Synced(time.Now())
	n.Synced(time.Now())
	return n, rec, src
}

// msg is one incoming message in a room.
func msg(room domain.RoomID, sender, body string) domain.Message {
	return domain.Message{ID: "$1", RoomID: room, Sender: sender, Body: body, Timestamp: time.Now()}
}

// The trigger levels, decided against the daemon's own room list.
func TestLevelsDecideWhatNotifies(t *testing.T) {
	t.Parallel()

	mention := func(m domain.Message) domain.Message { m.Mentioned = true; return m }
	tests := map[string]struct {
		level string
		msg   domain.Message
		want  bool
	}{
		"none notifies about nothing":    {"none", mention(msg(dmRoom, alice, "hi")), false},
		"our own message never notifies": {"all", msg(dmRoom, me, "hi"), false},
		"mention admits a mention":       {"mention", mention(msg(chatRm, alice, "hi")), true},
		"mention rejects chatter":        {"mention", msg(chatRm, alice, "hi"), false},
		"dm admits a direct message":     {"dm", msg(dmRoom, alice, "hi"), true},
		"dm rejects a group room":        {"dm", msg(chatRm, alice, "hi"), false},
		"all admits everything":          {"all", msg(chatRm, alice, "hi"), true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, rec, _ := notifier(t, notifsOn(tc.level))
			if _, got := n.Deliver(context.Background(), tc.msg); got != tc.want {
				t.Errorf("delivered = %v, want %v", got, tc.want)
			}
			if got := len(rec.all()); (got > 0) != tc.want {
				t.Errorf("%d notifications reached the sink, want %v", got, tc.want)
			}
		})
	}
}

// The notification carries the fields a template renders, resolved from the daemon's own
// cache: the room's name, the sender's, and the protocol its MXID implies.
func TestNotificationCarriesResolvedFields(t *testing.T) {
	t.Parallel()

	n, rec, _ := notifier(t, notifsOn("all"))
	m := msg(chatRm, alice, "stand-up in five")
	m.SenderName = "Alice"
	if _, ok := n.Deliver(context.Background(), m); !ok {
		t.Fatal("a message at level all should notify")
	}
	sent := rec.all()
	if len(sent) != 1 {
		t.Fatalf("%d notifications, want 1", len(sent))
	}
	got := sent[0]
	if got.Room != chatNam {
		t.Errorf("Room = %q, want the name the daemon resolved (%q)", got.Room, chatNam)
	}
	if got.Sender != "Alice" || got.MXID != alice {
		t.Errorf("sender = %q/%q, want Alice/%s", got.Sender, got.MXID, alice)
	}
	if got.Body != "stand-up in five" {
		t.Errorf("Body = %q, want the message text", got.Body)
	}
	if got.Protocol == "" {
		t.Error("Protocol should be derived from the MXID")
	}
}

// A long body is cut to a summons rather than a screenful, and cut by runes so a
// non-ASCII message is not sliced mid-character.
func TestNotificationTruncatesTheBody(t *testing.T) {
	t.Parallel()

	n, _, _ := notifier(t, notifsOn("all"))
	long := strings.Repeat("ש", 400)
	got, ok := n.Deliver(context.Background(), msg(chatRm, alice, long))
	if !ok {
		t.Fatal("a message at level all should notify")
	}
	body := []rune(got.Body)
	if len(body) != 141 || body[140] != '…' {
		t.Errorf("body is %d runes ending %q, want 140 plus an ellipsis", len(body), string(body[len(body)-1]))
	}
}

// A rule narrows the policy for one place or one person, resolved against the
// spaces the daemon reads out of its own cache.
func TestRulesResolveAgainstTheDaemonsRooms(t *testing.T) {
	t.Parallel()

	cfg := notifsOn("none")
	cfg.Notifications.Rules = append(cfg.Notifications.Rules,
		config.Rule{Match: spaceEntry, Show: "all"}, // everything in the Work space
		config.Rule{Sender: bob, Show: "none"},      // …except Bob, wherever he is
	)
	n, _, _ := notifier(t, cfg)

	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); !ok {
		t.Error("a space rule should admit a room in that space")
	}
	if _, ok := n.Deliver(context.Background(), msg(dmRoom, alice, "hi")); ok {
		t.Error("a space rule should not admit a room outside it")
	}
	if _, ok := n.Deliver(context.Background(), msg(chatRm, bob, "hi")); ok {
		t.Error("a sender rule is more specific than a space rule and should win")
	}
}

// The room's alias reaches the notification text, because that is the name the user gave it
// and the one they will recognize.
func TestNotificationUsesTheConfiguredRoomAlias(t *testing.T) {
	t.Parallel()

	cfg := notifsOn("all")
	cfg.Display.Names = []config.DisplayName{{Target: chatRm, Name: "The Team"}}
	n, _, _ := notifier(t, cfg)

	got, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi"))
	if !ok {
		t.Fatal("a message at level all should notify")
	}
	if got.Room != "The Team" {
		t.Errorf("Room = %q, want the alias the user gave it", got.Room)
	}
}

// A rule names a room by ID unless you say otherwise, and a bare name names nothing.
func TestRulesMatchARoomByIDAndRefuseABareName(t *testing.T) {
	t.Parallel()

	withRule := func(match string) config.Config {
		cfg := notifsOn("none")
		cfg.Notifications.Rules = append(cfg.Notifications.Rules, config.Rule{Match: match, Show: "all"})
		return cfg
	}
	n, _, _ := notifier(t, withRule(chatNam))
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); ok {
		t.Error("a bare name matched a room; an entry has to declare its kind")
	}

	// Asked for explicitly, it matches — and the caveat is the user's to carry.
	n, _, _ = notifier(t, withRule(chatNamEntry))
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); !ok {
		t.Error("room:<name> should match the room it names")
	}

	// The ID does match, which is what the app writes.
	n, _, _ = notifier(t, withRule(string(chatRm)))
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); !ok {
		t.Error("a rule naming the room's ID should match it")
	}
}

// Reload replaces the running policy; a config that will not parse is refused, naming
// the section, and the old policy stays in force.
func TestReloadReplacesThePolicy(t *testing.T) {
	t.Parallel()

	n, _, _ := notifier(t, notifsOn("none"))
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); ok {
		t.Fatal("level none should notify about nothing")
	}
	if err := n.Reload(notifsOn("all")); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); !ok {
		t.Error("the reloaded policy should be the one that decides")
	}
	err := n.Reload(notifsOn("mentions")) // not a level: the spelling is "mention"
	if err == nil || !strings.Contains(err.Error(), "show") {
		t.Fatalf("Reload(bad) = %v, want a refusal naming the key", err)
	}
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); !ok {
		t.Error("the previous policy should still be in force after a refused reload")
	}
}

// NewNotifications refuses a bad config outright, naming the key, rather than starting a
// daemon that silently notifies about nothing.
func TestNewNotificationsRefusesABadConfig(t *testing.T) {
	t.Parallel()

	badWindow := notifsOn("all")
	badWindow.Notifications.MaxPerRoom = 3
	badWindow.Notifications.RateWindow = "soon"
	for key, cfg := range map[string]config.Config{
		"show":        notifsOn("everything"),
		"rate_window": badWindow,
	} {
		_, err := daemon.NewNotifications(cfg, world(), me, func(config.Notifications) notify.Notifier { return notify.Nop{} })
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("NewNotifications() = %v, want a refusal naming %s", err, key)
		}
	}
}

// The room index is read once and reused: two SQLite queries per notification would
// put the cache in the path of every popup for nothing.
func TestScopeIsIndexedNotReReadPerMessage(t *testing.T) {
	t.Parallel()

	n, _, src := notifier(t, notifsOn("all"))
	for range 5 {
		n.Deliver(context.Background(), msg(chatRm, alice, "hi"))
	}
	if got := src.roomReads(); got > 1 {
		t.Errorf("the room list was read %d times for 5 messages, want 1", got)
	}
}

// A room the index has never seen forces a re-read — the first message in a room
// you were just invited to is exactly the case a stale index gets wrong.
func TestAnUnknownRoomRebuildsTheIndex(t *testing.T) {
	t.Parallel()

	n, _, src := notifier(t, notifsOn("all"))
	n.Deliver(context.Background(), msg(chatRm, alice, "hi"))
	before := src.roomReads()

	src.mu.Lock()
	src.rooms = append(src.rooms, domain.Room{ID: "!new:example.org", Name: "New"})
	src.mu.Unlock()

	got, ok := n.Deliver(context.Background(), msg("!new:example.org", alice, "hi"))
	if !ok {
		t.Fatal("a message in a new room should still notify")
	}
	if got.Room != "New" {
		t.Errorf("Room = %q, want the newly-read name", got.Room)
	}
	if src.roomReads() <= before {
		t.Error("an unknown room should have forced a re-read rather than waiting out the TTL")
	}
}

// A message notifies with no TUI attached.
func TestNotifiesWithNoClientAttached(t *testing.T) {
	t.Parallel()

	msgs := make(chan domain.Message, 1)
	backend := &scopeBackend{
		rooms:  []domain.Room{{ID: chatRm, Name: chatNam}},
		spaces: nil,
	}
	backend.Msgs = msgs

	streams := daemon.NewStreams()
	ctx := t.Context()

	n, rec, _ := notifier(t, notifsOn("all"))
	go streams.Run(ctx, backend)
	// Subscribe before producing: the hub is a fan-out, not a replay log.
	ready := make(chan struct{})
	go func() { close(ready); n.Run(ctx, streams) }()
	<-ready
	waitFor(t, func() bool { return streams.MessageSubscribers() == 1 })

	if streams.Attached() != 0 {
		t.Fatalf("Attached = %d, want no clients — that is the whole point", streams.Attached())
	}
	msgs <- msg(chatRm, alice, "are you there?")

	waitFor(t, func() bool { return len(rec.all()) == 1 })
	got := rec.all()[0]
	if got.Body != "are you there?" || got.Room != chatNam {
		t.Errorf("notification = %+v, want the message, resolved, with nobody watching", got)
	}
}

// waitFor polls cond until it holds or the test's patience runs out. A round trip
// through the fan-out takes microseconds; anything approaching the bound is a hang.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(settle)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}

// The message stream carries revisions as well as messages, and neither kind of revision is
// a second event worth a second interruption.
func TestRevisionsDoNotNotify(t *testing.T) {
	t.Parallel()

	redaction := domain.Message{ID: "$1", RoomID: chatRm, Redacted: true} // exactly what onRedaction emits
	edit := domain.Message{ID: "$2", RoomID: chatRm, Sender: alice, Body: "fixed typo", Edited: true}

	for name, m := range map[string]domain.Message{"a redaction marker": redaction, "an edit": edit} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, rec, _ := notifier(t, notifsOn("all"))
			if _, ok := n.Deliver(context.Background(), m); ok {
				t.Error("this revises a message already delivered; it should not notify")
			}
			if got := rec.all(); len(got) != 0 {
				t.Errorf("notifications = %+v, want none", got)
			}
		})
	}
}

// An attachment sent without a caption has an empty body and is still worth being
// told about, so it is described by what it is rather than arriving blank.
func TestMediaNotificationSaysWhatItIs(t *testing.T) {
	t.Parallel()

	n, _, _ := notifier(t, notifsOn("all"))

	withName := msg(chatRm, alice, "")
	withName.Media = &domain.Media{Type: domain.MediaFile, Name: "invoice-2026.pdf"}
	got, ok := n.Deliver(context.Background(), withName)
	if !ok {
		t.Fatal("an attachment should still notify")
	}
	if got.Body != "[file: invoice-2026.pdf]" {
		t.Errorf("Body = %q, want the attachment named", got.Body)
	}

	unnamed := msg(chatRm, alice, "")
	unnamed.Media = &domain.Media{Type: domain.MediaImage}
	got, ok = n.Deliver(context.Background(), unnamed)
	if !ok {
		t.Fatal("an unnamed attachment should still notify")
	}
	if got.Body != "[image]" {
		t.Errorf("Body = %q, want the kind of attachment", got.Body)
	}

	// A caption still wins: it is what the sender actually said.
	captioned := msg(chatRm, alice, "look at this")
	captioned.Media = &domain.Media{Type: domain.MediaImage, Name: "cat.png"}
	got, _ = n.Deliver(context.Background(), captioned)
	if got.Body != "look at this" {
		t.Errorf("Body = %q, want the caption", got.Body)
	}
}

// Nothing is announced retroactively.
func TestCatchUpDoesNotNotify(t *testing.T) {
	t.Parallel()

	src, rec := world(), &recorder{}
	n, err := daemon.NewNotifications(notifsOn("all"), src, me,
		func(config.Notifications) notify.Notifier { return rec })
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}

	// Before any sync at all.
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "overnight")); ok {
		t.Error("nothing should notify before the first sync response")
	}

	// The catch-up batch itself: its events arrive after the first response is
	// announced and before the second.
	n.Synced(time.Now())
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "also overnight")); ok {
		t.Error("the catch-up batch is history; history does not interrupt you")
	}

	// Caught up: from here a message is a message.
	n.Synced(time.Now())
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "just now")); !ok {
		t.Error("once caught up, a message that arrives should notify at once")
	}
	if got := rec.all(); len(got) != 1 || got[0].Body != "just now" {
		t.Errorf("notifications = %+v, want only the live one", got)
	}
}

// A sound-only mute lets the notification through and takes its sound, so it is not routed
// through the do-not-disturb suppression at all.
func TestSoundMuteSilencesWithoutHiding(t *testing.T) {
	t.Parallel()

	n, rec, _ := notifier(t, notifsOn("all"))
	n.SetDND(silenceRule(chatRm, ""))

	got, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi"))
	if !ok {
		t.Fatal("a sound-only mute should not hold the notification back")
	}
	if !got.Silent {
		t.Error("…but it should arrive silent")
	}
	if delivered := rec.all(); len(delivered) != 1 || !delivered[0].Silent {
		t.Errorf("delivered = %+v, want one silent notification", delivered)
	}

	// A room it does not name is unaffected.
	elsewhere, ok := n.Deliver(context.Background(), msg(dmRoom, alice, "hi"))
	if !ok || elsewhere.Silent {
		t.Errorf("notification = %+v/%v, want an ordinary one elsewhere", elsewhere, ok)
	}

	// Hiding still hides.
	n.SetDND(hideRule(chatRm, ""))
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); ok {
		t.Error("replacing the sound mute with a hide should hold it back")
	}
}

// The global sound is the default and a rule replaces it, which is what makes one
// sound expressible everywhere and a different one per space, room or person.
func TestSoundIsGlobalWithRuleOverrides(t *testing.T) {
	t.Parallel()

	cfg := notifsOn("all")
	cfg.Notifications.Sound = "/s/default.oga"
	cfg.Notifications.Rules = append(cfg.Notifications.Rules, config.Rule{Match: spaceEntry, Sound: "/s/work.oga"})
	n, _, _ := notifier(t, cfg)

	got, _ := n.Deliver(context.Background(), msg(dmRoom, alice, "hi"))
	if got.Sound != "/s/default.oga" {
		t.Errorf("Sound = %q, want the global default", got.Sound)
	}
	got, _ = n.Deliver(context.Background(), msg(chatRm, alice, "hi"))
	if got.Sound != "/s/work.oga" {
		t.Errorf("Sound = %q, want the space rule's sound", got.Sound)
	}
}

// The room index is dropped when the rooms actually change, rather than waited out.
func TestInvalidateScopeDropsTheIndex(t *testing.T) {
	t.Parallel()

	n, _, src := notifier(t, notifsOn("all"))
	got, _ := n.Deliver(context.Background(), msg(chatRm, alice, "hi"))
	if got.Room != chatNam {
		t.Fatalf("Room = %q, want the cached name", got.Room)
	}
	reads := src.roomReads()

	// The room is renamed behind the index. Without invalidation it would keep
	// answering with the old name until the backstop expired.
	src.mu.Lock()
	src.rooms = []domain.Room{{ID: chatRm, Name: "Renamed"}, {ID: dmRoom, Name: dmName, IsDirect: true}}
	src.mu.Unlock()

	got, _ = n.Deliver(context.Background(), msg(chatRm, alice, "hi"))
	if got.Room != chatNam {
		t.Errorf("Room = %q — the index should still be trusted until something says otherwise", got.Room)
	}

	n.InvalidateScope()
	got, _ = n.Deliver(context.Background(), msg(chatRm, alice, "hi"))
	if got.Room != "Renamed" {
		t.Errorf("Room = %q, want the new name after invalidation", got.Room)
	}
	if src.roomReads() <= reads {
		t.Error("invalidation should have forced a re-read")
	}
}

// The rule helpers the do-not-disturb tests are written in.
func hideRule(match, sender string) notify.Rule {
	none := notify.LevelNone
	return notify.Rule{Match: match, Sender: sender, Show: &none, Temp: true}
}

// silenceRule takes only the sound, which is the other key.
func silenceRule(match, sender string) notify.Rule {
	none := notify.LevelNone
	return notify.Rule{Match: match, Sender: sender, Ring: &none, Temp: true}
}

// named and withUntil keep the call sites readable where only one field varies.
func named(r notify.Rule, name string) notify.Rule { r.Name = name; return r }

func withUntil(r notify.Rule, until time.Time) notify.Rule { r.Until = until; return r }

// A rule's thread clause is answered against the daemon's own cache: whether we have spoken
// in the conversation a message arrived in is not a fact the message carries, and it is the
// distinction such a rule is entirely about.
func TestAThreadRuleAsksWhetherWeAreInTheConversation(t *testing.T) {
	t.Parallel()

	cfg := notifsOn("all")
	cfg.Notifications.Rules = append(cfg.Notifications.Rules,
		config.Rule{Name: "no threads", Match: string(chatRm), Thread: "any", Show: "none"},
		config.Rule{Name: "mine", Match: string(chatRm), Thread: "participating", Show: "all"},
	)
	src, rec := world(), &recorder{}
	src.inThreads = map[domain.EventID]bool{"$mine": true}
	n, err := daemon.NewNotifications(cfg, src, me, func(config.Notifications) notify.Notifier { return rec })
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	n.Synced(time.Now())
	n.Synced(time.Now())

	inThread := func(root domain.EventID) domain.Message {
		m := msg(chatRm, alice, "hi")
		m.ThreadRoot = root
		return m
	}
	if _, got := n.Deliver(context.Background(), inThread("$mine")); !got {
		t.Error("a thread we have spoken in should notify")
	}
	if _, got := n.Deliver(context.Background(), inThread("$theirs")); got {
		t.Error("a thread we have not spoken in should be silent")
	}
	// And the room's main timeline is neither: the rule silencing threads matched it
	// too, because "any" is a clause about threads that asks nothing.
	if _, got := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); got {
		t.Error("the room itself is silenced by the rule that names it")
	}
}

// A bursty room delivers its first MaxPerRoom messages, then one silent summary that
// replaces its popup.
func TestABurstyRoomIsSummarizedRatherThanStacked(t *testing.T) {
	t.Parallel()

	cfg := notifsOn("all")
	cfg.Notifications.MaxPerRoom = 2
	cfg.Notifications.RateWindow = "10m"
	n, rec, _ := notifier(t, cfg)

	for i := range 6 {
		n.Deliver(context.Background(), msg(chatRm, alice, fmt.Sprintf("message %d", i)))
	}
	sent := rec.all()
	if len(sent) != 3 {
		t.Fatalf("%d notifications for six messages, want two and a summary: %+v", len(sent), sent)
	}
	if sent[0].Body != "message 0" || sent[1].Body != "message 1" {
		t.Errorf("the burst was not delivered as itself: %q, %q", sent[0].Body, sent[1].Body)
	}
	summary := sent[2]
	if summary.Body != "1 new message" {
		t.Errorf("summary body = %q, want the count that was held", summary.Body)
	}
	if summary.Room != chatNam {
		t.Errorf("summary room = %q, want the room it is about", summary.Room)
	}
	// It names no sender and makes no sound: the sound already rang for the ones that
	// got through, and quoting one of several messages would be picking a winner.
	if summary.Sender != "" || summary.Body == "" || !summary.Silent {
		t.Errorf("summary = %+v, want an anonymous, silent count", summary)
	}
	// And it replaces the room's previous popup rather than stacking under it.
	if summary.Replaces != string(chatRm) {
		t.Errorf("summary Replaces = %q, want the room id", summary.Replaces)
	}
}

// Nothing is dropped where nobody asked for a limit, which is every config that has
// never heard of this.
func TestNoLimitConfiguredDeliversEverything(t *testing.T) {
	t.Parallel()

	n, rec, _ := notifier(t, notifsOn("all"))
	for i := range 20 {
		n.Deliver(context.Background(), msg(chatRm, alice, fmt.Sprintf("message %d", i)))
	}
	if got := len(rec.all()); got != 20 {
		t.Errorf("%d notifications for twenty messages with no limit set, want all of them", got)
	}
}

// A busy room must not silence a quiet one.
func TestTheLimitIsPerRoom(t *testing.T) {
	t.Parallel()

	cfg := notifsOn("all")
	cfg.Notifications.MaxPerRoom = 1
	cfg.Notifications.RateWindow = "10m"
	n, rec, _ := notifier(t, cfg)

	n.Deliver(context.Background(), msg(chatRm, alice, "one"))
	n.Deliver(context.Background(), msg(chatRm, alice, "two"))   // summarized
	n.Deliver(context.Background(), msg(chatRm, alice, "three")) // held
	n.Deliver(context.Background(), msg(dmRoom, alice, "hello")) // a different room

	sent := rec.all()
	if len(sent) != 3 {
		t.Fatalf("%d notifications, want two from the busy room and one from the quiet one: %+v", len(sent), sent)
	}
	if sent[2].Body != "hello" {
		t.Errorf("the quiet room's message = %q, want it delivered as itself", sent[2].Body)
	}
}

// A room that stays unknown is asked about once, not once per lookup.
func TestAnUnknownRoomIsOnlyAskedAboutOnce(t *testing.T) {
	t.Parallel()

	n, _, src := notifier(t, notifsOn("all"))
	n.Deliver(context.Background(), msg(chatRm, alice, "hi"))

	// A room nothing will ever report: the sender's list is left alone.
	const ghost = "!ghost:example.org"
	before := src.roomReads()
	n.Deliver(context.Background(), msg(ghost, alice, "first"))
	first := src.roomReads() - before
	if first == 0 {
		t.Fatal("the first message in an unknown room did not read the room list at all")
	}

	// Five more messages from the same unknown room.
	after := src.roomReads()
	for range 5 {
		n.Deliver(context.Background(), msg(ghost, alice, "again"))
	}
	if extra := src.roomReads() - after; extra != 0 {
		t.Errorf("five more messages from the same unknown room cost %d further reads of the "+
			"room list; the miss should be remembered for scopeRetry", extra)
	}
}

// A message from any of your own IDs — an identity a bridge posts as, a WhatsApp
// number — is yours: it never notifies you.
func TestYourOtherIDsNeverNotifyYou(t *testing.T) {
	t.Parallel()
	n, _, _ := notifier(t, notifsOn("all"))
	const whatsapp = "whatsapp:359000000001@s.whatsapp.net"
	if _, ok := n.Deliver(context.Background(), msg(chatRm, whatsapp, "from my phone")); !ok {
		t.Fatal("before UseSelves the number is a stranger's, and should notify")
	}
	n.UseSelves(func() []string { return []string{me, whatsapp} })
	if _, ok := n.Deliver(context.Background(), msg(chatRm, whatsapp, "from my phone again")); ok {
		t.Error("your own WhatsApp number notified you")
	}
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); !ok {
		t.Error("someone else stopped notifying")
	}
}
