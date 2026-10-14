package linkedin

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A mention is marked by rune offsets, each name after the one before; mentions of
// people on other networks, or with no name, are not LinkedIn's to mark.
func TestMentionsAreMarkedByRunes(t *testing.T) {
	t.Parallel()

	body := "שלום Dana Reyes and 🙂 Eli Stone, Dana Reyes again, and Danny on Matrix"
	attrs := mentionAttributes(body, []domain.Mention{
		{UserID: personID(dana), Name: "Dana Reyes"},
		{UserID: personID(eli), Name: "Eli Stone"},
		{UserID: personID(dana), Name: "Dana Reyes"},
		{UserID: "@danny:example.org", Name: "Danny"},
		{UserID: personID(eli), Name: ""},
		{UserID: personID(eli), Name: "Nobody here"},
	})
	want := []struct{ start, length int }{{5, 10}, {22, 9}, {33, 10}}
	if len(attrs) != len(want) {
		t.Fatalf("attributes = %+v", attrs)
	}
	for i, w := range want {
		a := attrs[i]
		if a.Start != w.start || a.Length != w.length || a.AttributeKindUnion.Entity == nil {
			t.Errorf("attribute %d = %+v, want %+v", i, a, w)
		}
	}
	if got := attrs[1].AttributeKindUnion.Entity.URN.String(); got != "urn:li:fsd_profile:"+eli {
		t.Errorf("Eli's mention names %s", got)
	}
}

// An origin token is a fresh random UUID, as LinkedIn's web app sends.
func TestOriginTokens(t *testing.T) {
	t.Parallel()

	shape := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 100 {
		tok := newToken()
		if !shape.MatchString(tok) || seen[tok] {
			t.Fatalf("token %q (seen %v)", tok, seen[tok])
		}
		seen[tok] = true
	}
}

// Messages and reactions keep their LinkedIn IDs through kith's.
func TestMessageAndReactionIDs(t *testing.T) {
	t.Parallel()

	mid := "(urn:li:fsd_profile:" + dana + ",2-bWVz/YWdl+==)"
	s, th, m, ok := messageOf(eventID(me, group, mid))
	if !ok || s != me || th != group || m != mid || messageURN(m).String() != "urn:li:msg_message:"+mid {
		t.Errorf("messageOf = %q %q %q %v", s, th, m, ok)
	}
	for _, bad := range []domain.EventID{"$ev:example.org", "linkedin:" + me + "/onlyroom", "linkedin:" + me + "/t/%zz"} {
		if _, _, _, ok := messageOf(bad); ok {
			t.Errorf("messageOf(%s) took it", bad)
		}
	}
	a, b := reactionID(me, group, mid, dana, "👍"), reactionID(me, group, mid, eli, "👍")
	if a == b || a == reactionID(me, group, mid, dana, "❤️") || a != reactionID(me, group, mid, dana, "👍") {
		t.Error("reaction IDs are not one per person and emoji")
	}
}

func reactionEvent(m linkedingo.Message, by, emoji string, added bool) *linkedingo.DecoratedEvent {
	return liveEvent(linkedingo.RealtimeEventTopicMessageReactionSummaries, linkedingo.DecoratedEventData{
		DecoratedReactionSummary: &linkedingo.DecoratedReactionSummary{Result: linkedingo.RealtimeReactionSummary{
			ReactionAdded: added, Actor: people[by], Message: m,
			ReactionSummary: linkedingo.ReactionSummary{Emoji: emoji, Count: 1},
		}},
	}, 1)
}

