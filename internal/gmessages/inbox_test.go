package gmessages

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Invented numbers and IDs, in the shapes the phone uses.
const (
	phoneID = "someone@example.org/1234567890"
	meNum   = "+15550000001"
	danaNum = "+15550000002"
	eliNum  = "+15550000003"
	danaDM  = "11"
	group   = "12"
)

var self = selfOf(phoneID)

func participant(pid, number, name string, me bool) *gmproto.Participant {
	return &gmproto.Participant{
		ID:       &gmproto.SmallInfo{Type: gmproto.IdentifierType_PHONE, Number: number, ParticipantID: pid},
		FullName: name, IsMe: me, IsVisible: true,
	}
}

var (
	pMe   = participant("1", meNum, "", true)
	pDana = participant("2", danaNum, "Dana Reyes", false)
	pEli  = participant("3", eliNum, "Eli Stone", false)
)

// conversation is a conversation as the phone sends it; ms are its times in ms.
func conversation(id, name string, group bool, lastMs int64, unread bool, status gmproto.ConversationStatus, who ...*gmproto.Participant) *gmproto.Conversation {
	return &gmproto.Conversation{
		ConversationID: id, Name: name, IsGroupChat: group, LastMessageTimestamp: lastMs * 1000,
		Unread: unread, Status: status, Participants: who,
	}
}

func active(id, name string, group bool, lastMs int64, unread bool, who ...*gmproto.Participant) *gmproto.Conversation {
	return conversation(id, name, group, lastMs, unread, gmproto.ConversationStatus_ACTIVE, who...)
}

// text is a message as the phone sends it.
func text(conv, id, from, body string, ms int64, status gmproto.MessageStatusType) *gmproto.Message {
	return &gmproto.Message{
		MessageID: id, ConversationID: conv, ParticipantID: from, Timestamp: ms * 1000,
		MessageStatus: &gmproto.MessageStatus{Status: status},
		MessageInfo:   []*gmproto.MessageInfo{{Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: body}}}},
	}
}

func person(number string) string { return domain.NativePerson(domain.ProtocolGMessages, number) }

// The phone's IDs survive kith's whatever they hold; the account part has no address
// and no slash; a person with a number is named by it.
func TestIDs(t *testing.T) {
	t.Parallel()

	if strings.ContainsAny(self, "/@") || self == "" || selfOf(phoneID) != self || selfOf("other@example.org/1") == self {
		t.Errorf("self = %q", self)
	}
	for _, conversation := range []string{"11", "a/b", "x%2Fy", "sp ace"} {
		room := roomID(self, conversation)
		s, got, ok := conversationOf(room)
		if !ok || s != self || got != conversation || !domain.IsRoomID(string(room)) {
			t.Errorf("conversationOf(%s) = %q %q %v", room, s, got, ok)
		}
		event := eventID(self, conversation, "m/1")
		if m, ok := messageOf(event); !ok || m != "m/1" || domain.IsRoomID(string(event)) {
			t.Errorf("messageOf(%s) = %q %v", event, m, ok)
		}
	}
	for _, bad := range []domain.RoomID{"!r:example.org", "gmessages:" + domain.RoomID(self) + "/a/b", "gmessages:" + domain.RoomID(self) + "/%zz"} {
		if _, _, ok := conversationOf(bad); ok {
			t.Errorf("conversationOf(%s) took it", bad)
		}
	}
	for _, bad := range []domain.EventID{"$e:example.org", "gmessages:x/onlyroom", "gmessages:x/c/%zz"} {
		if _, ok := messageOf(bad); ok {
			t.Errorf("messageOf(%s) took it", bad)
		}
	}
	if got := personOf(self, pDana.GetID()); got != person(danaNum) || domain.ShortName(got) != danaNum {
		t.Errorf("a number = %s (%s)", got, domain.ShortName(got))
	}
	email := &gmproto.SmallInfo{Type: gmproto.IdentifierType_EMAIL, Number: "dana@example.org", ParticipantID: "7"}
	if got := personOf(self, email); got != person(self+".7") || domain.ShortName(got) != "" {
		t.Errorf("an address = %s", got)
	}
}

