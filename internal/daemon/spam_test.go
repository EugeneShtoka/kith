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

// spamBackend is the world() backend plus the three things promoting a room needs: what a
// room has said, what is already caught, and somewhere to record a verdict.
type spamBackend struct {
	*scopeBackend

	mu       sync.Mutex
	timeline map[domain.RoomID][]domain.Message
	caught   []domain.SpamVerdict
	marked   []domain.SpamVerdict
	// refuse is what MarkSpam answers, for the write the homeserver turns down.
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

// store replaces what the account data says, as a sync from another machine would.
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

// said fills a room's history, which is what "the first message" and "most of them" are
// asked about.
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

// spamConfig is a notifying config with one filter, plus whatever the case changes.
func spamConfig(spam config.Spam) config.Config {
	cfg := notifsOn("all")
	if len(spam.Filters) == 0 {
		spam.Filters = []config.SpamFilter{{Name: "crypto", Words: []string{"*bitcoin*", "invest*"}}}
	}
	cfg.Spam = spam
	return cfg
}

// spamNotifier is the daemon's notifier over a backend that can be caught.
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

// The strongest rule and the cheapest: in a new room the first message *is* the
// relationship, and the message that catches the room is the one that must not notify.
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

// In an established room a spam message is noise, not a relationship — so the strongest
// rule deliberately does not fire, and the message notifies like anything else.
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

// A DM you have written in is a conversation, and one caught message must not move it.
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

// A DM is one person, so a verdict on the message is a verdict on the sender and the sender
// is the room.
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

// The third rule, and the only one that can move a room that was fine yesterday. It takes
// three numbers because one is not enough: a share, a floor under it, and a window.
func TestMostOfTheMessagesPromotesAGroup(t *testing.T) {
	t.Parallel()

	src := spamWorld().said(chatRm,
		"double your bitcoin", "invest with us", "bitcoin bitcoin", "morning")
	ratio := config.Spam{Ratio: 0.6, Floor: 4}
	n, _ := spamNotifier(t, spamConfig(ratio), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "invest now")); notified {
		t.Error("a room that is mostly spam still notified")
	}
	marked := src.verdicts()
	if len(marked) != 1 || marked[0].Rule != domain.SpamMostly {
		t.Fatalf("verdicts = %+v, want one most-of-them promotion", marked)
	}
}

// Two of three is not evidence, which is what the floor is for.
func TestTheFloorStopsASmallSample(t *testing.T) {
	t.Parallel()

	src := spamWorld().said(chatRm, "invest with us", "morning")
	ratio := config.Spam{Ratio: 0.5, Floor: 10}
	n, _ := spamNotifier(t, spamConfig(ratio), src)

	if _, notified := n.Deliver(context.Background(), msg(chatRm, alice, "invest now")); !notified {
		t.Error("a room with three messages was promoted on a share of them")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want no promotion under the floor", src.verdicts())
	}
}

// The carve-out outranks every rule, which is what makes releasing a room final.
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

// A room you said was spam needs no rule and records no verdict — the statement is
// already in the config, which is the one place a person can edit it.
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

// A verdict made before this daemon started still holds, because it is read back from
// account data rather than remembered by the process that made it.
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

// With no filter written, nothing here can fire — which is why there is no `enabled`
// switch beside them.
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

// Our own first message in a room cannot promote it, which matters because the commonest
// way a room has exactly one message in it is that you just started it.
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

// A room a tag picked is one a rule can name — the documented way to let one
// conversation through a silence — and the fact has to exist on the side that decides.
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
	// And the room nobody pinned stays silent, so the rule is doing the work rather
	// than the level.
	if _, notified := n.Deliver(context.Background(), msg(dmRoom, alice, "hi")); notified {
		t.Error("an unpinned room was let through by a rule about pinned ones")
	}
}

// A release recorded in account data — made on this machine or any other — outranks every
// rule, exactly as the local exemption does.
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

// A room released while this daemon is running leaves Spam here too.
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

// When the homeserver says the room was released — a release this daemon had not heard
// of yet — the rule loses, and the message is delivered like any other.
func TestARuleThatLosesToAReleaseDoesNotSilence(t *testing.T) {
	t.Parallel()

	src := spamWorld()
	src.refuse = fmt.Errorf("matrix: record spam: %w", domain.ErrSpamReleased)
	n, _ := spamNotifier(t, spamConfig(config.Spam{}), src)

	ctx := context.Background()
	if _, notified := n.Deliver(ctx, msg(chatRm, alice, "double your bitcoin")); !notified {
		t.Error("a rule silenced a room the homeserver says was released")
	}
	// And it is remembered: the next message does not try again.
	src.refuse = nil
	if _, notified := n.Deliver(ctx, msg(chatRm, alice, "more bitcoin")); !notified {
		t.Error("the second message was silenced")
	}
	if len(src.verdicts()) != 0 {
		t.Errorf("recorded %+v, want nothing: the room was released", src.verdicts())
	}
}
