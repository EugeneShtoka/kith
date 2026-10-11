package daemon_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/notify"
)

// spamBackend is world() plus room history, caught rooms and a verdict store.
type spamBackend struct {
	*scopeBackend

	mu       sync.Mutex
	timeline map[domain.RoomID][]domain.Message
	caught   []domain.SpamVerdict
	marked   []domain.SpamVerdict
	// refuse is what MarkSpam answers, for a write the homeserver turns down.
	refuse error
}

func spamWorld() *spamBackend {
	return &spamBackend{scopeBackend: world(), timeline: map[domain.RoomID][]domain.Message{}}
}

func (s *spamBackend) CachedTimeline(_ context.Context, room domain.RoomID) ([]domain.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timeline[room], nil
}

func (s *spamBackend) SpamRooms(context.Context) ([]domain.SpamVerdict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caught, nil
}

func (s *spamBackend) MarkSpam(_ context.Context, verdict domain.SpamVerdict) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse != nil {
		return s.refuse
	}
	s.marked = append(s.marked, verdict)
	return nil
}

// store replaces the account data, as a sync from another machine would.
func (s *spamBackend) store(verdicts ...domain.SpamVerdict) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caught = verdicts
}

func (s *spamBackend) verdicts() []domain.SpamVerdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.SpamVerdict(nil), s.marked...)
}

// said fills a room's history.
func (s *spamBackend) said(room domain.RoomID, bodies ...string) *spamBackend {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, body := range bodies {
		s.timeline[room] = append(s.timeline[room], domain.Message{
			ID:        domain.EventID("$old" + string(rune('a'+i))),
			RoomID:    room,
			Sender:    alice,
			Body:      body,
			Timestamp: time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}
	return s
}

// spamConfig is a notifying config with one filter plus the case's changes.
func spamConfig(spam config.Spam) config.Config {
	cfg := notifsOn("all")
	if len(spam.Filters) == 0 {
		spam.Filters = []config.SpamFilter{{Name: "crypto", Words: []string{"*bitcoin*", "invest*"}}}
	}
	cfg.Spam = spam
	return cfg
}

// spamNotifier is the daemon's notifier over a spam-capable backend.
func spamNotifier(t *testing.T, cfg config.Config, src *spamBackend) (*daemon.Notifications, *recorder) {
	t.Helper()
	rec := &recorder{}
	n, err := daemon.NewNotifications(cfg, src, func(config.Notifications) notify.Notifier { return rec })
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}
	n.UseSelves(func() []string { return []string{me} })
	n.Synced(time.Now())
	n.Synced(time.Now())
	return n, rec
}

// In a new room the first spam message promotes it, and that message does not notify.
func TestTheFirstMessageInARoomPromotesIt(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	n, rec := spamNotifier(t, spamConfig(config.Spam{}), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "double your bitcoin today")); notified {
		t.Fatal("the message that caught the room notified")
	}
	if len(rec.all()) != 0 {
		t.Fatalf("delivered %+v, want nothing", rec.all())
	}
	marked := src.verdicts()
	if len(marked) != 1 {
		t.Fatalf("recorded %d verdicts, want one", len(marked))
	}
	if marked[0].Rule != domain.SpamFirstMessage || marked[0].Filter != "crypto" {
		t.Errorf("verdict = %+v, want the first-message rule naming the filter", marked[0])
	}
	if marked[0].Room != chatRm {
		t.Errorf("verdict is about %s, want %s", marked[0].Room, chatRm)
	}
}

// In an established room one spam message is noise: no promotion, it notifies.
func TestAnEstablishedRoomIsNotPromotedByOneMessage(t *testing.T) {
	t.Parallel()

	src := spamWorld().said(chatRm, "morning", "any news on the deploy?")
	n, rec := spamNotifier(t, spamConfig(config.Spam{}), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "invest with us")); !notified {
		t.Fatal("a room with history was silenced by one caught message")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want no promotion", src.verdicts())
	}
	if len(rec.all()) != 1 {
		t.Errorf("delivered %d notifications, want one", len(rec.all()))
	}
}

// A DM you have written in is a conversation; one caught message does not move it.
func TestADirectMessageYouHaveAnsweredIsAConversation(t *testing.T) {
	t.Parallel()

	src := spamWorld().said(dmRoom, "hello")
	src.timeline[dmRoom] = append(src.timeline[dmRoom], domain.Message{
		ID: "$mine", RoomID: dmRoom, Sender: me, Body: "morning", Timestamp: time.Now(),
	})
	n, _ := spamNotifier(t, spamConfig(config.Spam{}), src)

	if _, notified := n.Deliver(context.Background(), msg(dmRoom, alice, "invest with us")); !notified {
		t.Error("a DM this account has written in was moved by one caught message")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing: it is a conversation", src.verdicts())
	}
}