// Conversations become rooms as the phone shows them: a direct one named after its
// person, a group by its name or its people; archived ones show, spam, blocked and
// deleted ones do not; you are who the phone marks as you.
func TestTheInboxKeepsConversationsAsThePhoneShowsThem(t *testing.T) {
	t.Parallel()

	in := newInbox(self)
	for _, c := range []*gmproto.Conversation{
		active(danaDM, "", false, 10, false, pMe, pDana),
		active(group, "Board games", true, 20, true, pMe, pDana, pEli),
		active("13", "", true, 30, false, pMe, pDana, pEli),
		conversation("14", "Old", false, 5, false, gmproto.ConversationStatus_ARCHIVED, pMe, pEli),
	} {
		if isShown, _ := in.take(c); !isShown {
			t.Errorf("%s not shown", c.GetConversationID())
		}
	}
	for _, status := range []gmproto.ConversationStatus{
		gmproto.ConversationStatus_SPAM_FOLDER, gmproto.ConversationStatus_BLOCKED_FOLDER,
		gmproto.ConversationStatus_DELETED, gmproto.ConversationStatus_TRASH_FOLDER,
	} {
		if isShown, _ := in.take(conversation("20", "x", false, 1, false, status, pMe, pDana)); isShown {
			t.Errorf("a conversation in %v was shown", status)
		}
	}
	names := map[string]bool{}
	for _, cv := range in.convs {
		r := in.room(cv)
		names[fmt.Sprintf("%s direct=%v members=%d", r.Name, r.IsDirect, len(r.Members))] = true
	}
	for _, want := range []string{
		"Dana Reyes direct=true members=2", "Board games direct=false members=3",
		"Dana Reyes, Eli Stone direct=false members=3", "Old direct=true members=2",
	} {
		if !names[want] {
			t.Errorf("no room %q in %v", want, names)
		}
	}
	if !in.me[person(meNum)] || len(in.me) != 1 {
		t.Errorf("me = %v", in.me)
	}
	if cv := in.convs[group]; !cv.unread || !cv.readTo.IsZero() {
		t.Errorf("an unread conversation: %+v", cv)
	}
	if cv := in.convs[danaDM]; cv.readTo.UnixMilli() != 10 {
		t.Errorf("a read conversation is read to %v", cv.readTo.UnixMilli())
	}
	// A conversation without its participants keeps the ones known; read times only
	// move forward.
	in.take(active(danaDM, "", false, 5, false))
	if cv := in.convs[danaDM]; len(cv.order) != 2 || cv.readTo.UnixMilli() != 10 {
		t.Errorf("a bare, older update: %+v", cv)
	}
	if isShown, _ := in.take(&gmproto.Conversation{}); isShown {
		t.Error("a conversation with no ID was shown")
	}
}

