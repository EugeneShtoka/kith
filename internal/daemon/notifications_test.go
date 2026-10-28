package daemon_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
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
	me      = "matrix:@me:example.org"
	alice   = "matrix:@alice:example.org"
	bob     = "matrix:@bob:example.org"
	dmRoom  = mx + "!dm:example.org"
	chatRm  = mx + "!standup:example.org"
	dmName  = "Alice"
	chatNam = "Standup"
	space   = "Work"
	// The same two places as rule entries.
	chatNamEntry = "room:Standup"
	spaceEntry   = "space:Work"
)

// scopeBackend serves the room and space lists the daemon reads; the rest is a Nop.
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

func world() *scopeBackend {
	return &scopeBackend{
		rooms: []domain.Room{
			{ID: dmRoom, Name: dmName, IsDirect: true},
			{ID: chatRm, Name: chatNam},
		},
		spaces: []domain.Space{{ID: mx + "!work:example.org", Name: space, Children: []domain.RoomID{chatRm}}},
	}
}

// recorder keeps what a notifier published, read as it is asked.
type recorder struct {
	mu   sync.Mutex
	take func() []daemon.Published
	sent []daemon.Published
}

// record subscribes a recorder to what n publishes, for the test's length.
func record(t *testing.T, n *daemon.Notifications) *recorder {
	t.Helper()
	take, release := n.Publications()
	t.Cleanup(release)
	return &recorder{take: take}
}

// published is everything published so far, with its topics.
func (r *recorder) published() []daemon.Published {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, r.take()...)
	return append([]daemon.Published(nil), r.sent...)
}

// all is the notifications published so far.
func (r *recorder) all() []notify.Notification {
	published := r.published()
	out := make([]notify.Notification, len(published))
	for i := range published {
		out[i] = published[i].Alert
	}
	return out
}

// notifsOn notifies at level with the sinks disabled; tests deliver to a recorder.
func notifsOn(level string) config.Config {
	return config.Config{Notifications: config.Notifications{Rules: []config.Rule{{Show: level}}}}
}

// notifier builds a Notifications over world() with a recorder as its sink.
func notifier(t *testing.T, cfg config.Config) (*daemon.Notifications, *recorder, *scopeBackend) {
	t.Helper()

	src := world()
	n, err := daemon.NewNotifications(asStarted(cfg), src)
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	rec := record(t, n)
	n.UseSelves(func() []string { return []string{me} })
	// Two sync responses put the daemon past its catch-up batch.
	n.Synced(mx, time.Now())
	n.Synced(mx, time.Now())
	return n, rec, src
}

func msg(room domain.RoomID, sender, body string) domain.Message {
	return domain.Message{ID: mx + "$1", RoomID: room, Sender: sender, Body: body, Timestamp: time.Now()}
}

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

// A notification carries the room's name, the sender's, and the protocol its MXID implies.
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
	if got.Sender != "Alice" || got.SenderID != alice {
		t.Errorf("sender = %q/%q, want Alice/%s", got.Sender, got.SenderID, alice)
	}
	if got.Body != "stand-up in five" {
		t.Errorf("Body = %q, want the message text", got.Body)
	}
	if got.Protocol == "" {
		t.Error("Protocol should be derived from the MXID")
	}
}

// A long body is cut by runes, so non-ASCII text is not sliced mid-character.
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

// A rule narrows the policy for one place or person, resolved against cached spaces.
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

// The room's configured alias reaches the notification text.
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

// A rule names a room by ID; a bare name names nothing.
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

	// Asked for explicitly, the name matches.
	n, _, _ = notifier(t, withRule(chatNamEntry))
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); !ok {
		t.Error("room:<name> should match the room it names")
	}

	n, _, _ = notifier(t, withRule(string(chatRm)))
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); !ok {
		t.Error("a rule naming the room's ID should match it")
	}
}