// In a DM a verdict on the message is a verdict on the room.
func TestADirectMessageIsItsSender(t *testing.T) {
	t.Parallel()

	src := spamWorld().said(dmRoom, "hello").said(chatRm, "morning")
	n, _ := spamNotifier(t, spamConfig(config.Spam{}), src)
	ctx := context.Background()

	if _, notified := n.Deliver(ctx, msg(dmRoom, alice, "invest with us")); notified {
		t.Error("a caught direct message notified")
	}
	// The same words in a group, where one sender is not the room.
	if _, notified := n.Deliver(ctx, msg(chatRm, bob, "invest with us")); !notified {
		t.Error("a group was moved by one sender's message")
	}
	marked := src.verdicts()
	if len(marked) != 1 || marked[0].Rule != domain.SpamDirect || marked[0].Room != dmRoom {
		t.Fatalf("verdicts = %+v, want one direct-message promotion of the DM", marked)
	}
}

// Most of a window's messages caught promotes a group: a share, a floor and a window.
func TestMostOfTheMessagesPromotesAGroup(t *testing.T) {
	t.Parallel()

	src := spamWorld().said(chatRm,
		"double your bitcoin", "invest with us", "bitcoin bitcoin", "morning")
	ratio := config.Spam{Ratio: 0.6, Floor: 4, Filters: byRatio}
	n, _ := spamNotifier(t, spamConfig(ratio), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "invest now")); notified {
		t.Error("a room that is mostly spam still notified")
	}
	marked := src.verdicts()
	if len(marked) != 1 || marked[0].Rule != domain.SpamMostly {
		t.Fatalf("verdicts = %+v, want one most-of-them promotion", marked)
	}
}

// byRatio is the crypto filter moving by the ratio rule alone.
var byRatio = []config.SpamFilter{{Name: "crypto", Words: []string{"*bitcoin*", "invest*"}, Moves: []string{"ratio"}}}

// A filter that does not move by ratio leaves a mostly-spam group alone.
func TestOnlyARatioFilterMovesByTheRatio(t *testing.T) {
	t.Parallel()

	src := spamWorld().said(chatRm,
		"double your bitcoin", "invest with us", "bitcoin bitcoin", "morning")
	n, _ := spamNotifier(t, spamConfig(config.Spam{Ratio: 0.6, Floor: 4}), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "invest now")); !notified {
		t.Error("a filter without the ratio move promoted a group by its share")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing", src.verdicts())
	}
}

// An `always` filter moves an established group on one catch, only in the places it
// watches; place:<label> names a renamed space as the rail shows it.
func TestAnAlwaysFilterMovesOnlyWhereItWatches(t *testing.T) {
	t.Parallel()

	src := spamWorld().said(chatRm, "morning", "standup at ten")
	src.rooms = append(src.rooms, domain.Room{ID: "!other:example.org", Name: "Other"})
	src.said("!other:example.org", "morning", "lunch?")
	cfg := spamConfig(config.Spam{Filters: []config.SpamFilter{
		{Name: "elections", Words: []string{"vote"}, In: []string{"place:Office"}, Moves: []string{"always"}},
	}})
	cfg.Display.Names = []config.DisplayName{{Target: "space:" + space, Name: "Office"}}
	n, _ := spamNotifier(t, cfg, src)

	if _, notified := n.Deliver(context.Background(), msg("!other:example.org", alice, "vote for us")); !notified {
		t.Error("a filter caught a message outside the places it watches")
	}
	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "vote for us")); notified {
		t.Error("an always filter left the room it caught in notifying")
	}
	marked := src.verdicts()
	if len(marked) != 1 || marked[0].Room != chatRm || marked[0].Rule != domain.SpamAlways || marked[0].Filter != "elections" {
		t.Fatalf("verdicts = %+v, want one always promotion of %s", marked, chatRm)
	}
}

// Two of three is under the floor.
func TestTheFloorStopsASmallSample(t *testing.T) {
	t.Parallel()

	src := spamWorld().said(chatRm, "invest with us", "morning")
	ratio := config.Spam{Ratio: 0.5, Floor: 10, Filters: byRatio}
	n, _ := spamNotifier(t, spamConfig(ratio), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "invest now")); !notified {
		t.Error("a room with three messages was promoted on a share of them")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want no promotion under the floor", src.verdicts())
	}
}