// What a message carries shows: its subject and text, attachments as labels, an MMS not
// downloaded yet saying so, a reply; the phone's notices do not show; a deleted message
// comes redacted.
func TestMessagesShowWhatTheyCarry(t *testing.T) {
	t.Parallel()

	in := newInbox(self)
	in.take(active(group, "G", true, 1, false, pMe, pDana, pEli))
	media := func(mime, name string) *gmproto.MessageInfo {
		return &gmproto.MessageInfo{Data: &gmproto.MessageInfo_MediaContent{MediaContent: &gmproto.MediaContent{MimeType: mime, MediaName: name}}}
	}
	withSubject := text(group, "m1", "2", "the body", 1000, gmproto.MessageStatusType_INCOMING_COMPLETE)
	withSubject.Subject = new("Subject")
	attachments := text(group, "m2", "3", "look", 2000, gmproto.MessageStatusType_INCOMING_COMPLETE)
	attachments.MessageInfo = append(attachments.MessageInfo, media("image/jpeg", ""), media("image/gif", ""),
		media("video/mp4", ""), media("audio/amr", ""), media("text/x-vcard", ""), media("application/pdf", "cv.pdf"),
		media("application/octet-stream", ""))
	pending := &gmproto.Message{MessageID: "m3", ConversationID: group, ParticipantID: "2", Timestamp: 3000000,
		MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_INCOMING_YET_TO_MANUAL_DOWNLOAD}}
	reply := text(group, "m4", "1", "agreed", 4000, gmproto.MessageStatusType_OUTGOING_DELIVERED)
	reply.ReplyMessage = &gmproto.ReplyMessage{MessageID: "m1"}
	stranger := text(group, "m5", "9", "hello", 5000, gmproto.MessageStatusType_INCOMING_COMPLETE)
	stranger.SenderParticipant = participant("9", "+15550000009", "Newcomer", false)

	for m, want := range map[*gmproto.Message]string{
		withSubject: "Subject\nthe body",
		attachments: "look\n[photo]\n[GIF]\n[video]\n[voice message]\n[contact]\n[file: cv.pdf]\n[attachment]",
		pending:     "[MMS not downloaded yet]",
		reply:       "agreed",
		stranger:    "hello",
	} {
		got, ok := in.message(m)
		if !ok || got.Body != want || got.RoomID != roomID(self, group) || got.Timestamp.UnixMilli() != m.GetTimestamp()/1000 {
			t.Errorf("%s: %q (%v) room %s", m.GetMessageID(), got.Body, ok, got.RoomID)
		}
	}
	if got, _ := in.message(reply); got.ReplyTo != eventID(self, group, "m1") || got.Sender != person(meNum) {
		t.Errorf("reply: to %s from %s", got.ReplyTo, got.Sender)
	}
	if got, _ := in.message(withSubject); got.Sender != person(danaNum) || got.SenderName != "Dana Reyes" {
		t.Errorf("sender %s %q", got.Sender, got.SenderName)
	}
	if got, _ := in.message(stranger); got.Sender != person("+15550000009") || got.SenderName != "Newcomer" {
		t.Errorf("a sender not among the participants: %s %q", got.Sender, got.SenderName)
	}
	for _, status := range []gmproto.MessageStatusType{
		gmproto.MessageStatusType_MESSAGE_DELETED, gmproto.MessageStatusType_OUTGOING_DELETED, gmproto.MessageStatusType_INCOMING_DELETED,
	} {
		if got, ok := in.message(text(group, "m6", "2", "gone", 6000, status)); !ok || !got.Redacted || got.RedactedBy != person(danaNum) {
			t.Errorf("deleted (%v) = %+v", status, got)
		}
	}
	for _, m := range []*gmproto.Message{
		text(group, "m7", "2", "", 7000, gmproto.MessageStatusType_TOMBSTONE_PARTICIPANT_JOINED),
		text(group, "m8", "2", "RCS chat", 8000, gmproto.MessageStatusType_TOMBSTONE_PROTOCOL_SWITCH_TO_RCS),
		text(group, "m9", "2", "  ", 9000, gmproto.MessageStatusType_INCOMING_COMPLETE),
		text(group, "", "2", "no id", 9000, gmproto.MessageStatusType_INCOMING_COMPLETE),
	} {
		if got, ok := in.message(m); ok {
			t.Errorf("shown: %+v", got)
		}
	}
	orphan := text("99", "m10", "", "who", 10, gmproto.MessageStatusType_INCOMING_COMPLETE)
	if got, ok := in.message(orphan); !ok || got.Sender != person(self+".") {
		t.Errorf("a message in an unknown conversation: %+v", got)
	}
}

// cachedAdapter is an adapter over a real cache with one connection, its announced
// messages collected.
func cachedAdapter(t *testing.T) (*Adapter, *conn, *db.Cache, *[]domain.Message) {
	t.Helper()
	cache, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	a := New(cache, newSecrets(), nil, nil)
	var announced []domain.Message
	a.OnCached(func(m domain.Message) { announced = append(announced, m) }, nil)
	a.UseConfig(context.Background(), withAccounts("phone"))
	c := a.newConn(Account{Name: "phone"}, aSession(phoneID, "x"), nil)
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
		if domain.NetworkOf(string(rooms[i].ID)) == domain.ProtocolGMessages {
			names = append(names, rooms[i].Name)
		}
	}
	slices.Sort(names)
	return names
}