// A config that will not parse is refused, naming the section; the old policy stays.
func TestReloadReplacesThePolicy(t *testing.T) {
	t.Parallel()

	n, _, _ := notifier(t, notifsOn("none"))
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); ok {
		t.Fatal("level none should notify about nothing")
	}
	if err := n.Reload(asStarted(notifsOn("all"))); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); !ok {
		t.Error("the reloaded policy should be the one that decides")
	}
	err := n.Reload(asStarted(notifsOn("mentions"))) // not a level: the spelling is "mention"
	if err == nil || !strings.Contains(err.Error(), "show") {
		t.Fatalf("Reload(bad) = %v, want a refusal naming the key", err)
	}
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "hi")); !ok {
		t.Error("the previous policy should still be in force after a refused reload")
	}
}

// NewNotifications refuses a bad config, naming the key.
func TestNewNotificationsRefusesABadConfig(t *testing.T) {
	t.Parallel()

	badWindow := notifsOn("all")
	badWindow.Notifications.MaxPerRoom = 3
	badWindow.Notifications.RateWindow = "soon"
	for key, cfg := range map[string]config.Config{
		"show":        notifsOn("everything"),
		"rate_window": badWindow,
	} {
		_, err := daemon.NewNotifications(asStarted(cfg), world())
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("NewNotifications() = %v, want a refusal naming %s", err, key)
		}
	}
}

// The room index is read once, keeping the cache out of every popup's path.
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

// A room the index has never seen forces a re-read (e.g. a fresh invite).
func TestAnUnknownRoomRebuildsTheIndex(t *testing.T) {
	t.Parallel()

	n, _, src := notifier(t, notifsOn("all"))
	n.Deliver(context.Background(), msg(chatRm, alice, "hi"))
	before := src.roomReads()

	src.mu.Lock()
	src.rooms = append(src.rooms, domain.Room{ID: mx + "!new:example.org", Name: "New"})
	src.mu.Unlock()

	got, ok := n.Deliver(context.Background(), msg(mx+"!new:example.org", alice, "hi"))
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
	// Subscribe before producing: the hub does not replay.
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

// waitFor polls cond until it holds; a fan-out round trip takes microseconds, so
// hitting the bound means a hang.
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

// Edits and redactions on the message stream never notify.
func TestRevisionsDoNotNotify(t *testing.T) {
	t.Parallel()

	redaction := domain.Message{ID: mx + "$1", RoomID: chatRm, Redacted: true} // exactly what onRedaction emits
	edit := domain.Message{ID: mx + "$2", RoomID: chatRm, Sender: alice, Body: "fixed typo", Edited: true}

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

// A captionless attachment is described by what it is rather than arriving blank.
func TestMediaNotificationSaysWhatItIs(t *testing.T) {
	t.Parallel()

	n, _, _ := notifier(t, notifsOn("all"))

	withName := msg(chatRm, alice, "")
	withName.Media = &domain.Media{Type: domain.MediaFile, Name: "invoice-2026.pdf"}
	got, ok := n.Deliver(context.Background(), withName)
	if !ok {
		t.Fatal("an attachment should still notify")
	}
	if !strings.Contains(got.Body, "invoice-2026.pdf") {
		t.Errorf("Body = %q, want the attachment named", got.Body)
	}

	unnamed := msg(chatRm, alice, "")
	unnamed.Media = &domain.Media{Type: domain.MediaImage}
	got, ok = n.Deliver(context.Background(), unnamed)
	if !ok {
		t.Fatal("an unnamed attachment should still notify")
	}
	if got.Body == "" {
		t.Errorf("Body = %q, want the kind of attachment", got.Body)
	}

	// A caption wins.
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

	src := world()
	n, err := daemon.NewNotifications(asStarted(notifsOn("all")), src)
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	rec := record(t, n)
	n.UseSelves(func() []string { return []string{me} })

	// Before any sync.
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "overnight")); ok {
		t.Error("nothing should notify before the first sync response")
	}

	// The catch-up batch arrives between the first and second responses.
	n.Synced(mx, time.Now())
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "also overnight")); ok {
		t.Error("the catch-up batch is history; history does not interrupt you")
	}

	n.Synced(mx, time.Now())
	if _, ok := n.Deliver(context.Background(), msg(chatRm, alice, "just now")); !ok {
		t.Error("once caught up, a message that arrives should notify at once")
	}
	if got := rec.all(); len(got) != 1 || got[0].Body != "just now" {
		t.Errorf("notifications = %+v, want only the live one", got)
	}
}