// The exemption outranks every rule.
func TestAnExemptionOutranksEveryRule(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	n, _ := spamNotifier(t, spamConfig(config.Spam{Except: []string{chatNamEntry}}), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "double your bitcoin")); !notified {
		t.Error("an exempt room was silenced by a rule")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing: the room is exempt", src.verdicts())
	}
}

// A room listed as spam needs no rule and records no verdict: the config holds it.
func TestAListedRoomIsSilentWithoutARule(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	n, _ := spamNotifier(t, spamConfig(config.Spam{Rooms: []string{chatNamEntry}}), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "morning")); notified {
		t.Error("a room listed as spam notified")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing written for a room the config names", src.verdicts())
	}
}

// A verdict made before this daemon started holds: it is read back from account data.
func TestAnAlreadyCaughtRoomStaysQuiet(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	src.caught = []domain.SpamVerdict{{Room: chatRm, Rule: domain.SpamFirstMessage, Filter: "crypto"}}
	n, _ := spamNotifier(t, spamConfig(config.Spam{}), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "morning")); notified {
		t.Error("a room caught before this start notified again")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing: it was already caught", src.verdicts())
	}
}

// With no filter written nothing fires, hence no `enabled` switch.
func TestNoFilterMeansNoPromotion(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	cfg := notifsOn("all")
	n, _ := spamNotifier(t, cfg, src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "double your bitcoin")); !notified {
		t.Error("a room was promoted with no filter configured")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing", src.verdicts())
	}
}

// Our own first message cannot promote a room, the usual case of a new room.
func TestOurOwnMessageNeverPromotes(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	n, _ := spamNotifier(t, spamConfig(config.Spam{}), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, me, "bitcoin, as we discussed")); notified {
		t.Error("our own message notified")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing: it was our own message", src.verdicts())
	}
}

// A rule can name a tag-picked room to let one conversation through a silence.
func TestAPickedRoomIsAPlaceARuleCanName(t *testing.T) {
	t.Parallel()

	cfg := notifsOn("none")
	cfg.Tags = []config.Tag{{Name: "Pinned", Picked: []string{chatNamEntry}}}
	cfg.Notifications.Rules = []config.Rule{{Match: "tag:Pinned", Show: "all"}}
	n, rec, _ := notifier(t, cfg)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "standup in five")); !notified {
		t.Fatal("a rule naming tag:Pinned did not reach a room the tag picked")
	}
	if len(rec.all()) != 1 {
		t.Errorf("delivered %d, want the one that was let through", len(rec.all()))
	}
	// The unpinned room stays silent, so the rule, not the level, does the work.
	if _, notified := n.Deliver(context.Background(), msg(dmRoom, alice, "hi")); notified {
		t.Error("an unpinned room was let through by a rule about pinned ones")
	}
}

// A release in account data, from any machine, outranks every rule.
func TestAReleaseInAccountDataOutranksEveryRule(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	src.caught = []domain.SpamVerdict{{Room: chatRm, Released: true}}
	n, _ := spamNotifier(t, spamConfig(config.Spam{}), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "double your bitcoin")); !notified {
		t.Error("a released room was silenced by a rule")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing: the room was released", src.verdicts())
	}
}

// A room released while this daemon runs leaves Spam here too.
func TestAReleaseReachesARunningDaemon(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	src.caught = []domain.SpamVerdict{{Room: chatRm, Rule: domain.SpamFirstMessage, Filter: "crypto"}}
	cfg := spamConfig(config.Spam{})
	n, _ := spamNotifier(t, cfg, src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "morning")); notified {
		t.Fatal("a caught room notified")
	}
	src.store(domain.SpamVerdict{Room: chatRm, Released: true})
	if err := n.Reload(cfg); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "double your bitcoin")); !notified {
		t.Error("a released room is still silenced by the verdict it was released from")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing: the room was released", src.verdicts())
	}
}

// A release this daemon had not heard of beats the rule; the message is delivered.
func TestARuleThatLosesToAReleaseDoesNotSilence(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	src.refuse = fmt.Errorf("matrix: record spam: %w", domain.ErrSpamReleased)
	n, _ := spamNotifier(t, spamConfig(config.Spam{}), src)

	ctx := context.Background()
	if _, notified := n.Deliver(ctx, msg(chatRm, alice, "double your bitcoin")); !notified {
		t.Error("a rule silenced a room the homeserver says was released")
	}
	// It is remembered: the next message does not try again.
	src.refuse = nil
	if _, notified := n.Deliver(ctx, msg(chatRm, alice, "more bitcoin")); !notified {
		t.Error("the second message was silenced")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing: the room was released", src.verdicts())
	}
}