// A later, shorter listing must not sweep a conversation an earlier one listed or the
// cache held; a conversation leaving the inbox (deleted, spam) takes it away, and a
// listing fetched before that does not bring it back; new activity does.
func TestConversationsComeAndGo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	now := time.Now().UnixMilli()
	a.takeConversations(ctx, c, []*gmproto.Conversation{
		active(group, "Board games", true, now-3_600_000, false, pMe, pDana, pEli),
		active(danaDM, "", false, now-7_200_000, false, pMe, pDana),
	})
	c2 := a.newConn(Account{Name: "phone"}, aSession(phoneID, "y"), nil)
	a.adopt(c2)
	a.seedRooms(ctx, c2)
	a.takeConversations(ctx, c2, []*gmproto.Conversation{active("13", "New", true, now, false, pMe, pDana, pEli)})
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"Board games", "Dana Reyes", "New"}) {
		t.Errorf("after a shorter listing: %v", got)
	}
	stale := active(group, "Board games", true, now-3_600_000, false, pMe, pDana, pEli)
	c2.enqueue(conversation(group, "Board games", true, now-3_600_000, false, gmproto.ConversationStatus_SPAM_FOLDER, pMe, pDana, pEli))
	a.take(ctx, c2, <-c2.queue)
	a.takeConversations(ctx, c2, []*gmproto.Conversation{stale})
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"Dana Reyes", "New"}) {
		t.Errorf("after spam and a stale listing: %v", got)
	}
	a.takeConversations(ctx, c2, []*gmproto.Conversation{active(group, "Board games", true, now+60_000, false, pMe, pDana, pEli)})
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"Board games", "Dana Reyes", "New"}) {
		t.Errorf("new activity did not bring it back: %v", got)
	}
	// Once back, an update that carries no time (a rename) keeps it.
	a.takeConversations(ctx, c2, []*gmproto.Conversation{active(group, "Board games night", true, 0, false, pMe, pDana, pEli)})
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"Board games night", "Dana Reyes", "New"}) {
		t.Errorf("an update after coming back: %v", got)
	}
	a.takeConversations(ctx, c2, []*gmproto.Conversation{
		conversation(group, "", true, 1, false, gmproto.ConversationStatus_DELETED),
		conversation(danaDM, "", false, 1, false, gmproto.ConversationStatus_DELETED),
		conversation("13", "", true, 1, false, gmproto.ConversationStatus_DELETED),
	})
	if got := cachedRooms(t, cache); len(got) != 0 {
		t.Errorf("after deleting them all: %v", got)
	}
}

// One message reaches kith three ways: a listing's copy, live as sent, and live again
// with every change of status, deletion among them. Whatever the order, a deleted
// message stays deleted, and one never deleted shows its text.
func TestAListingNeverUndoesADeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(5, 8))
	a, c, cache, _ := cachedAdapter(t)
	for run := range 60 {
		for _, keep := range []bool{false, true} {
			conv := fmt.Sprintf("c%d-%v", run, keep)
			a.takeConversations(ctx, c, []*gmproto.Conversation{active(conv, "G", true, 1, false, pMe, pDana, pEli)})
			a.keepDeleted.Store(keep)
			sent := text(conv, "m1", "2", "words", 1000, gmproto.MessageStatusType_INCOMING_COMPLETE)
			displayed := text(conv, "m1", "2", "words", 1000, gmproto.MessageStatusType_INCOMING_DISPLAYED)
			gone := text(conv, "m1", "2", "", 1000, gmproto.MessageStatusType_MESSAGE_DELETED)
			steps := []struct {
				name string
				do   func()
			}{
				{"listed", func() { a.takeMessages(ctx, c, []*gmproto.Message{sent}, false) }},
				{"live", func() { a.take(ctx, c, &libgm.WrappedMessage{Message: sent}) }},
				{"read live", func() { a.take(ctx, c, &libgm.WrappedMessage{Message: displayed}) }},
				{"replayed old", func() { a.take(ctx, c, &libgm.WrappedMessage{Message: sent, IsOld: true}) }},
				{"deleted live", func() { a.take(ctx, c, &libgm.WrappedMessage{Message: gone}) }},
				{"listed deleted", func() { a.takeMessages(ctx, c, []*gmproto.Message{gone}, false) }},
			}
			var names []string
			deletedLive, seen := false, false
			for range 1 + rng.IntN(5) {
				s := steps[rng.IntN(len(steps))]
				s.do()
				names = append(names, s.name)
				switch s.name {
				case "deleted live":
					deletedLive = true
				case "listed deleted":
				default:
					seen = true
				}
			}
			got, ok, err := cache.MessageByID(ctx, roomID(self, conv), eventID(self, conv, "m1"))
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case deletedLive:
				if ok && !got.Redacted {
					t.Fatalf("run %d keep=%v: after %v the message shows %q, not deleted", run, keep, names, got.Body)
				}
				if ok && !keep && got.Body != "" {
					t.Fatalf("run %d: after %v a deleted message keeps %q", run, names, got.Body)
				}
			case seen:
				if !ok || got.Redacted || got.Body != "words" {
					t.Fatalf("run %d keep=%v: after %v the message is %+v (%v)", run, keep, names, got, ok)
				}
			}
		}
	}
}