// Under any interleaving of two Matrix accounts' syncs and messages, a message
// notifies only once its own account is past its catch-up batch: one account caught
// up never lets another's batch through. A network that reports no syncs (WhatsApp
// here) waits for any account.
func TestEachAccountHasItsOwnCatchUp(t *testing.T) {
	t.Parallel()
	work := domain.AccountRooms(domain.ProtocolMatrix, "fedcba9876543210")
	rooms := []domain.Room{
		{ID: chatRm, Name: chatNam},
		{ID: domain.RoomID(string(work) + "!standup:example.org"), Name: "Standup at work"},
		{ID: "whatsapp:15550000001/120363@g.us", Name: "Choir"},
	}
	accounts := []domain.RoomOwner{domain.RoomOwner(mx), work}
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 5))
		n, err := daemon.NewNotifications(asStarted(notifsOn("all")), &scopeBackend{rooms: rooms})
		if err != nil {
			t.Fatal(err)
		}
		n.UseSelves(func() []string { return []string{me} })
		syncs := map[domain.RoomOwner]int{}
		var trail []string
		for step := range 16 {
			if rng.IntN(3) == 0 {
				on := accounts[rng.IntN(len(accounts))]
				n.Synced(on, time.Now())
				syncs[on]++
				trail = append(trail, "sync "+string(on))
				continue
			}
			room := rooms[rng.IntN(len(rooms))].ID
			want := false
			if count, reports := syncs[domain.OwnerOf(room)]; reports {
				want = count > 1
			} else {
				for _, count := range syncs {
					want = want || count > 1
				}
			}
			trail = append(trail, "message in "+string(room))
			m := msg(room, alice, fmt.Sprint("message ", step))
			m.ID = domain.EventID(fmt.Sprint(m.ID, step))
			if _, got := n.Deliver(context.Background(), m); got != want {
				t.Fatalf("seed %d: notified = %t, want %t after:\n  %s", seed, got, want, strings.Join(trail, "\n  "))
			}
		}
	}
}

// A sound-only mute keeps the notification and drops its sound.
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

// The global sound is the default; a rule replaces it.
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

