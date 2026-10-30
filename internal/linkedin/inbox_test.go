package linkedin

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"
	"go.mau.fi/util/jsontime"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

// Invented profiles and threads; thread IDs look like LinkedIn's and may hold a slash.
const (
	me      = "ACoAAme01"
	dana    = "ACoAAdana2"
	eli     = "ACoAAeli03"
	danaDM  = "2-ZGFuYS9tZQ=="
	group   = "2-Z3JvdXArYm9hcmQ/"
	acmeOrg = "ACoAAacme4"
)

func at(ms int64) jsontime.UnixMilli { return jsontime.UnixMilli{Time: time.UnixMilli(ms)} }

func member(id, first, last string) linkedingo.MessagingParticipant {
	return linkedingo.MessagingParticipant{
		EntityURN: linkedingo.NewURN("urn:li:msg_messagingParticipant:urn:li:fsd_profile:" + id),
		ParticipantType: linkedingo.ParticipantType{Member: &linkedingo.MemberParticipantInfo{
			FirstName: linkedingo.AttributedText{Text: first}, LastName: linkedingo.AttributedText{Text: last},
		}},
	}
}

func org(id, name string) linkedingo.MessagingParticipant {
	return linkedingo.MessagingParticipant{
		EntityURN: linkedingo.NewURN("urn:li:msg_messagingParticipant:urn:li:fsd_profile:" + id),
		ParticipantType: linkedingo.ParticipantType{Organization: &linkedingo.OrganizationParticipantInfo{
			Name: linkedingo.AttributedText{Text: name},
		}},
	}
}

var people = map[string]linkedingo.MessagingParticipant{
	me: member(me, "Eve", "Me"), dana: member(dana, "Dana", "Reyes"), eli: member(eli, "Eli", "Stone"),
	acmeOrg: org(acmeOrg, "Acme Recruiting"),
}

// conversation is a conversation as LinkedIn sends it.
func conversation(thread, title string, lastActive, lastRead int64, read bool, who ...string) linkedingo.Conversation {
	c := linkedingo.Conversation{
		Title: title, EntityURN: conversationURN(me, thread), GroupChat: len(who) > 2,
		LastActivityAt: at(lastActive), LastReadAt: at(lastRead), Read: read, Categories: []string{"INBOX", "PRIMARY_INBOX"},
	}
	for _, id := range who {
		c.ConversationParticipants = append(c.ConversationParticipants, people[id])
	}
	return c
}

// message is a message as LinkedIn sends it.
func message(thread, mid, sender, text string, sent int64) linkedingo.Message {
	return linkedingo.Message{
		EntityURN:               linkedingo.NewURN("urn:li:msg_message:(urn:li:fsd_profile:" + me + "," + mid + ")"),
		Sender:                  people[sender],
		Body:                    linkedingo.AttributedText{Text: text},
		DeliveredAt:             at(sent),
		MessageBodyRenderFormat: linkedingo.MessageBodyRenderFormatDefault,
		Conversation:            linkedingo.Conversation{EntityURN: conversationURN(me, thread)},
	}
}

// msgID is mid's event ID in thread.
func msgID(thread, mid string) domain.EventID {
	return eventID(me, thread, "(urn:li:fsd_profile:"+me+","+mid+")")
}

// Thread and message IDs survive kith's IDs whatever they hold, and rebuild LinkedIn's
// URNs; anything not of that shape is refused.
func TestIDsRoundTrip(t *testing.T) {
	t.Parallel()

	for _, thread := range []string{danaDM, group, "2-abc", "a/b/c", "x%2Fy", "sp ace"} {
		room := roomID(me, thread)
		self, got, ok := threadOf(room)
		if !ok || self != me || got != thread || !domain.IsRoomID(string(room)) {
			t.Errorf("threadOf(%s) = %q, %q, %v (room shape %v)", room, self, got, ok, domain.IsRoomID(string(room)))
		}
		if back, found := threadIn(conversationURN(me, thread)); !found || back != thread {
			t.Errorf("threadIn(conversationURN(%q)) = %q, %v", thread, back, found)
		}
		if event := eventID(me, thread, "(urn:li:fsd_profile:"+dana+",2-bWVz/YWdl+==)"); domain.IsRoomID(string(event)) {
			t.Errorf("an event reads as a room: %s", event)
		}
	}
	for _, bad := range []domain.RoomID{"!room:example.org", "slack:T1/C2", "linkedin:" + me, "linkedin:" + me + "/a/b", "linkedin:" + me + "/%zz"} {
		if _, _, ok := threadOf(bad); ok {
			t.Errorf("threadOf(%s) took it", bad)
		}
	}
	for _, bad := range []string{"urn:li:msg_conversation:plain", "urn:li:msg_conversation:(nocomma)", "urn:li:msg_conversation:(a,)"} {
		if _, ok := threadIn(linkedingo.NewURN(bad)); ok {
			t.Errorf("threadIn(%s) took it", bad)
		}
	}
	if accountSpaceID(me) != "linkedin:"+me+"/account" || !owner(me).Owns(roomID(me, group)) || owner(dana).Owns(roomID(me, group)) {
		t.Error("account space or owner")
	}
}