// History a listing carries is cached quietly; a live message is announced and
// streamed once, not again with each change of its status.
func TestOnlyNewLiveMessagesAreAnnounced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, announced := cachedAdapter(t)
	a.takeConversations(ctx, c, []*gmproto.Conversation{active(group, "G", true, 2000, true, pMe, pDana, pEli)})
	a.takeMessages(ctx, c, []*gmproto.Message{text(group, "m1", "3", "old", 1000, gmproto.MessageStatusType_INCOMING_COMPLETE)}, false)
	a.take(ctx, c, &libgm.WrappedMessage{Message: text(group, "m2", "3", "new", 2000, gmproto.MessageStatusType_INCOMING_COMPLETE)})
	a.take(ctx, c, &libgm.WrappedMessage{Message: text(group, "m3", "3", "replayed", 1500, gmproto.MessageStatusType_INCOMING_COMPLETE), IsOld: true})
	if len(*announced) != 1 || (*announced)[0].Body != "new" {
		t.Errorf("announced %+v, want only the new live message", *announced)
	}
	msgs, err := cache.Messages(ctx, roomID(self, group), 10)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, m := range msgs {
		bodies = append(bodies, m.Body)
	}
	if strings.Join(bodies, ",") != "old,replayed,new" {
		t.Errorf("cached %v", bodies)
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

// A conversation read in kith stays read whatever the phone says after; only a newer
// message makes it unread. The phone not having taken the mark, it is owed.
func TestAConversationReadInKithStaysRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	room := roomID(self, danaDM)
	a.takeConversations(ctx, c, []*gmproto.Conversation{active(danaDM, "", false, 1000, false, pMe, pDana)})
	listed := active(danaDM, "", false, 3000, true, pMe, pDana)
	a.takeConversations(ctx, c, []*gmproto.Conversation{listed})
	a.takeMessages(ctx, c, []*gmproto.Message{
		text(danaDM, "m2", "2", "two", 2000, gmproto.MessageStatusType_INCOMING_COMPLETE),
		text(danaDM, "m3", "2", "three", 3000, gmproto.MessageStatusType_INCOMING_COMPLETE),
	}, false)
	unread := func() int {
		n, _, counted, err := cache.CountUnread(ctx, a.Me(), room)
		if err != nil || !counted {
			t.Fatalf("count: %d, counted %v, %v", n, counted, err)
		}
		return n
	}
	if n := unread(); n != 2 {
		t.Fatalf("before reading: %d, want 2", n)
	}
	if err := a.MarkRead(ctx, room, eventID(self, danaDM, "m3"), false); err != nil {
		t.Fatal(err)
	}
	a.takeConversations(ctx, c, []*gmproto.Conversation{listed}) // the phone's stale view
	if n := unread(); n != 0 {
		t.Errorf("after reading in kith and the phone's stale view: %d", n)
	}
	if owed := a.reads.take(self); owed[danaDM].message != "m3" {
		t.Errorf("the mark is not owed: %+v", owed)
	}
	a.take(ctx, c, &libgm.WrappedMessage{Message: text(danaDM, "m4", "2", "four", 4000, gmproto.MessageStatusType_INCOMING_COMPLETE)})
	if n := unread(); n != 1 {
		t.Errorf("after a newer message: %d", n)
	}
	// The phone saying it is read reads it here.
	a.takeConversations(ctx, c, []*gmproto.Conversation{active(danaDM, "", false, 4000, false, pMe, pDana)})
	if n := unread(); n != 0 {
		t.Errorf("after reading on the phone: %d", n)
	}
	if res, err := a.MarkRoomsRead(ctx, []domain.RoomID{room, roomID(self, "none")}, false); err != nil || res.Marked != 1 || res.Skipped != 1 {
		t.Errorf("MarkRoomsRead = %+v, %v", res, err)
	}
	if err := a.MarkRead(ctx, "!x:example.org", "", false); err == nil {
		t.Error("a Matrix room was taken")
	}
	if err := a.MarkRoomUnread(ctx, room, true); err == nil {
		t.Error("marking unread said it was done")
	}
}

// Read marks owed: a later one replaces an earlier, settling an earlier one leaves a
// later, and taking empties them.
func TestOwedMarks(t *testing.T) {
	t.Parallel()

	var o owed
	t0 := time.UnixMilli(1000)
	o.add("s", "c", mark{message: "m1", at: t0})
	o.add("s", "c", mark{message: "m2", at: t0.Add(time.Second)})
	o.add("s", "c", mark{message: "m0", at: t0.Add(-time.Second)})
	o.settle("s", "c", mark{message: "m1", at: t0})
	if got := o.take("s"); got["c"].message != "m2" {
		t.Errorf("owed = %+v", got)
	}
	if got := o.take("s"); len(got) != 0 {
		t.Errorf("owed again = %+v", got)
	}
	o.add("s", "c", mark{message: "m1", at: t0})
	o.settle("s", "c", mark{message: "m1", at: t0})
	if got := o.take("s"); len(got) != 0 {
		t.Errorf("a settled mark is still owed: %+v", got)
	}
}