// InvalidateScope drops the room index when rooms change.
func TestInvalidateScopeDropsTheIndex(t *testing.T) {
	t.Parallel()

	n, _, src := notifier(t, notifsOn("all"))
	got, _ := n.Deliver(context.Background(), msg(chatRm, alice, "hi"))
	if got.Room != chatNam {
		t.Fatalf("Room = %q, want the cached name", got.Room)
	}
	reads := src.roomReads()

	// Renamed behind the index; without invalidation the old name would stay until
	// the backstop expired.
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

func hideRule(match, sender string) notify.Rule {
	none := notify.LevelNone
	return notify.Rule{Match: match, Sender: sender, Show: &none, Temp: true}
}

func silenceRule(match, sender string) notify.Rule {
	none := notify.LevelNone
	return notify.Rule{Match: match, Sender: sender, Ring: &none, Temp: true}
}

func named(r notify.Rule, name string) notify.Rule { r.Name = name; return r }

func withUntil(r notify.Rule, until time.Time) notify.Rule { r.Until = until; return r }

// A thread clause is answered from the cache: whether we spoke in the conversation
// is not a fact the message carries.
func TestAThreadRuleAsksWhetherWeAreInTheConversation(t *testing.T) {
	t.Parallel()

	cfg := notifsOn("all")
	cfg.Notifications.Rules = append(cfg.Notifications.Rules,
		config.Rule{Name: "no threads", Match: string(chatRm), Thread: "any", Show: "none"},
		config.Rule{Name: "mine", Match: string(chatRm), Thread: "participating", Show: "all"},
	)
	src := world()
	src.inThreads = map[domain.EventID]bool{mx + "$mine": true}
	n, err := daemon.NewNotifications(asStarted(cfg), src)
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	n.UseSelves(func() []string { return []string{me} })
	n.Synced(mx, time.Now())
	n.Synced(mx, time.Now())

	inThread := func(root domain.EventID) domain.Message {
		m := msg(chatRm, alice, "hi")
		m.ThreadRoot = root
		return m
	}
	if _, got := n.Deliver(context.Background(), inThread(mx+"$mine")); !got {
		t.Error("a thread we have spoken in should notify")
	}
	if _, got := n.Deliver(context.Background(), inThread(mx+"$theirs")); got {
		t.Error("a thread we have not spoken in should be silent")
	}
	// The main timeline is matched too: "any" asks nothing about threads.
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
	// No sender and no sound: the sound already rang, and quoting one message would pick
	// a winner.
	if summary.Sender != "" || summary.Body == "" || !summary.Silent {
		t.Errorf("summary = %+v, want an anonymous, silent count", summary)
	}
	if summary.Replaces != string(chatRm) {
		t.Errorf("summary Replaces = %q, want the room id", summary.Replaces)
	}
}

// Old messages a filled gap brings fold into one silent count per room; a recent late one
// and any live one notify as themselves and start the count over.
func TestOldMessagesFromAFilledGapFoldIntoOne(t *testing.T) {
	t.Parallel()

	n, rec, _ := notifier(t, notifsOn("all"))
	late := func(body string, age time.Duration) domain.Message {
		m := msg(chatRm, alice, body)
		m.Late, m.Timestamp = true, time.Now().Add(-age)
		return m
	}
	ctx := context.Background()
	n.Deliver(ctx, late("an hour ago", time.Hour))
	n.Deliver(ctx, late("two hours ago", 2*time.Hour))
	n.Deliver(ctx, late("a minute ago", time.Minute))
	n.Deliver(ctx, msg(chatRm, alice, "just now"))
	n.Deliver(ctx, late("a day ago", 24*time.Hour))

	sent := rec.all()
	var bodies []string
	for _, s := range sent {
		bodies = append(bodies, s.Body)
	}
	want := []string{"1 message from earlier", "2 messages from earlier", "a minute ago", "just now", "1 message from earlier"}
	if !slices.Equal(bodies, want) {
		t.Fatalf("notified %q, want %q", bodies, want)
	}
	if folded := sent[1]; !folded.Silent || folded.Sender != "" || folded.Replaces != string(chatRm) {
		t.Errorf("folded = %+v, want a silent count about the room, replacing itself", folded)
	}
}

// Without a configured limit nothing is dropped.
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

	// A room nothing will ever report.
	const ghost = mx + "!ghost:example.org"
	before := src.roomReads()
	n.Deliver(context.Background(), msg(ghost, alice, "first"))
	first := src.roomReads() - before
	if first == 0 {
		t.Fatal("the first message in an unknown room did not read the room list at all")
	}

	after := src.roomReads()
	for range 5 {
		n.Deliver(context.Background(), msg(ghost, alice, "again"))
	}
	if extra := src.roomReads() - after; extra != 0 {
		t.Errorf("five more messages from the same unknown room cost %d further reads of the "+
			"room list; the miss should be remembered for scopeRetry", extra)
	}
}