// Conversations become rooms as LinkedIn shows them: a direct one named after its
// person, a group by its title or else its people; spam, and ones you left, are not
// shown.
func TestTheInboxKeepsConversationsAsLinkedInShowsThem(t *testing.T) {
	t.Parallel()

	in := newInbox(me)
	take := func(c linkedingo.Conversation) bool {
		_, shown, _ := in.take(&c)
		return shown
	}
	take(conversation(danaDM, "", 10, 10, true, me, dana))
	take(conversation(group, "Board games", 20, 20, true, me, dana, eli))
	take(conversation("2-untitled", "", 30, 30, true, me, dana, eli))
	take(conversation("2-self", "", 5, 5, true, me))
	take(conversation("2-recruiter", "", 6, 6, true, me, acmeOrg))
	spam := conversation("2-spam", "", 40, 0, false, me, dana)
	spam.Categories = []string{"INBOX", categorySpam}
	if take(spam) {
		t.Error("a spam conversation was shown")
	}
	if take(conversation("2-left", "Old group", 50, 0, false, dana, eli)) {
		t.Error("a conversation you left was shown")
	}
	names := map[string]bool{}
	for _, cv := range in.convs {
		r := in.room(cv)
		names[fmt.Sprintf("%s direct=%v members=%d", r.Name, r.IsDirect, len(r.Members))] = true
	}
	for _, want := range []string{
		"Dana Reyes direct=true members=2", "Board games direct=false members=3",
		"Dana Reyes, Eli Stone direct=false members=3", noteToSelf + " direct=true members=1",
		"Acme Recruiting direct=true members=2",
	} {
		if !names[want] {
			t.Errorf("no room %q in %v", want, names)
		}
	}
	if len(in.convs) != 5 {
		t.Errorf("%d conversations kept, want 5", len(in.convs))
	}
	members := in.roomMembers(in.convs[group])
	if len(members) != 3 || members[0].UserID != personID(me) || members[2].DisplayName != "Eli Stone" {
		t.Errorf("members = %+v", members)
	}
	// A conversation event without participants keeps the ones known.
	bare := conversation(group, "Board games!", 25, 25, true)
	take(bare)
	if cv := in.convs[group]; len(cv.others) != 2 || cv.title != "Board games!" {
		t.Errorf("a bare update lost the participants: %+v", cv)
	}
	// Read times only move forward.
	take(conversation(group, "Board games!", 25, 1, false, me, dana, eli))
	if cv := in.convs[group]; cv.readTo.UnixMilli() != 25 || cv.read {
		t.Errorf("read state = %v %v", cv.readTo.UnixMilli(), cv.read)
	}
	var bad linkedingo.Conversation
	if thread, shown, _ := in.take(&bad); thread != "" || shown {
		t.Error("a conversation without a URN was taken")
	}
}

// What a message carries shows: text with its links' addresses, a reply, a forward
// quoted, the first attachment shown and the others as labels, a shared post as its
// address; a system notice
// does not show, a recalled message comes redacted.
func TestMessagesShowWhatTheyCarry(t *testing.T) {
	t.Parallel()

	in := newInbox(me)
	linked := message(group, "m1", dana, "see our site and https://b.example", 1000)
	linked.Body.Attributes = []linkedingo.Attribute{
		{Start: 4, Length: 8, AttributeKind: linkedingo.AttributeKind{Hyperlink: &linkedingo.Hyperlink{URL: "https://a.example"}}},
		{Start: 17, Length: 17, AttributeKind: linkedingo.AttributeKind{Hyperlink: &linkedingo.Hyperlink{URL: "https://b.example"}}},
		{Start: 0, Length: 99, AttributeKind: linkedingo.AttributeKind{Hyperlink: &linkedingo.Hyperlink{URL: "https://out.of.range"}}},
	}
	reply := message(group, "m2", eli, "agreed", 2000)
	reply.RenderContent = []linkedingo.RenderContent{{RepliedMessageContent: &linkedingo.RepliedMessage{OriginalMessage: message(group, "m1", dana, "", 1000)}}}
	forward := message(group, "m3", dana, "", 3000)
	forward.RenderContent = []linkedingo.RenderContent{{ForwardedMessage: &linkedingo.ForwardedMessage{
		ForwardedBody: linkedingo.AttributedText{Text: "line one\nline two"}, OriginalSender: people[eli],
	}}}
	files := message(group, "m4", dana, "", 4000)
	files.RenderContent = []linkedingo.RenderContent{
		{VectorImage: &linkedingo.VectorImage{}}, {File: &linkedingo.FileAttachment{Name: "cv.pdf"}},
		{Video: &linkedingo.VideoPlayMetadata{}}, {Audio: &linkedingo.AudioMetadata{}}, {ExternalMedia: &linkedingo.ExternalMedia{}},
		{File: &linkedingo.FileAttachment{}},
		{HostURNData: &linkedingo.HostURNData{Type: "FEED_UPDATE", HostURN: linkedingo.NewURN("urn:li:activity:7")}},
		{HostURNData: &linkedingo.HostURNData{Type: "JOB_POSTING", HostURN: linkedingo.NewURN("urn:li:job:9")}},
	}
	mention := message(group, "m5", dana, "@Eve look", 5000)
	mention.Body.Attributes = []linkedingo.Attribute{{Start: 0, Length: 4, AttributeKind: linkedingo.AttributeKind{
		Entity: &linkedingo.Entity{URN: linkedingo.NewURN("urn:li:fsd_profile:" + me)},
	}}}
	edited := message(group, "m6", dana, "fixed", 6000)
	edited.MessageBodyRenderFormat = linkedingo.MessageBodyRenderFormatEdited
	recalled := message(group, "m7", dana, "", 7000)
	recalled.MessageBodyRenderFormat = linkedingo.MessageBodyRenderFormatRecalled

	want := map[*linkedingo.Message]string{
		&linked:  "see our site (https://a.example) and https://b.example",
		&reply:   "agreed",
		&forward: "Forwarded from Eli Stone:\n> line one\n> line two",
		&files: "[file: cv.pdf]\n[video]\n[voice message]\n[GIF]\n[file]\n" +
			"https://www.linkedin.com/feed/update/urn:li:activity:7\n[job posting]",
		&mention: "@Eve look",
		&edited:  "fixed",
	}
	for m, body := range want {
		got, ok := in.message(group, m)
		if !ok || got.Body != body {
			t.Errorf("%s: %q (%v), want %q", m.EntityURN, got.Body, ok, body)
		}
		if got.RoomID != roomID(me, group) || got.Timestamp != m.DeliveredAt.Time || got.SenderName == "" {
			t.Errorf("%s: room %s time %v sender %q", m.EntityURN, got.RoomID, got.Timestamp, got.SenderName)
		}
	}
	if got, _ := in.message(group, &files); !got.Media.IsImage() {
		t.Errorf("the first attachment shows as %+v, want the photo", got.Media)
	}
	if got, _ := in.message(group, &reply); got.ReplyTo != msgID(group, "m1") {
		t.Errorf("reply to %s", got.ReplyTo)
	}
	if got, _ := in.message(group, &mention); !got.Mentioned {
		t.Error("a mention of me is not marked")
	}
	if got, _ := in.message(group, &linked); got.Mentioned {
		t.Error("a message with no mention is marked")
	}
	other := message(group, "m11", dana, "@Eli look", 11)
	other.Body.Attributes = []linkedingo.Attribute{{Start: 0, Length: 4, AttributeKind: linkedingo.AttributeKind{
		Entity: &linkedingo.Entity{URN: linkedingo.NewURN("urn:li:fsd_profile:" + eli)},
	}}}
	if got, _ := in.message(group, &other); got.Mentioned {
		t.Error("a mention of someone else is marked as mine")
	}
	if got, _ := in.message(group, &edited); !got.Edited {
		t.Error("an edited message is not marked")
	}
	if got, ok := in.message(group, &recalled); !ok || !got.Redacted || got.RedactedBy != personID(dana) {
		t.Errorf("recalled = %+v", got)
	}
	system := message(group, "m8", dana, "Dana joined", 8000)
	system.MessageBodyRenderFormat = linkedingo.MessageBodyRenderFormatSystem
	empty := message(group, "m9", dana, "  ", 9000)
	var noURN linkedingo.Message
	for _, m := range []*linkedingo.Message{&system, &empty, &noURN} {
		if got, ok := in.message(group, m); ok {
			t.Errorf("shown: %+v", got)
		}
	}
	anon := message(group, "m10", dana, "", 10)
	anon.RenderContent = []linkedingo.RenderContent{{ForwardedMessage: &linkedingo.ForwardedMessage{ForwardedBody: linkedingo.AttributedText{Text: "x"}}}}
	if got, _ := in.message(group, &anon); got.Body != "Forwarded:\n> x" {
		t.Errorf("an anonymous forward = %q", got.Body)
	}
}