// Each paired phone is a space holding its rooms; signing out forgets the pairing here
// (and, asked, the rooms).
func TestSpacesAndSigningOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	_ = saveSession(a.secrets, "phone", aSession(phoneID, "x"))
	a.takeConversations(ctx, c, []*gmproto.Conversation{active(group, "G", true, 2000, false, pMe, pDana, pEli)})
	a.takeMessages(ctx, c, []*gmproto.Message{text(group, "m1", "3", "hi", 1000, gmproto.MessageStatusType_INCOMING_COMPLETE)}, false)
	spaces, err := a.Spaces(ctx)
	if err != nil || len(spaces) != 1 || spaces[0].ID != accountSpaceID(self) || spaces[0].Name != "Google Messages phone" ||
		!slices.Equal(spaces[0].Children, []domain.RoomID{roomID(self, group)}) {
		t.Fatalf("spaces = %+v, %v", spaces, err)
	}
	if p, _ := a.CanonicalParent(ctx, roomID(self, group)); p != accountSpaceID(self) {
		t.Errorf("parent = %s", p)
	}
	if me := a.Me(); !slices.Equal(me, []string{person(meNum)}) {
		t.Errorf("Me = %v", me)
	}
	members, _ := a.Members(ctx, roomID(self, group), 0)
	ranked, _ := a.MentionCandidates(ctx, roomID(self, group), 10)
	if len(members) != 3 || len(ranked) != 3 {
		t.Errorf("members %d, candidates %d", len(members), len(ranked))
	}
	if page, err := a.Timeline(ctx, roomID(self, group), "", 0); err != nil || len(page.Messages) != 1 {
		t.Errorf("timeline = %+v, %v", page, err)
	}
	if m, ferr := a.FetchEvent(ctx, roomID(self, group), eventID(self, group, "m1")); ferr != nil || m.Body != "hi" {
		t.Errorf("fetch = %+v, %v", m, ferr)
	}
	if unread, uerr := a.CachedUnread(ctx); uerr != nil || len(unread) != 1 || !unread[0].Counted {
		t.Errorf("cached unread = %+v, %v", unread, uerr)
	}
	if err := a.SignOut(ctx, "gmessages:nobody/account", true); err == nil {
		t.Error("signing out a space that is no account's was taken")
	}
	if err := a.SignOut(ctx, accountSpaceID(self), true); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := loadSession(a.secrets, "phone"); ok || !c.closed() {
		t.Error("the pairing is kept, or the connection open")
	}
	if got := cachedRooms(t, cache); len(got) != 0 {
		t.Errorf("rooms after signing out = %v", got)
	}
}

// The queue hands events to the worker in order and lets go when the connection does.
func TestTheQueueKeepsOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	done := make(chan struct{})
	go func() { a.work(ctx, c); close(done) }()
	c.enqueue(active(group, "G", true, 1, false, pMe, pDana, pEli))
	for i := range 20 {
		c.enqueue(&libgm.WrappedMessage{Message: text(group, fmt.Sprintf("m%02d", i), "2", fmt.Sprintf("n%02d", i), int64(1000+i), gmproto.MessageStatusType_INCOMING_COMPLETE)})
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if msgs, _ := cache.Messages(ctx, roomID(self, group), 50); len(msgs) == 20 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	msgs, _ := cache.Messages(ctx, roomID(self, group), 50)
	if len(msgs) != 20 || msgs[0].Body != "n00" || msgs[19].Body != "n19" {
		t.Errorf("worked %d messages", len(msgs))
	}
	c.close()
	<-done
	// A closed connection drops what libgm hands it, even with its queue full, rather
	// than hold libgm's goroutine.
	dropped := make(chan struct{})
	go func() {
		for range queueCapacity + 1 {
			c.enqueue(active("13", "late", true, 1, false))
		}
		close(dropped)
	}()
	select {
	case <-dropped:
	case <-time.After(5 * time.Second):
		t.Error("a closed connection's full queue held its caller")
	}
}