// put adds one message to a room's cached history, age ago.
func (s *spamBackend) put(room domain.RoomID, sender, body string, age time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.timeline[room])
	s.timeline[room] = append(s.timeline[room], domain.Message{
		ID: domain.EventID(fmt.Sprintf("$%s%d", room, n)), RoomID: room, Sender: sender, Body: body,
		Timestamp: time.Now().Add(-age),
	})
}

// A rescan judges cached history by the rules that hold for an old message: always,
// direct and ratio, never first_message. It skips exempt, released and caught rooms
// and your own messages, previews without moving, and applies only the rooms named.
func TestARescanJudgesCachedHistory(t *testing.T) {
	t.Parallel()
	const (
		dmWritten = "!dm2:example.org"
		group     = "!group:example.org"
		exempt    = "!exempt:example.org"
		released  = "!released:example.org"
		mineOnly  = "!mine:example.org"
		day       = 24 * time.Hour
	)
	src := spamWorld()
	src.rooms = append(src.rooms,
		domain.Room{ID: dmWritten, Name: "Bob", IsDirect: true},
		domain.Room{ID: group, Name: "Group"},
		domain.Room{ID: exempt, Name: "Exempt"},
		domain.Room{ID: released, Name: "Released"},
		domain.Room{ID: mineOnly, Name: "Mine"},
	)
	src.spaces[0].Children = append(src.spaces[0].Children, exempt, released, mineOnly)
	src.put(chatRm, alice, "morning", 3*day)
	src.put(chatRm, alice, "go vote today", 2*day)
	src.put(dmRoom, alice, "double your bitcoin", 5*day)
	src.put(dmWritten, bob, "bitcoin?", day)
	src.put(dmWritten, me, "no thanks", day)
	src.put(group, bob, "bitcoin bitcoin", day) // first in the room, but that rule is out
	src.put(exempt, alice, "vote", day)
	src.put(released, alice, "vote", day)
	src.put(mineOnly, me, "I will vote", day)
	src.store(domain.SpamVerdict{Room: released, Released: true})
	n, _ := spamNotifier(t, spamConfig(config.Spam{
		Except: []string{"room:Exempt"},
		Filters: []config.SpamFilter{
			{Name: "crypto", Words: []string{"*bitcoin*"}},
			{Name: "elections", Words: []string{"vote"}, In: []string{"space:" + space}, Moves: []string{"always"}},
		},
	}), src)
	ctx := context.Background()

	found, err := n.RescanSpam(ctx, time.Time{}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[domain.RoomID]domain.SpamCandidate{}
	for _, c := range found {
		got[c.Verdict.Room] = c
	}
	if c := got[chatRm]; len(got) != 2 || c.Verdict.Rule != domain.SpamAlways || c.Verdict.Filter != "elections" || c.Sample.Body != "go vote today" {
		t.Fatalf("candidates = %+v, want the Work room by elections and the unanswered DM", found)
	}
	if c := got[dmRoom]; c.Verdict.Rule != domain.SpamDirect || c.Verdict.Filter != "crypto" {
		t.Errorf("DM candidate = %+v, want the direct rule by crypto", c)
	}
	if len(src.verdicts()) != 0 {
		t.Fatalf("a preview recorded %+v", src.verdicts())
	}

	recent, err := n.RescanSpam(ctx, time.Now().Add(-36*time.Hour), false, nil)
	if err != nil || len(recent) != 0 {
		t.Errorf("since 36h ago = %+v (%v), want nothing: both catches are older", recent, err)
	}

	moved, err := n.RescanSpam(ctx, time.Time{}, true, []domain.RoomID{chatRm})
	if err != nil || len(moved) != 1 || moved[0].Verdict.Room != chatRm {
		t.Fatalf("apply = %+v (%v), want the Work room alone", moved, err)
	}
	if marked := src.verdicts(); len(marked) != 1 || marked[0].Room != chatRm || marked[0].Rule != domain.SpamAlways {
		t.Fatalf("recorded %+v, want the Work room alone", marked)
	}
	again, _ := n.RescanSpam(ctx, time.Time{}, false, nil)
	if len(again) != 1 || again[0].Verdict.Room != dmRoom {
		t.Errorf("after moving, a preview = %+v, want only the DM left", again)
	}
}