// cachedAdapter is an adapter over a real cache with one connection, its announced
// messages collected.
func cachedAdapter(t *testing.T) (*Adapter, *conn, *db.Cache, *[]domain.Message) {
	t.Helper()
	return cachedAdapterAt(t, filepath.Join(t.TempDir(), "cache.db"))
}

// cachedAdapterAt is cachedAdapter over the cache file at path.
func cachedAdapterAt(t *testing.T, path string) (*Adapter, *conn, *db.Cache, *[]domain.Message) {
	t.Helper()
	cache, err := db.Open(context.Background(), path, make([]byte, sealeddb.KeyLen))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	a := New(cache, newSecrets(), nil, nil)
	var announced []domain.Message
	a.OnCached(func(m domain.Message) { announced = append(announced, m) }, nil)
	a.UseConfig(context.Background(), withAccounts("li"))
	c := a.newConn(Account{Name: "li"}, aSession(me, "x"), nil)
	a.adopt(c)
	return a, c, cache, &announced
}

func cachedRooms(t *testing.T, cache *db.Cache) []string {
	t.Helper()
	rooms, err := cache.Rooms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i := range rooms {
		if domain.NetworkOf(string(rooms[i].ID)) == domain.ProtocolLinkedIn {
			names = append(names, rooms[i].Name)
		}
	}
	slices.Sort(names)
	return names
}

// withMessages is c carrying msgs as its newest.
func withMessages(c linkedingo.Conversation, msgs ...linkedingo.Message) linkedingo.Conversation {
	c.Messages.Elements = msgs
	return c
}

// A later, shorter listing must not sweep a conversation an earlier one listed or the
// cache already held; only LinkedIn's deletes take one away.
func TestALaterListingNeverSweepsAnEarlierConversation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	a.takeConversations(ctx, c, []linkedingo.Conversation{
		conversation(group, "Board games", 10, 10, true, me, dana, eli),
		conversation(danaDM, "", 5, 5, true, me, dana),
	}, nil)
	c2 := a.newConn(Account{Name: "li"}, aSession(me, "y"), nil)
	a.adopt(c2)
	a.seedRooms(ctx, c2)
	a.takeConversations(ctx, c2, []linkedingo.Conversation{conversation("2-new", "New group", 30, 30, true, me, dana, eli)}, nil)
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"Board games", "Dana Reyes", "New group"}) {
		t.Errorf("rooms after a shorter listing = %v", got)
	}
	a.takeConversations(ctx, c2, nil, []string{"2-new"})
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"Board games", "Dana Reyes"}) {
		t.Errorf("rooms after a delete = %v", got)
	}
	a.takeConversations(ctx, c2, nil, []string{group, danaDM})
	if got := cachedRooms(t, cache); len(got) != 0 {
		t.Errorf("rooms after deleting the rest = %v", got)
	}
}