// Someone reacting shows; taking it back takes it away; each person's reaction is their
// own; an event missing its message, actor or emoji changes nothing.
func TestLiveReactions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	room := roomID(me, group)
	m := message(group, "m1", dana, "hi", 1000)
	a.takeConversations(ctx, c, []linkedingo.Conversation{withMessages(conversation(group, "G", 1000, 0, true, me, dana, eli), m)}, nil)
	keys := func() map[string]int {
		rs, err := cache.Reactions(ctx, room)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, r := range rs {
			out[r.Key+" "+domain.ParseID(r.Sender).Native]++
		}
		return out
	}
	a.event(ctx, c, reactionEvent(m, dana, "👍", true))
	a.event(ctx, c, reactionEvent(m, eli, "👍", true))
	if got := keys(); got["👍 "+dana] != 1 || got["👍 "+eli] != 1 || len(got) != 2 {
		t.Errorf("after two reactions: %v", got)
	}
	a.event(ctx, c, reactionEvent(m, dana, "👍", false))
	if got := keys(); got["👍 "+dana] != 0 || got["👍 "+eli] != 1 {
		t.Errorf("after Dana took hers back: %v", got)
	}
	noActor := reactionEvent(m, dana, "😮", true)
	noActor.Payload.Data.DecoratedReactionSummary.Result.Actor = linkedingo.MessagingParticipant{}
	a.event(ctx, c, noActor)
	a.event(ctx, c, reactionEvent(m, dana, "", true))
	a.event(ctx, c, reactionEvent(linkedingo.Message{}, dana, "😮", true))
	a.event(ctx, c, liveEvent(linkedingo.RealtimeEventTopicMessageReactionSummaries, linkedingo.DecoratedEventData{}, 1))
	if got := keys(); len(got) != 1 {
		t.Errorf("incomplete events changed the reactions: %v", got)
	}
	select {
	case u := <-a.Reactions():
		if u.Reaction.Key != "👍" || u.Removed {
			t.Errorf("first streamed reaction = %+v", u)
		}
	case <-time.After(time.Second):
		t.Error("no reaction was streamed")
	}
}

// A reply quotes the cached original: its sender as a participant, its time, its
// message and its words; one not cached, or not LinkedIn's, cannot be replied to.
func TestAReplyQuotesTheOriginal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, _, _ := cachedAdapter(t)
	room := roomID(me, group)
	a.takeConversations(ctx, c, []linkedingo.Conversation{
		withMessages(conversation(group, "G", 1000, 0, true, me, dana, eli), message(group, "m1", dana, "the original", 1000)),
	}, nil)
	quote, err := a.replyTo(ctx, room, msgID(group, "m1"))
	if err != nil {
		t.Fatal(err)
	}
	if quote.OriginalSenderURN.String() != "urn:li:msg_messagingParticipant:urn:li:fsd_profile:"+dana ||
		quote.OriginalMessageURN.String() != "urn:li:msg_message:(urn:li:fsd_profile:"+me+",m1)" ||
		quote.OriginalSendAt.UnixMilli() != 1000 || quote.MessageBody.Text != "the original" {
		t.Errorf("quote = %+v", quote)
	}
	if _, err := a.replyTo(ctx, room, msgID(group, "nope")); !errors.Is(err, errUnknownTarget) {
		t.Errorf("an uncached original = %v", err)
	}
	if _, err := a.replyTo(ctx, room, "$ev:example.org"); !errors.Is(err, errNotAMessage) {
		t.Errorf("a Matrix event = %v", err)
	}
}