// A message from any of your own IDs (bridge puppet, WhatsApp number) never notifies.
func TestYourOtherIDsNeverNotifyYou(t *testing.T) {
	t.Parallel()
	n, _, _ := notifier(t, notifsOn("all"))
	const whatsapp = "whatsapp:44000000001@s.whatsapp.net"
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

// A bridge placeholder is not announced; the edit replacing it is, as the message it
// now is; an ordinary edit is not.
func TestAPlaceholderIsAnnouncedOnceItIsReplaced(t *testing.T) {
	t.Parallel()
	n, rec, _ := notifier(t, notifsOn("all"))
	ctx := context.Background()
	stand := msg(chatRm, alice, "Decrypting message from WhatsApp failed")
	stand.ID, stand.Placeholder = mx+"$stand", true
	if _, got := n.Deliver(ctx, stand); got {
		t.Error("the placeholder was announced")
	}
	replaced := msg(chatRm, alice, "look at this")
	replaced.ID, replaced.Edited, replaced.RevisionID = mx+"$stand", true, mx+"$edit"
	replaced.Media = &domain.Media{Type: domain.MediaImage, Name: "image.jpg"}
	if _, got := n.Deliver(ctx, replaced); !got {
		t.Error("the message replacing the placeholder was not announced")
	}
	if _, got := n.Deliver(ctx, replaced); got {
		t.Error("a second edit of it was announced too")
	}
	edit := msg(chatRm, alice, "fixed a typo")
	edit.ID, edit.Edited, edit.RevisionID = mx+"$other", true, mx+"$edit2"
	if _, got := n.Deliver(ctx, edit); got {
		t.Error("an ordinary edit was announced")
	}
	if got := rec.all(); len(got) != 1 || !strings.Contains(got[0].Body, "look at this") {
		t.Errorf("announced %+v, want the replacement alone", got)
	}
}

// A sender or room shown only as a number is named from the directory.
func TestANotificationNamesANumberFromTheDirectory(t *testing.T) {
	t.Parallel()
	n, rec, src := notifier(t, notifsOn("all"))
	src.mu.Lock()
	src.rooms = append(src.rooms, domain.Room{ID: mx + "!num:example.org", Name: "+15550100001 (WA)", IsDirect: true})
	src.mu.Unlock()
	n.InvalidateScope()
	n.UseDirectory(func(context.Context) (domain.Directory, error) {
		return numbersNamed(map[string]string{"15550100001": "Dana", "15550100002": "Eli"}), nil
	})
	for _, c := range []struct {
		room          domain.RoomID
		shown         string
		sender, title string
	}{
		{mx + "!num:example.org", "+1 555 010 0001 (WA)", "Dana", "Dana"},
		{chatRm, "+15550100002", "Eli", chatNam},
		{chatRm, "Alice", "Alice", chatNam},
	} {
		m := msg(c.room, "matrix:@whatsapp_15550100009:example.org", "hi")
		m.SenderName = c.shown
		if _, ok := n.Deliver(context.Background(), m); !ok {
			t.Fatalf("%q in %s did not notify", c.shown, c.room)
		}
		sent := rec.all()
		if got := sent[len(sent)-1]; got.Sender != c.sender || got.Room != c.title {
			t.Errorf("%q in %s: sender %q, room %q; want %q, %q", c.shown, c.room, got.Sender, got.Room, c.sender, c.title)
		}
	}
}

// {date} and {time} follow [display] and reloads; an unreadable pattern is refused.
func TestNotificationsWriteTheChosenClock(t *testing.T) {
	t.Parallel()

	cfg := notifsOn("all")
	cfg.Display.TimeFormat, cfg.Display.ShortDateFormat = "12h", "DD.MM.YYYY"
	n, rec, _ := notifier(t, cfg)
	m := msg(chatRm, alice, "stand-up in five")
	m.Timestamp = time.Date(2026, 8, 21, 14, 5, 0, 0, time.Local)
	n.Deliver(context.Background(), m)

	cfg.Display.TimeFormat, cfg.Display.ShortDateFormat = "24h", "MM/DD/YY"
	if err := n.Reload(asStarted(cfg)); err != nil {
		t.Fatal(err)
	}
	m.ID = mx + "$2"
	n.Deliver(context.Background(), m)

	sent := rec.all()
	if len(sent) != 2 {
		t.Fatalf("%d notifications, want 2", len(sent))
	}
	for i, want := range [][2]string{{"21.08.2026", "2:05 PM"}, {"08/21/26", "14:05"}} {
		if sent[i].Date != want[0] || sent[i].Time != want[1] {
			t.Errorf("notification %d wrote %q %q, want %q %q", i, sent[i].Date, sent[i].Time, want[0], want[1])
		}
	}

	bad := notifsOn("all")
	bad.Display.LongDateFormat = "YYY"
	if _, err := daemon.NewNotifications(asStarted(bad), world()); err == nil ||
		!strings.Contains(err.Error(), "long_date_format") {
		t.Errorf("NewNotifications() = %v, want a refusal naming long_date_format", err)
	}
}

// Notifications name people as the timeline does: alias, phone book name, first
// names only where the space says so.
func TestNotificationsNamePeopleAsTheTimelineDoes(t *testing.T) {
	t.Parallel()
	const dana = "whatsapp:15550100001@s.whatsapp.net"
	cfg := notifsOn("all")
	cfg.Display.Identities = []config.Identity{{Alias: "Alice Cooper", IDs: []string{alice}}}
	n, rec, _ := notifier(t, cfg)
	n.UseDirectory(func(context.Context) (domain.Directory, error) {
		return numbersNamed(map[string]string{"15550100001": "Dana Levi"}), nil
	})
	m := msg(chatRm, alice, "@100000000000005 can you look?")
	m.SenderName = "alice"
	m.Mentions = []domain.Mention{{UserID: dana, Name: "@100000000000005"}}
	n.Deliver(context.Background(), m)

	cfg.Display.SpaceRules = []config.SpaceRule{{Space: "space:" + space, FirstNameOnly: true}}
	if err := n.Reload(asStarted(cfg)); err != nil {
		t.Fatal(err)
	}
	m.ID = mx + "$2"
	n.Deliver(context.Background(), m)

	sent := rec.all()
	if len(sent) != 2 {
		t.Fatalf("%d notifications, want 2", len(sent))
	}
	for i, want := range [][2]string{{"Alice Cooper", "@Dana Levi can you look?"}, {"Alice", "@Dana can you look?"}} {
		if sent[i].Sender != want[0] || sent[i].Body != want[1] {
			t.Errorf("notification %d: %q said %q, want %q saying %q", i, sent[i].Sender, sent[i].Body, want[0], want[1])
		}
	}
}

// Mentions and senders are named as the room's member list names them now.
func TestANotificationNamesPeopleAsTheRoomNamesThemNow(t *testing.T) {
	t.Parallel()
	n, rec, _ := notifier(t, notifsOn("all"))
	now := map[string]string{alice: "Alice Renamed", "slack:T1/U2": "Bo New"}
	n.UseMemberNames(func(_ context.Context, room domain.RoomID, user string) (string, error) {
		if room != chatRm {
			t.Errorf("a member name asked of %s, want the message's room", room)
		}
		return now[user], nil
	})
	m := msg(chatRm, alice, "@Bo Old, standup moved")
	m.SenderName = "Alice"
	m.Mentions = []domain.Mention{{UserID: "slack:T1/U2", Name: "@Bo Old"}}
	n.Deliver(context.Background(), m)

	sent := rec.all()
	if len(sent) != 1 || sent[0].Sender != "Alice Renamed" || sent[0].Body != "@Bo New, standup moved" {
		t.Fatalf("sent %+v, want Alice Renamed saying \"@Bo New, standup moved\"", sent)
	}
}

// asStarted is cfg with the profile the starter config writes, every room, unless cfg
// writes its own.
func asStarted(cfg config.Config) config.Config {
	if len(cfg.Profiles) == 0 {
		cfg.Profiles = []config.Profile{{Name: "default", Include: []string{"*"}}}
	}
	return cfg
}