// A conversation LinkedIn deleted is not brought back by a listing fetched before the
// deletion; new activity after it brings it back.
func TestADeletedConversationStaysDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	old := conversation(group, "Board games", time.Now().Add(-time.Hour).UnixMilli(), 0, true, me, dana, eli)
	a.takeConversations(ctx, c, []linkedingo.Conversation{old}, nil)
	a.event(ctx, c, &linkedingo.DecoratedEvent{
		Topic: linkedingo.NewURN("urn:li-realtime:conversationDeletesTopic:urn:li-realtime:myself"),
		Payload: linkedingo.DecoratedEventPayload{Data: linkedingo.DecoratedEventData{
			DecoratedConversationDelete: &linkedingo.DecoratedConversation{Result: old},
		}},
	})
	a.takeConversations(ctx, c, []linkedingo.Conversation{old}, nil) // a listing from before
	if got := cachedRooms(t, cache); len(got) != 0 {
		t.Errorf("a listing from before the deletion brought back %v", got)
	}
	newer := old
	newer.LastActivityAt = at(time.Now().Add(time.Hour).UnixMilli())
	a.takeConversations(ctx, c, []linkedingo.Conversation{newer}, nil)
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"Board games"}) {
		t.Errorf("new activity did not bring it back: %v", got)
	}
	// Once back, an update that carries no time (a rename) keeps it.
	renamed := conversation(group, "Board games night", 0, 0, true, me, dana, eli)
	renamed.LastActivityAt = jsontime.UnixMilli{}
	a.takeConversations(ctx, c, []linkedingo.Conversation{renamed}, nil)
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"Board games night"}) {
		t.Errorf("an update after coming back = %v", got)
	}
}

// liveEvent is a realtime event on topic carrying data.
func liveEvent(topic string, data linkedingo.DecoratedEventData, left int64) *linkedingo.DecoratedEvent {
	return &linkedingo.DecoratedEvent{
		Topic: linkedingo.NewURN("urn:li-realtime:" + topic + ":urn:li-realtime:myself"), LeftServerAt: at(left),
		Payload: linkedingo.DecoratedEventPayload{Data: data},
	}
}

func liveMessage(m linkedingo.Message, left int64) *linkedingo.DecoratedEvent {
	return liveEvent(linkedingo.RealtimeEventTopicMessages, linkedingo.DecoratedEventData{DecoratedMessage: &linkedingo.DecoratedMessage{Result: m}}, left)
}

// forRun is one generator run's conversation and the three shapes of its message.
func forRun(thread string) (conv linkedingo.Conversation, original, edit, again, recall linkedingo.Message) {
	conv = conversation(thread, "G", 1000, 0, true, me, dana, eli)
	original = message(thread, "m1", dana, "first words", 1000)
	edit = message(thread, "m1", dana, "second words", 1000)
	edit.MessageBodyRenderFormat = linkedingo.MessageBodyRenderFormatEdited
	again = message(thread, "m1", dana, "third words", 1000)
	again.MessageBodyRenderFormat = linkedingo.MessageBodyRenderFormatEdited
	recall = message(thread, "m1", dana, "", 1000)
	recall.MessageBodyRenderFormat = linkedingo.MessageBodyRenderFormatRecalled
	return conv, original, edit, again, recall
}

// step is one way the message reaches kith.
type step struct {
	name string
	do   func(a *Adapter, c *conn)
}

func runSteps(ctx context.Context, conv linkedingo.Conversation, original, edit, again, recall linkedingo.Message) []step {
	return []step{
		{"listed as sent", func(a *Adapter, c *conn) {
			a.takeConversations(ctx, c, []linkedingo.Conversation{withMessages(conv, original)}, nil)
		}},
		{"listed as edited", func(a *Adapter, c *conn) {
			a.takeConversations(ctx, c, []linkedingo.Conversation{withMessages(conv, edit)}, nil)
		}},
		{"edited live", func(a *Adapter, c *conn) { a.event(ctx, c, liveMessage(edit, 2000)) }},
		{"edited again live", func(a *Adapter, c *conn) { a.event(ctx, c, liveMessage(again, 2500)) }},
		{"recalled live", func(a *Adapter, c *conn) { a.event(ctx, c, liveMessage(recall, 3000)) }},
		{"listed as recalled", func(a *Adapter, c *conn) {
			a.takeConversations(ctx, c, []linkedingo.Conversation{withMessages(conv, recall)}, nil)
		}},
		{"sent live", func(a *Adapter, c *conn) { a.event(ctx, c, liveMessage(original, 1000)) }},
	}
}

// One message reaches kith three ways: a listing's copy (as sent, or as edited or
// recalled when the listing is newer), a live edit, a live recall. Whatever the order,
// what is cached is the newest truth: recalled once recalled anywhere, else the later
// edit once made, else the edit, else the original. A stale copy never undoes an edit
// or a recall.
func TestASyncNeverUndoesALiveEditOrRecall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(3, 141))
	a, c, cache, _ := cachedAdapter(t)
	for run := range 60 {
		for _, keep := range []bool{false, true} {
			// Each run is its own conversation: a fresh message, no state carried over.
			thread := fmt.Sprintf("2-run%d-%v", run, keep)
			conv, original, edit, again, recall := forRun(thread)
			steps := runSteps(ctx, conv, original, edit, again, recall)
			a.keepDeleted.Store(keep)
			var names []string
			edited, editedAgain, recalled, seen := false, false, false, false
			for range 1 + rng.IntN(5) {
				s := steps[rng.IntN(len(steps))]
				s.do(a, c)
				names = append(names, s.name)
				switch s.name {
				case "edited live", "listed as edited":
					edited, seen = true, true
				case "edited again live":
					editedAgain, seen = true, true
				case "recalled live", "listed as recalled":
					recalled = true
				default:
					seen = true
				}
			}
			got, ok, err := cache.MessageByID(ctx, roomID(me, thread), msgID(thread, "m1"))
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case recalled:
				if ok && !got.Redacted {
					t.Fatalf("run %d keep=%v: after %v the message shows %q, not recalled", run, keep, names, got.Body)
				}
				if ok && !keep && got.Body != "" {
					t.Fatalf("run %d: after %v a recalled message keeps %q", run, names, got.Body)
				}
			case editedAgain:
				// The later edit stands, whatever stale copy of the first came after.
				if !ok || got.Body != "third words" {
					t.Fatalf("run %d keep=%v: after %v the message is %q (%v), want the later edit", run, keep, names, got.Body, ok)
				}
			case edited:
				if !ok || got.Body != "second words" {
					t.Fatalf("run %d keep=%v: after %v the message is %q (%v), want the edit", run, keep, names, got.Body, ok)
				}
			case seen:
				if !ok || got.Body != "first words" {
					t.Fatalf("run %d keep=%v: after %v the message is %q (%v)", run, keep, names, got.Body, ok)
				}
			}
		}
	}
}