// Talking needs the account connected: each way of talking says so rather than keeping
// something here LinkedIn never heard of; an empty message is refused first.
func TestTalkingNeedsLinkedIn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, announced := cachedAdapter(t)
	room := roomID(me, group)
	a.takeConversations(ctx, c, []linkedingo.Conversation{
		withMessages(conversation(group, "G", 1000, 0, true, me, dana, eli), message(group, "m1", dana, "hi", 1000)),
	}, nil)
	target := msgID(group, "m1")
	for name, err := range map[string]error{
		"send":   a.Send(ctx, room, domain.Draft{Body: "hello"}),
		"edit":   a.Send(ctx, room, domain.Draft{Body: "fixed", Edits: target}),
		"delete": a.Redact(ctx, room, target, ""),
		"react":  a.SendReaction(ctx, room, target, "👍"),
		"typing": a.SendTyping(ctx, room, true, time.Second),
	} {
		if !errors.Is(err, api.ErrNetworkOff) {
			t.Errorf("%s while not connected = %v", name, err)
		}
	}
	if err := a.SendTyping(ctx, room, false, 0); err != nil {
		t.Errorf("stopping typing needs nothing: %v", err)
	}
	if err := a.Send(ctx, "!r:example.org", domain.Draft{Body: "x"}); !errors.Is(err, errNotAConversation) {
		t.Errorf("a Matrix room = %v", err)
	}
	c.client = &linkedingo.Client{} // connected, as far as the checks go: they come first
	if err := a.Send(ctx, room, domain.Draft{Body: "  "}); !errors.Is(err, errEmptyMessage) {
		t.Errorf("an empty message = %v", err)
	}
	for name, err := range map[string]error{
		"edit of a Matrix event": a.Send(ctx, room, domain.Draft{Body: "x", Edits: "$e:x"}),
		"delete of one":          a.Redact(ctx, room, "$e:x", ""),
		"reaction to one":        a.SendReaction(ctx, room, "$e:x", "👍"),
		"reply to one":           a.Send(ctx, room, domain.Draft{Body: "x", ReplyTo: "$e:x"}),
	} {
		if !errors.Is(err, errNotAMessage) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if msgs, _ := cache.Messages(ctx, room, 10); len(msgs) != 1 || len(*announced) != 0 {
		t.Errorf("a refused send changed the room: %d messages, %d announced", len(msgs), len(*announced))
	}
}

// A sent message and its echo on the realtime stream are one message.
func TestASentMessageAndItsEchoAreOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	room := roomID(me, group)
	a.takeConversations(ctx, c, []linkedingo.Conversation{conversation(group, "G", 1000, 0, true, me, dana, eli)}, nil)
	sent := message(group, "m9", me, "from kith", 2000)
	c.applying.Lock()
	msg, _ := c.inbox.message(group, &sent)
	a.store(ctx, c, change{live: []domain.Message{msg}}) // as Send keeps LinkedIn's answer
	c.applying.Unlock()
	a.event(ctx, c, liveMessage(sent, 2000)) // the echo
	if msgs, _ := cache.Messages(ctx, room, 10); len(msgs) != 1 || msgs[0].Body != "from kith" {
		t.Errorf("cached %+v", msgs)
	}
}

// History pages through the cache, oldest first, each page continuing from its oldest
// message; past the cache, LinkedIn is asked, which needs the account connected.
func TestHistoryPagesThroughTheCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, _, _ := cachedAdapter(t)
	room := roomID(me, group)
	var msgs []linkedingo.Message
	for i := range 5 {
		msgs = append(msgs, message(group, "m"+string(rune('0'+i)), dana, "n", int64(1000+i)))
	}
	a.takeConversations(ctx, c, []linkedingo.Conversation{withMessages(conversation(group, "G", 1004, 0, true, me, dana, eli), msgs...)}, nil)
	page, err := a.Timeline(ctx, room, "", 2)
	if err != nil || len(page.Messages) != 2 || page.Messages[1].ID != msgID(group, "m4") || page.Next != string(msgID(group, "m3")) {
		t.Fatalf("newest page = %+v, %v", page, err)
	}
	page, err = a.Timeline(ctx, room, page.Next, 2)
	if err != nil || len(page.Messages) != 2 || page.Messages[0].ID != msgID(group, "m1") || page.Next != string(msgID(group, "m1")) {
		t.Fatalf("second page = %+v, %v", page, err)
	}
	// One left in the cache: the rest is LinkedIn's, and the account is not connected.
	page, err = a.Timeline(ctx, room, page.Next, 2)
	if err != nil || len(page.Messages) != 1 || page.Next == "" {
		t.Errorf("the cache's last = %+v, %v", page, err)
	}
	if _, err := a.Timeline(ctx, room, string(msgID(group, "m0")), 2); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("past the cache, not connected = %v", err)
	}
	if page, _ := a.Timeline(ctx, room, "", 0); len(page.Messages) != 5 {
		t.Errorf("a page of no size is the default: %d", len(page.Messages))
	}
}