// A conversation read in kith stays read whatever LinkedIn says after: an older read
// time, its unread flag. Only a newer message makes it unread; reading it on another
// device reads it here.
func TestAConversationReadInKithStaysRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	room := roomID(me, danaDM)
	listed := withMessages(conversation(danaDM, "", 3000, 1000, false, me, dana),
		message(danaDM, "m2", dana, "two", 2000), message(danaDM, "m3", dana, "three", 3000))
	a.takeConversations(ctx, c, []linkedingo.Conversation{listed}, nil)
	unread := func() int {
		n, _, counted, err := cache.CountUnread(ctx, a.Me(), room)
		if err != nil || !counted {
			t.Fatalf("count: %d, counted %v, %v", n, counted, err)
		}
		return n
	}
	if n := unread(); n != 2 {
		t.Fatalf("before reading: %d unread, want 2", n)
	}
	if err := a.MarkRead(ctx, room, msgID(danaDM, "m3"), false); err != nil {
		t.Fatal(err)
	}
	a.takeConversations(ctx, c, []linkedingo.Conversation{listed}, nil) // LinkedIn's stale view
	if n := unread(); n != 0 {
		t.Errorf("after reading in kith and LinkedIn's stale view: %d unread, want 0", n)
	}
	if owedMarks := a.reads.Take(me); len(owedMarks) != 1 || owedMarks[room] != 3000 {
		t.Errorf("the read mark LinkedIn has not taken is not owed: %v", owedMarks)
	}
	a.event(ctx, c, liveMessage(message(danaDM, "m4", dana, "four", 4000), 4000))
	if n := unread(); n != 1 {
		t.Errorf("after a newer message: %d unread, want 1", n)
	}
	// Read on the phone: your own receipt reads it here; Dana's does not.
	receipt := func(by string, seenAt int64) *linkedingo.DecoratedEvent {
		return liveEvent(linkedingo.RealtimeEventTopicMessageSeenReceipts, linkedingo.DecoratedEventData{
			DecoratedSeenReceipt: &linkedingo.DecoratedSeenReceipt{Result: linkedingo.SeenReceipt{
				SeenAt: at(seenAt), Message: message(danaDM, "m4", dana, "", 4000), SeenByParticipant: people[by],
			}},
		}, seenAt)
	}
	a.event(ctx, c, receipt(dana, 4500))
	if n := unread(); n != 1 {
		t.Errorf("after Dana's receipt: %d unread, want 1", n)
	}
	a.event(ctx, c, receipt(me, 4500))
	if n := unread(); n != 0 {
		t.Errorf("after reading on another device: %d unread, want 0", n)
	}
	if _, err := a.MarkRoomsRead(ctx, []domain.RoomID{room, roomID(me, "2-nothing")}, false); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkRead(ctx, "!other:example.org", "", false); err == nil {
		t.Error("marking a room of another network read was taken")
	}
}

// A conversation's unread mark follows its last word, wherever it was said: marked on
// another device, read there, a newer message, read in kith (LinkedIn told late, or a
// listing from before it arriving after), the mark cleared in kith, or a mark from
// kith that LinkedIn could not take. A read in kith is never undone by LinkedIn's
// view from before it.
func TestAnUnreadMarkFollowsItsLastWord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(60) {
		r := rand.New(rand.NewPCG(seed, 11))
		a, c, cache, _ := cachedAdapter(t)
		room := roomID(me, danaDM)
		last, liRead := int64(1000), int64(1000) // newest activity; LinkedIn's read time
		listing := func(read bool) linkedingo.Conversation {
			return withMessages(conversation(danaDM, "", last, liRead, read, me, dana))
		}
		a.takeConversations(ctx, c, []linkedingo.Conversation{withMessages(
			conversation(danaDM, "", last, liRead, true, me, dana), message(danaDM, "m0", dana, "zero", last))}, nil)
		marked, n, liReadFlag := false, 0, true // liReadFlag: LinkedIn's own read flag
		var owedAt []int64                      // kith's reads LinkedIn has not taken
		var steps []string
		for range 12 {
			switch r.IntN(7) {
			case 0: // marked unread on another device
				steps = append(steps, "marked elsewhere")
				liReadFlag = false
				a.takeConversations(ctx, c, []linkedingo.Conversation{listing(false)}, nil)
				marked = liRead >= last && len(owedAt) == 0
			case 1: // read on another device
				steps = append(steps, "read elsewhere")
				liRead, liReadFlag = last, true
				a.takeConversations(ctx, c, []linkedingo.Conversation{listing(true)}, nil)
				marked = false
			case 2: // a newer message
				n++
				last += 1000
				steps = append(steps, fmt.Sprint("message m", n))
				a.event(ctx, c, liveMessage(message(danaDM, fmt.Sprint("m", n), dana, "more", last), last))
				liReadFlag = false
				a.takeConversations(ctx, c, []linkedingo.Conversation{listing(false)}, nil)
				marked = false
			case 3: // read in kith, or the mark cleared in kith (which reads it)
				steps = append(steps, "read in kith")
				var err error
				if r.IntN(2) == 0 {
					err = a.MarkRead(ctx, room, msgID(danaDM, fmt.Sprint("m", n)), false)
				} else {
					err = a.MarkRoomUnread(ctx, room, false)
				}
				if err != nil {
					t.Fatalf("seed %d %v: %v", seed, steps, err)
				}
				owedAt = append(owedAt, last)
				marked = false
			case 4: // LinkedIn takes kith's oldest owed read
				if len(owedAt) == 0 {
					continue
				}
				steps = append(steps, "LinkedIn takes a read")
				a.reads.Settle(room, owedAt[0])
				if owedAt[0] >= owedAt[len(owedAt)-1] {
					owedAt = nil
				} else {
					owedAt = owedAt[1:]
				}
				liRead, liReadFlag = max(liRead, last), true
			case 5: // a listing from before kith's read: LinkedIn's view as it last was
				steps = append(steps, "a stale listing")
				a.takeConversations(ctx, c, []linkedingo.Conversation{listing(liReadFlag)}, nil)
				marked = !liReadFlag && liRead >= last && len(owedAt) == 0
			default: // marked unread in kith, which LinkedIn cannot take while offline
				steps = append(steps, "marked in kith (offline)")
				if err := a.MarkRoomUnread(ctx, room, true); !errors.Is(err, errNotConnected) {
					t.Fatalf("seed %d %v: marking unread offline: %v", seed, steps, err)
				}
			}
			rows, err := cache.Unread(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got := false
			for _, u := range rows {
				if u.RoomID == room {
					got = u.Marked
				}
			}
			if got != marked {
				t.Fatalf("seed %d %v: marked %v, want %v", seed, steps, got, marked)
			}
		}
	}
}

// A read is kept here first or not at all, and LinkedIn is owed the newest one until it
// takes it: in any order of new messages, reads whose message lookup or position save
// fails, and LinkedIn taking an earlier read's mark late, a failed read is an error that
// reads nothing and owes nothing, nothing unseen counts as read, and a late answer for
// an earlier read never settles a newer one.
func TestAFailedReadReadsNothingAndALateAnswerSettlesOnlyItsOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(40) {
		r := rand.New(rand.NewPCG(seed, 7))
		path := filepath.Join(t.TempDir(), "cache.db")
		a, c, cache, _ := cachedAdapterAt(t, path)
		raw, err := sealeddb.OpenWith(ctx, path, make([]byte, sealeddb.KeyLen), sealeddb.Setup{Init: fts5.Register})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = raw.Close() })
		room := roomID(me, danaDM)
		sent := []int64{1000}
		a.takeConversations(ctx, c, []linkedingo.Conversation{withMessages(
			conversation(danaDM, "", 1000, 500, false, me, dana), message(danaDM, "m0", dana, "zero", 1000))}, nil)
		readTo, owedTo := int64(500), int64(0) // what kith has read through, and owes LinkedIn
		var taken []int64                      // the marks sent, any of which LinkedIn may answer late
		fail := func(table string) func() {
			if _, err := raw.ExecContext(ctx, "ALTER TABLE "+table+" RENAME TO "+table+"_away"); err != nil {
				t.Fatal(err)
			}
			return func() {
				if _, err := raw.ExecContext(ctx, "ALTER TABLE "+table+"_away RENAME TO "+table); err != nil {
					t.Fatal(err)
				}
			}
		}
		var steps []string
		for range 12 {
			switch r.IntN(3) {
			case 0: // a new message
				at := sent[len(sent)-1] + 1000
				mid := fmt.Sprint("m", len(sent))
				sent = append(sent, at)
				a.event(ctx, c, liveMessage(message(danaDM, mid, dana, "more", at), at))
				steps = append(steps, "message "+mid)
			case 1: // read through a message, maybe while the cache fails
				i := r.IntN(len(sent))
				event := msgID(danaDM, fmt.Sprint("m", i))
				switch r.IntN(3) {
				case 0:
					steps = append(steps, fmt.Sprint("read m", i))
					if err := a.MarkRead(ctx, room, event, false); err != nil {
						t.Fatalf("seed %d %v: %v", seed, steps, err)
					}
					readTo, owedTo = max(readTo, sent[i]), max(owedTo, sent[i])
					taken = append(taken, sent[i])
				case 1:
					steps = append(steps, fmt.Sprint("read m", i, " (lookup fails)"))
					undo := fail("messages")
					err := a.MarkRead(ctx, room, event, false)
					undo()
					if err == nil {
						t.Fatalf("seed %d %v: a failed lookup was taken as a read", seed, steps)
					}
				default:
					steps = append(steps, fmt.Sprint("read m", i, " (save fails)"))
					undo := fail("room_unread")
					res, err := a.MarkRoomsRead(ctx, []domain.RoomID{room}, false)
					undo()
					if err == nil && res.Marked != 0 {
						t.Fatalf("seed %d %v: a read not kept here counts as marked: %+v", seed, steps, res)
					}
				}
			default: // LinkedIn answers an earlier mark
				if len(taken) == 0 {
					continue
				}
				at := taken[r.IntN(len(taken))]
				steps = append(steps, fmt.Sprint("LinkedIn takes ", at))
				a.reads.Settle(room, at)
				if owedTo <= at {
					owedTo = 0
				}
			}
			want := 0
			for _, at := range sent {
				if at > readTo {
					want++
				}
			}
			n, _, _, err := cache.CountUnread(ctx, a.Me(), room)
			if err != nil {
				t.Fatal(err)
			}
			if n != want {
				t.Fatalf("seed %d %v: %d unread, want %d", seed, steps, n, want)
			}
			owedNow := a.reads.Take(me)
			for r, at := range owedNow {
				a.reads.Add(r, at)
			}
			got := owedNow[room]
			if got != owedTo {
				t.Fatalf("seed %d %v: LinkedIn is owed %d, want %d", seed, steps, got, owedTo)
			}
		}
	}
}

// History a listing carries is cached quietly; a live message is announced and streamed.
func TestOnlyLiveMessagesAreAnnounced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, announced := cachedAdapter(t)
	a.takeConversations(ctx, c, []linkedingo.Conversation{
		withMessages(conversation(group, "G", 2000, 0, false, me, dana, eli), message(group, "m1", eli, "old", 1000)),
	}, nil)
	a.event(ctx, c, liveMessage(message(group, "m2", eli, "new", 2000), 2000))
	system := message(group, "m3", eli, "Eli left", 3000)
	system.MessageBodyRenderFormat = linkedingo.MessageBodyRenderFormatSystem
	a.event(ctx, c, liveMessage(system, 3000))
	recalled := message(group, "m1", eli, "", 1000)
	recalled.MessageBodyRenderFormat = linkedingo.MessageBodyRenderFormatRecalled
	a.event(ctx, c, liveMessage(recalled, 3500)) // a recall is no news
	if len(*announced) != 1 || (*announced)[0].Body != "new" {
		t.Errorf("announced %+v, want only the live message", *announced)
	}
	msgs, err := cache.Messages(ctx, roomID(me, group), 10)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, m := range msgs {
		bodies = append(bodies, m.Body)
	}
	if strings.Join(bodies, ",") != ",new" {
		t.Errorf("cached %v, want the recalled one empty, then the new one", bodies)
	}
	select {
	case m := <-a.Messages():
		if m.Body != "new" {
			t.Errorf("streamed %q", m.Body)
		}
	case <-time.After(time.Second):
		t.Error("the live message was not streamed")
	}
}

// Events of topics kith does not take, and ones without their payload, change nothing.
func TestEventsWithoutWhatTheyCarry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, announced := cachedAdapter(t)
	for _, topic := range []string{
		linkedingo.RealtimeEventTopicMessages, linkedingo.RealtimeEventTopicConversationDelete,
		linkedingo.RealtimeEventTopicMessageSeenReceipts, linkedingo.RealtimeEventTopicTypingIndicators,
		linkedingo.RealtimeEventTopicConversations, "somethingNewTopic",
	} {
		a.event(ctx, c, liveEvent(topic, linkedingo.DecoratedEventData{}, 1))
	}
	noConv := message(group, "m1", dana, "lost", 1000)
	noConv.Conversation = linkedingo.Conversation{}
	a.event(ctx, c, liveMessage(noConv, 1000))
	a.event(ctx, c, liveEvent(linkedingo.RealtimeEventTopicConversations, linkedingo.DecoratedEventData{
		DecoratedConversation: &linkedingo.DecoratedConversation{Result: conversation(group, "G", 1, 0, true, me, dana, eli)},
	}, 1))
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"G"}) || len(*announced) != 0 {
		t.Errorf("rooms %v, announced %v", got, *announced)
	}
}

// Each account is a space holding its rooms; signing out forgets the session here (and,
// asked, the rooms) without telling LinkedIn.
func TestSpacesAndSigningOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	_ = saveSession(a.secrets, "li", aSession(me, "x"))
	a.takeConversations(ctx, c, []linkedingo.Conversation{
		withMessages(conversation(group, "G", 2000, 2000, true, me, dana, eli), message(group, "m1", eli, "hi", 1000)),
	}, nil)
	spaces, err := a.Spaces(ctx)
	if err != nil || len(spaces) != 1 || spaces[0].ID != accountSpaceID(me) || spaces[0].Name != "LinkedIn li" ||
		!slices.Equal(spaces[0].Children, []domain.RoomID{roomID(me, group)}) {
		t.Fatalf("spaces = %+v, %v", spaces, err)
	}
	if p, _ := a.CanonicalParent(ctx, roomID(me, group)); p != accountSpaceID(me) {
		t.Errorf("parent = %s", p)
	}
	if p, _ := a.CanonicalParent(ctx, "!x:example.org"); p != "" {
		t.Errorf("parent of a Matrix room = %s", p)
	}
	members, _ := a.Members(ctx, roomID(me, group), 0)
	ranked, _ := a.MentionCandidates(ctx, roomID(me, group), 10)
	if len(members) != 3 || len(ranked) != 3 {
		t.Errorf("members %d, candidates %d", len(members), len(ranked))
	}
	page, err := a.Timeline(ctx, roomID(me, group), "", 0)
	if err != nil || len(page.Messages) != 1 {
		t.Errorf("timeline = %+v, %v", page, err)
	}
	if older, _ := a.Timeline(ctx, roomID(me, group), string(msgID(group, "m1")), 10); len(older.Messages) != 0 {
		t.Errorf("an older page = %+v", older)
	}
	if m, ferr := a.FetchEvent(ctx, roomID(me, group), msgID(group, "m1")); ferr != nil || m.Body != "hi" {
		t.Errorf("fetch = %+v, %v", m, ferr)
	}
	if _, ferr := a.FetchEvent(ctx, roomID(me, group), msgID(group, "nope")); !errors.Is(ferr, api.ErrNoSuchMessage) {
		t.Errorf("fetching an uncached message = %v, want ErrNoSuchMessage", ferr)
	}
	if enc, _ := a.RoomEncryption(ctx, []domain.RoomID{roomID(me, group)}); enc[roomID(me, group)] {
		t.Error("a LinkedIn room is encrypted")
	}
	unread, err := a.CachedUnread(ctx)
	if err != nil || len(unread) != 1 || !unread[0].Counted {
		t.Errorf("cached unread = %+v, %v", unread, err)
	}
	// Without a read position, LinkedIn's unread flag is what shows.
	a.takeConversations(ctx, c, []linkedingo.Conversation{conversation(danaDM, "", 2000, 0, false, me, dana)}, nil)
	unread, _ = a.CachedUnread(ctx)
	flagged := 0
	for _, u := range unread {
		if u.RoomID == roomID(me, danaDM) {
			flagged = u.Notifications
		}
	}
	if flagged != 1 {
		t.Errorf("an unread conversation with no read position shows %d", flagged)
	}
	if err := a.SignOut(ctx, "linkedin:nobody/account", true); err == nil {
		t.Error("signing out a space that is no account's was taken")
	}
	if err := a.SignOut(ctx, accountSpaceID(me), true); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := loadSession(a.secrets, "li"); ok {
		t.Error("the session is still kept")
	}
	if !c.closed() {
		t.Error("the connection is still open")
	}
	if got := cachedRooms(t, cache); len(got) != 0 {
		t.Errorf("rooms after signing out and forgetting = %v", got)
	}
	if spaces, _ := a.Spaces(ctx); len(spaces) != 0 {
		t.Errorf("spaces after signing out = %+v", spaces)
	}
}

// Under [display.deleted] keep, an edit keeps the words it replaced.
func TestAnEditKeepsWhatItReplaced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, keep := range []bool{false, true} {
		a, c, _, _ := cachedAdapter(t)
		a.UseConfig(ctx, withAccounts("li"))
		a.keepDeleted.Store(keep)
		a.takeConversations(ctx, c, []linkedingo.Conversation{
			withMessages(conversation(group, "G", 1000, 0, true, me, dana, eli), message(group, "m1", dana, "first", 1000)),
		}, nil)
		edit := message(group, "m1", dana, "second", 1000)
		edit.MessageBodyRenderFormat = linkedingo.MessageBodyRenderFormatEdited
		a.event(ctx, c, liveMessage(edit, 2000))
		revisions, _, err := a.MessageHistory(ctx, roomID(me, group), msgID(group, "m1"))
		if err != nil {
			t.Fatal(err)
		}
		var bodies []string
		for _, r := range revisions {
			bodies = append(bodies, r.Body)
		}
		if keep != slices.Contains(bodies, "first") {
			t.Errorf("keep=%v: versions %v", keep, bodies)
		}
	}
}

// Leaving needs LinkedIn: a room of another network is refused, and an account not
// connected says so rather than dropping the conversation only here.
func TestLeavingNeedsLinkedIn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	a.takeConversations(ctx, c, []linkedingo.Conversation{conversation(group, "G", 1000, 0, true, me, dana, eli)}, nil)
	if err := a.LeaveRoom(ctx, "!room:example.org"); !errors.Is(err, errNotAConversation) {
		t.Errorf("a Matrix room = %v", err)
	}
	if err := a.LeaveRoom(ctx, roomID(me, group)); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("not connected = %v", err)
	}
	if err := a.LeaveRoom(ctx, roomID(eli, group)); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("another account's room = %v", err)
	}
	if got := cachedRooms(t, cache); len(got) != 1 {
		t.Errorf("a refused leave dropped the room: %v", got)
	}
}

// A first listing that stops short keeps no sync token, so the next sync lists the
// whole inbox again rather than only what changed since.
func TestAListingThatStopsShortKeepsNoSyncToken(t *testing.T) {
	t.Parallel()
	a, _, _, _ := cachedAdapter(t)
	s := aSession(me, "y")
	s.SyncToken = "token-from-an-earlier-sync"
	secrets := a.secrets.(*memSecrets)
	c := a.newConn(Account{Name: "li"}, s, nil)
	a.adopt(c)
	c.client = a.clientFor(context.Background(), s, linkedingo.Handlers{})
	stopped, cancel := context.WithCancel(context.Background())
	cancel() // every page fails, before any request goes out
	a.sync(stopped, c, "session-1")
	if c.session.SyncToken != "" {
		t.Errorf("the token kept in memory = %q, want none", c.session.SyncToken)
	}
	kept, ok, err := secrets.Secret(sessionRef("li"))
	if err != nil || !ok || strings.Contains(kept, "token-from-an-earlier-sync") {
		t.Errorf("the saved session still carries the token (%v, %v): %s", ok, err, kept)
	}
	if c.synced == "session-1" {
		t.Error("a sync that listed nothing counts as done")
	}
}

// Typists are streamed on change, through the realtime stream, never yourself; one
// whose notices stop is forgotten.
func TestTypingIsStreamedOnChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, _, _ := cachedAdapter(t)
	typing := func(thread, who string) *linkedingo.DecoratedEvent {
		return liveEvent(linkedingo.RealtimeEventTopicTypingIndicators, linkedingo.DecoratedEventData{
			DecoratedTypingIndicator: &linkedingo.DecoratedTypingIndicator{Result: linkedingo.RealtimeTypingIndicator{
				TypingParticipant: people[who], Conversation: linkedingo.Conversation{EntityURN: conversationURN(me, thread)},
			}},
		}, 1000)
	}
	room := roomID(me, danaDM)
	a.event(ctx, c, typing(danaDM, me)) // yourself, on another device
	a.event(ctx, c, typing(danaDM, dana))
	a.event(ctx, c, typing(danaDM, dana)) // still typing: nothing new
	select {
	case got := <-a.Activity():
		if got.RoomID != room || !slices.Equal(got.Typing, []string{personID(dana)}) {
			t.Errorf("activity = %+v, want Dana typing", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Dana typing was not streamed")
	}
	select {
	case more := <-a.Activity():
		t.Errorf("a repeated notice streamed %+v", more)
	default:
	}
	a.stoppedTyping(room, personID(dana))
	if gone := <-a.Activity(); gone.RoomID != room || len(gone.Typing) != 0 {
		t.Errorf("after the notices stopped: %+v", gone)
	}
}
